package cache

import (
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// patterns.go — 型態掃描:每小時收盤後,對成交額前 200 幣的 1H K,把 16 種經典型態全部評分一次。
//
// 做法:先用 ZigZag 找波段轉折點(四種尺度,小型態 / 大型態都抓得到),再依每種型態該有的條件
// (前段趨勢、高低點排列、趨勢線收斂 / 平行、貼合度、量縮…)各項打 0~100 分,加權成「相似度」。
// 看跌型態 = 把 K 線上下翻轉後用看漲型態的規則判斷(雙重頂 = 翻過來的雙重底),所以兩邊口徑一致。
// 相似度只代表「長得多像教科書型態」,不是勝率。
//
// 只收「現在進行中」的型態:最後一個轉折點要夠新;已經突破的只收 5 根內剛突破、還站在突破價外的。
// 細節規則在 patterns_detect.go。

// PatternPart 是相似度的一個分項。
type PatternPart struct {
	Name  string `json:"name"`
	Score int    `json:"score"`
	Note  string `json:"note"`
}

// PatternPivot / PatternLine 是給前端畫圖用的轉折點與線段(i = 圖上第幾根 K)。
type PatternPivot struct {
	I  int     `json:"i"`
	P  float64 `json:"p"`
	Hi bool    `json:"hi"`
}

type PatternLine struct {
	I1 float64 `json:"i1"`
	P1 float64 `json:"p1"`
	I2 float64 `json:"i2"`
	P2 float64 `json:"p2"`
}

type PatternChart struct {
	Candles [][4]float64   `json:"candles"` // [開, 高, 低, 收]
	Pivots  []PatternPivot `json:"pivots"`
	Lines   []PatternLine  `json:"lines"`
	Level   float64        `json:"level"` // 突破價
}

// PatternHit 是某個幣目前最像某型態的那一段。
type PatternHit struct {
	Coin     string        `json:"coin"`
	Score    int           `json:"score"`
	State    string        `json:"state"` // forming 形成中 / breakout 剛突破
	Bars     int           `json:"bars"`  // 型態跨幾根 K
	Breakout float64       `json:"breakout"`
	DistPct  float64       `json:"dist_pct"` // 現價離突破價 %
	Target   float64       `json:"target"`   // 等幅目標價
	Parts    []PatternPart `json:"parts"`
	Chart    PatternChart  `json:"chart"`
}

const (
	ptMinScore   = 50  // 低於這個不列
	ptMaxRows    = 40  // 每種型態最多回幾檔
	ptChartLead  = 30  // 圖上型態前面多畫幾根(看前段趨勢)
	ptChartMax   = 170 // 圖上最多幾根
	ptMaxBars    = 180 // 型態最長幾根
	ptMinBars    = 15  // 型態最短幾根
	ptBreakBars  = 5   // 突破後幾根內算「剛突破」
	ptMinCandles = 80
)

// ptScales:ZigZag 的轉折門檻(幾個 ATR),由小到大。
var ptScales = []float64{1.0, 1.5, 2.2, 3.0}

// ---- 轉折點、線、評分工具 ----

type pvt struct {
	i    int
	p    float64
	hi   bool
	tent bool // 最後一個還沒被確認的轉折(之後可能還會延伸)
}

// zigzag:價格從上一個極值反向走超過 dev 就確認一個轉折。高點用 High、低點用 Low。
func zigzag(cs []exchange.Candle, dev float64) []pvt {
	var out []pvt
	if len(cs) < 2 || dev <= 0 {
		return out
	}
	dir, hiI, loI := 0, 0, 0
	for i := 1; i < len(cs); i++ {
		switch dir {
		case 0:
			if cs[i].High > cs[hiI].High {
				hiI = i
			}
			if cs[i].Low < cs[loI].Low {
				loI = i
			}
			if cs[hiI].High-cs[loI].Low >= dev {
				if hiI > loI {
					out = append(out, pvt{i: loI, p: cs[loI].Low})
					dir = 1
				} else {
					out = append(out, pvt{i: hiI, p: cs[hiI].High, hi: true})
					dir = -1
				}
			}
		case 1: // 上漲段,找最高點
			if cs[i].High >= cs[hiI].High {
				hiI = i
			}
			if i > hiI && cs[hiI].High-cs[i].Low >= dev { // 反轉要發生在高點之後的 K(同一根大 K 不算)
				out = append(out, pvt{i: hiI, p: cs[hiI].High, hi: true})
				dir, loI = -1, i
			}
		case -1: // 下跌段,找最低點
			if cs[i].Low <= cs[loI].Low {
				loI = i
			}
			if i > loI && cs[i].High-cs[loI].Low >= dev {
				out = append(out, pvt{i: loI, p: cs[loI].Low})
				dir, hiI = 1, i
			}
		}
	}
	switch dir {
	case 1:
		out = append(out, pvt{i: hiI, p: cs[hiI].High, hi: true, tent: true})
	case -1:
		out = append(out, pvt{i: loI, p: cs[loI].Low, tent: true})
	}
	return out
}

// pline 是一條直線:在第 i 根的價格 = p0 + slope × (i − i0)。
type pline struct{ i0, p0, slope float64 }

func (l pline) at(i float64) float64 { return l.p0 + l.slope*(i-l.i0) }

// fitLine 用最小平方法把一條線穿過這些轉折點(只有一點就是水平線)。
func fitLine(ps []pvt) pline {
	if len(ps) == 1 {
		return pline{float64(ps[0].i), ps[0].p, 0}
	}
	var sx, sy, sxx, sxy float64
	for _, p := range ps {
		x := float64(p.i)
		sx, sy, sxx, sxy = sx+x, sy+p.p, sxx+x*x, sxy+x*p.p
	}
	n := float64(len(ps))
	den := n*sxx - sx*sx
	if den == 0 {
		return pline{sx / n, sy / n, 0}
	}
	slope := (n*sxy - sx*sy) / den
	return pline{sx / n, sy / n, slope}
}

// lineResid:轉折點離線的平均距離(價格)。
func lineResid(l pline, ps []pvt) float64 {
	s := 0.0
	for _, p := range ps {
		s += math.Abs(p.p - l.at(float64(p.i)))
	}
	return s / float64(len(ps))
}

// ramp 把 x 線性換成 0~100 分:x = bad → 0、x = good → 100(bad > good 也可以),超出就夾住。
func ramp(x, bad, good float64) float64 {
	if bad == good {
		if x >= good {
			return 100
		}
		return 0
	}
	t := (x - bad) / (good - bad)
	return math.Max(0, math.Min(1, t)) * 100
}

// pctx 是一次判斷的資料(看跌型態時 cs 已經上下翻轉)。
type pctx struct {
	cs   []exchange.Candle
	n    int
	atr  float64
	c    float64 // 最新一根已收的收盤
	bear bool
}

// w 依看漲 / 看跌挑說明文字(看跌是翻轉後判斷,原本的「高」在圖上是「低」)。
func (x *pctx) w(bull, bear string) string {
	if x.bear {
		return bear
	}
	return bull
}

// pct 把價差換成 %(翻轉後價格是負的,用絕對值當分母)。
func pct(delta, base float64) float64 {
	if base == 0 {
		return 0
	}
	return delta / math.Abs(base) * 100
}

func (x *pctx) minLow(from, to int) float64 { // [from, to)
	m := math.Inf(1)
	for i := max(0, from); i < min(to, x.n); i++ {
		m = math.Min(m, x.cs[i].Low)
	}
	return m
}

func (x *pctx) maxHigh(from, to int) float64 {
	m := math.Inf(-1)
	for i := max(0, from); i < min(to, x.n); i++ {
		m = math.Max(m, x.cs[i].High)
	}
	return m
}

// recent:型態最後一個轉折點要夠新(型態還在進行中)。
func (x *pctx) recent(last, length int) bool {
	return x.n-1-last <= max(8, int(0.45*float64(length)))
}

// breakState:從 from 那根之後看收盤有沒有站上突破價。沒有 → 形成中;5 根內剛突破而且現在還站在上面
// → 剛突破;更早就突破、或突破後又跌回來 → 不算(ok=false)。
func (x *pctx) breakState(from int, level func(i float64) float64) (string, bool) {
	first := -1
	for i := from + 1; i < x.n; i++ {
		if x.cs[i].Close > level(float64(i))+0.1*x.atr {
			first = i
			break
		}
	}
	if first < 0 {
		return "forming", true
	}
	if x.n-1-first > ptBreakBars || x.c <= level(float64(x.n-1)) {
		return "", false
	}
	return "breakout", true
}

// pcand 是一個候選型態(內部用,價格在翻轉後的座標)。
type pcand struct {
	score  float64
	parts  []PatternPart
	start  int // 型態起點(第一個轉折)
	piv    []pvt
	lines  []PatternLine
	level  float64 // 最新一根的突破價
	target float64
	state  string
}

// part 加一個分項。總分是「加權幾何平均」:任何一項很差都會把總分明顯拉低,
// 不會被其他高分項目蓋過去(一般平均的話,缺一個關鍵條件還是能拿高分)。
func (c *pcand) part(name string, score, weight float64, note string, wsum *float64) {
	c.parts = append(c.parts, PatternPart{Name: name, Score: int(math.Round(score)), Note: note})
	c.score += weight * math.Log(math.Max(score, 5)/100)
	*wsum += weight
}

func better(a, b *pcand) *pcand {
	if b != nil && (a == nil || b.score > a.score) {
		return b
	}
	return a
}

// segLine 把一條 pline 從 i1 畫到 i2。
func segLine(l pline, i1, i2 float64) PatternLine {
	return PatternLine{I1: i1, P1: l.at(i1), I2: i2, P2: l.at(i2)}
}

// ---- 型態清單與掃描 ----

type patDef struct {
	Key, Name string
	Bear      bool
	det       func(*pctx, []pvt) *pcand
}

var patDefs = []patDef{
	{"sym_tri_bull", "看漲對稱三角", false, detTriangle},
	{"sym_tri_bear", "看跌對稱三角", true, detTriangle},
	{"dbl_bottom", "雙重底", false, detDoubleBottom},
	{"dbl_top", "雙重頂", true, detDoubleBottom},
	{"bull_flag", "牛旗", false, detFlag},
	{"bear_flag", "熊旗", true, detFlag},
	{"fall_wedge", "看漲下降模型", false, detWedge},
	{"rise_wedge", "看跌上升模型", true, detWedge},
	{"tri_bottom", "三重底", false, detTripleBottom},
	{"tri_top", "三重頂", true, detTripleBottom},
	{"hs_bottom", "頭肩底", false, detHSBottom},
	{"hs_top", "頭肩頂", true, detHSBottom},
	{"cup_bull", "看漲杯柄", false, detCup},
	{"cup_bear", "看跌杯柄", true, detCup},
	{"round_bottom", "圓弧底", false, detRound},
	{"round_top", "圓弧頂", true, detRound},
}

// mirrorCandles 把 K 線上下翻轉(價格取負、高低對調),看跌型態就能用看漲規則判斷。
func mirrorCandles(cs []exchange.Candle) []exchange.Candle {
	out := make([]exchange.Candle, len(cs))
	for i, c := range cs {
		out[i] = exchange.Candle{Ts: c.Ts, Open: -c.Open, High: -c.Low, Low: -c.High, Close: -c.Close, Volume: c.Volume}
	}
	return out
}

// scanPatterns 對一個幣已收的 1H K 評 16 種型態,回傳每種型態最像的那一段(低於門檻的不回)。
func scanPatterns(coin string, cs []exchange.Candle) map[string]*PatternHit {
	out := map[string]*PatternHit{}
	if len(cs) < ptMinCandles {
		return out
	}
	atr := robustATR(cs, 14, 2)
	if atr <= 0 {
		return out
	}
	for _, bear := range []bool{false, true} {
		src := cs
		if bear {
			src = mirrorCandles(cs)
		}
		x := &pctx{cs: src, n: len(src), atr: atr, c: src[len(src)-1].Close, bear: bear}
		var sets [][]pvt
		for _, k := range ptScales {
			piv := zigzag(src, k*atr)
			sets = append(sets, piv)
			if n := len(piv); n > 0 && piv[n-1].tent { // 最後一段可能是突破後的新走勢 → 也試試不含它
				sets = append(sets, piv[:n-1])
			}
		}
		for _, d := range patDefs {
			if d.Bear != bear {
				continue
			}
			var best *pcand
			for _, piv := range sets {
				if len(piv) >= 2 {
					best = better(best, d.det(x, piv))
				}
			}
			if best != nil && best.score >= ptMinScore {
				out[d.Key] = toHit(coin, cs, best, bear)
			}
		}
	}
	return out
}

// toHit 把候選(翻轉座標)換回原本價格,並切出畫圖用的那一段 K。
func toHit(coin string, cs []exchange.Candle, c *pcand, bear bool) *PatternHit {
	sgn := 1.0
	if bear {
		sgn = -1
	}
	n := len(cs)
	from := max(0, c.start-ptChartLead)
	if n-from > ptChartMax {
		from = n - ptChartMax
	}
	h := &PatternHit{Coin: coin, Score: int(math.Round(c.score)), State: c.state, Bars: n - 1 - c.start,
		Breakout: roundPx(sgn * c.level), Target: roundPx(sgn * c.target), Parts: c.parts}
	last := cs[n-1].Close
	h.DistPct = r2((last - h.Breakout) / h.Breakout * 100)
	for _, k := range cs[from:] {
		h.Chart.Candles = append(h.Chart.Candles, [4]float64{k.Open, k.High, k.Low, k.Close})
	}
	for _, p := range c.piv {
		if p.i >= from {
			h.Chart.Pivots = append(h.Chart.Pivots, PatternPivot{I: p.i - from, P: roundPx(sgn * p.p), Hi: p.hi != bear})
		}
	}
	for _, l := range c.lines {
		f := float64(from)
		h.Chart.Lines = append(h.Chart.Lines, PatternLine{I1: l.I1 - f, P1: roundPx(sgn * l.P1), I2: l.I2 - f, P2: roundPx(sgn * l.P2)})
	}
	h.Chart.Level = h.Breakout
	return h
}

// ---- 排程與 API ----

type patternStore struct {
	mu      sync.Mutex
	hits    map[string]map[string]*PatternHit // 幣 → 型態 → 結果
	seen    map[string]int64                  // 幣 → 這次結果是哪一根 K 算的(收盤時間)
	updated int64
}

// PatternTick 每分鐘由 main 呼叫:整點收盤後把宇宙裡每個幣的 16 種型態重算一次。
func (s *Store) PatternTick() {
	if s.ptStore == nil {
		return
	}
	now := time.Now().UTC()
	coins := s.ptEng.due(now, s.wideUniverse)
	done := 0
	for _, coin := range coins {
		closed, _, ok := s.hourBars(coin, now)
		if !ok {
			continue
		}
		delete(s.ptEng.pend, coin)
		hits := scanPatterns(coin, closed)
		st := s.ptStore
		st.mu.Lock()
		st.hits[coin] = hits
		st.seen[coin] = closed[len(closed)-1].Ts
		st.updated = now.UnixMilli()
		st.mu.Unlock()
		done++
		time.Sleep(20 * time.Millisecond)
	}
	if done > 0 { // 兩小時沒更新的幣(掉出宇宙 / 下架)清掉
		cut := now.UnixMilli() - 3*dcBarMs
		s.ptStore.mu.Lock()
		for c, ts := range s.ptStore.seen {
			if ts < cut {
				delete(s.ptStore.hits, c)
				delete(s.ptStore.seen, c)
			}
		}
		s.ptStore.mu.Unlock()
	}
}

// PatternMeta 是型態清單(前端畫選單用)。
type PatternMeta struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Bear bool   `json:"bear"`
}

// PatternResult 是 /api/patterns 的回應:所有型態的分數摘要(算各型態有幾檔)+ 選中型態的完整列表。
type PatternResult struct {
	TF       string              `json:"tf"`
	Scanned  int                 `json:"scanned"`
	Updated  int64               `json:"updated"`
	Patterns []PatternMeta       `json:"patterns"`
	Summary  map[string][][2]int `json:"summary"` // 型態 → [[相似度, 1=剛突破 / 0=形成中], ...]
	Rows     []*PatternHit       `json:"rows"`
	Key      string              `json:"key"`
}

// Patterns 回選中型態 key 的列表(相似度降冪,最多 ptMaxRows 檔)和每種型態的分數摘要。
func (s *Store) Patterns(key string) PatternResult {
	res := PatternResult{TF: "1h", Summary: map[string][][2]int{}, Rows: []*PatternHit{}, Key: key}
	for _, d := range patDefs {
		res.Patterns = append(res.Patterns, PatternMeta{Key: d.Key, Name: d.Name, Bear: d.Bear})
		res.Summary[d.Key] = [][2]int{}
	}
	if s.ptStore == nil {
		return res
	}
	st := s.ptStore
	st.mu.Lock()
	defer st.mu.Unlock()
	res.Scanned, res.Updated = len(st.hits), st.updated
	for _, hs := range st.hits {
		for k, h := range hs {
			b := 0
			if h.State == "breakout" {
				b = 1
			}
			res.Summary[k] = append(res.Summary[k], [2]int{h.Score, b})
			if k == key {
				res.Rows = append(res.Rows, h)
			}
		}
	}
	sort.Slice(res.Rows, func(i, j int) bool {
		if res.Rows[i].Score != res.Rows[j].Score {
			return res.Rows[i].Score > res.Rows[j].Score
		}
		return res.Rows[i].Coin < res.Rows[j].Coin
	})
	if len(res.Rows) > ptMaxRows {
		res.Rows = res.Rows[:ptMaxRows]
	}
	return res
}
