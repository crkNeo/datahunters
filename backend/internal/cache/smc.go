package cache

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// smc.go — SMC 教練（多單）策略。定版規格 2026-07-24。
//
// 只做多。三週期:4H+1H 定方向、1H 選區域、15M 執行。每個幣種同時只有一個 setup
// 在跑,靠一個七步狀態機推進(全部在 15M 收盤判斷)。狀態機是「即時引擎」——
// 每根新收的 15M 棒推進一次;重啟後狀態歸零回 Stage 1(可接受,失效規則與逾時會
// 在數小時內重建 setup)。已開的單持久化在 paper_trades(book="smc"),獨立管理。
//
// 消融證據(來自規格,別亂動):
//   ・15M 樞紐 k=3(k>=4 多單翻負);逾時 80 根 15M(放寬會不穩)。
//   ・進場用「觸碰」模式(掛區域上緣);確認棒模式較弱。
//   ・TP 固定 1:2(掛最近阻力區期望≈0);方向必須 1H+4H 同時多(只看 4H test 全滅)。
//   ・不開空單鏡像(train 段為負)。

const (
	smcPivotK       = 3   // 15M 樞紐 k(嚴格大/小於前後各 k 根)
	smcHTFPivotK    = 3   // 1H/4H 樞紐 k
	smcTimeoutBars  = 80  // 逾時:離開 Stage 1 後 80 根 15M(20 小時)沒進場就重置
	smcRR           = 2.0 // 固定盈虧比 1:2
	smcZoneLookback = 100 // 區域回看:最近 N 根「該週期」棒內生成的 FVG 才算
	smcIDMLookback  = 200 // IDM / swing 掃描:最近 N 根 15M
	smcKlimit       = 750 // 取 750 根 15M(~7.8 天 → 約 46 根 4H、187 根 1H,足夠算趨勢+區域)
	smcBarSec       = 900 // 15M = 900 秒
	smcKeep         = 500 // 每個 book 保留的模擬單上限
)

// smcZone 是一個多方 FVG 需求區。top=上緣、bot=下緣、ts=生成棒的開盤時戳(ms)。
type smcZone struct {
	top, bot float64
	ts       int64
	valid    bool
}

// smcState 是單一幣種的狀態機。stage 1..7;stageStart 是離開 Stage 1 的那根 15M
// 開盤時戳(逾時基準)。
type smcState struct {
	stage      int
	stageStart int64
	hz         smcZone // 鎖定的 1H 需求區
	sweepLow   float64 // IDM 掃蕩的保護低點(= SL)
	lastSwing  float64 // 掃蕩前最近已確認 15M pivot high(MSS 基準)
	mssBar     int64   // MSS 成立的棒時戳
	bosLevel   float64
	bosBar     int64   // BOS 成立的棒時戳
	pz         smcZone // 15M 回踩進場區
}

// smcBook 是 SMC 策略的容器。states 是每幣的狀態機(記憶體,重啟歸零);trades 持久化。
type smcBook struct {
	mu      sync.Mutex
	states  map[string]*smcState
	trades  []*PaperTrade
	bucket  int64 // 上次處理的 15M 桶(避免同一根棒處理兩次)
	seeded  bool  // 開機第一次只建基準,不回補歷史進場
}

func newSMCBook() *smcBook { return &smcBook{states: map[string]*smcState{}} }

// ---- K 線工具:聚合、樞紐、趨勢、FVG ----

// aggregate 把 15M 收盤棒聚合成更高週期(factor=4→1H, 16→4H)。只回傳「已完結」的
// 高週期棒(該組必須湊滿 factor 根 15M),進行中的那根不算。
func smcAggregate(cs15 []exchange.Candle, factor int) []exchange.Candle {
	if factor <= 1 {
		return cs15
	}
	periodMs := int64(factor) * smcBarSec * 1000
	groups := map[int64][]exchange.Candle{}
	var order []int64
	for _, c := range cs15 {
		b := (c.Ts / periodMs) * periodMs
		if _, ok := groups[b]; !ok {
			order = append(order, b)
		}
		groups[b] = append(groups[b], c)
	}
	out := make([]exchange.Candle, 0, len(order))
	for _, b := range order {
		g := groups[b]
		if len(g) != factor { // 沒湊滿 → 進行中的高週期棒,跳過
			continue
		}
		hi, lo := g[0].High, g[0].Low
		for _, c := range g {
			if c.High > hi {
				hi = c.High
			}
			if c.Low < lo {
				lo = c.Low
			}
		}
		out = append(out, exchange.Candle{
			Ts: b, Open: g[0].Open, High: hi, Low: lo, Close: g[len(g)-1].Close,
		})
	}
	return out
}

// pivotHighIdx 回傳所有已確認 pivot high 的索引(strict:high 嚴格大於前後各 k 根)。
// 因為需要前後各 k 根,回傳的索引天然都是「已確認」的(i 落在 [k, n-1-k])。
func pivotHighIdx(cs []exchange.Candle, k int) []int {
	var out []int
	for i := k; i < len(cs)-k; i++ {
		h := cs[i].High
		ok := true
		for j := 1; j <= k && ok; j++ {
			if cs[i-j].High >= h || cs[i+j].High >= h {
				ok = false
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

func pivotLowIdx(cs []exchange.Candle, k int) []int {
	var out []int
	for i := k; i < len(cs)-k; i++ {
		l := cs[i].Low
		ok := true
		for j := 1; j <= k && ok; j++ {
			if cs[i-j].Low <= l || cs[i+j].Low <= l {
				ok = false
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// smcTrend 回傳該週期的結構趨勢:+1 多 / -1 空 / 0 未定。
// 規則:收盤 > 最近已確認 pivot high → 多;收盤 < 最近已確認 pivot low → 空;否則沿用。
// 逐棒重播並尊重 k 根確認延遲(pivot 在 i 根,要到 i+k 根才可用)。
func smcTrend(cs []exchange.Candle, k int) int {
	n := len(cs)
	if n < 2*k+2 {
		return 0
	}
	ph, pl := pivotHighIdx(cs, k), pivotLowIdx(cs, k)
	pi, li := 0, 0
	lastPH, lastPL := math.NaN(), math.NaN()
	trend := 0
	for t := 0; t < n; t++ {
		for pi < len(ph) && ph[pi]+k <= t {
			lastPH = cs[ph[pi]].High
			pi++
		}
		for li < len(pl) && pl[li]+k <= t {
			lastPL = cs[pl[li]].Low
			li++
		}
		c := cs[t].Close
		if !math.IsNaN(lastPH) && c > lastPH {
			trend = 1
		}
		if !math.IsNaN(lastPL) && c < lastPL {
			trend = -1
		}
	}
	return trend
}

// smcBullFVGs 找出多方 FVG(三棒缺口:low[j] > high[j-2] → 區域 [high[j-2], low[j]])。
// 只留:生成棒落在最近 lookback 根內、且生成後「沒有任何收盤跌破區域下緣」的。
func smcBullFVGs(cs []exchange.Candle, lookback int) []smcZone {
	n := len(cs)
	var out []smcZone
	start := 2
	if n-lookback > start {
		start = n - lookback
	}
	for j := start; j < n; j++ {
		if cs[j].Low <= cs[j-2].High {
			continue // 沒有缺口
		}
		z := smcZone{top: cs[j].Low, bot: cs[j-2].High, ts: cs[j].Ts, valid: true}
		for m := j + 1; m < n; m++ { // 生成後被收盤跌破下緣 → 失效
			if cs[m].Close < z.bot {
				z.valid = false
				break
			}
		}
		if z.valid {
			out = append(out, z)
		}
	}
	return out
}

// ---- Tick:排程進入點 ----

// SMCTick 每根新收的 15M 棒對所有幣種推進一次狀態機。
func (s *Store) SMCTick() {
	b := s.smcBook
	bkt := time.Now().UTC().Unix() / smcBarSec
	if bkt == b.bucket {
		return
	}
	b.bucket = bkt
	if !b.seeded { // 開機只建基準,不回補歷史(避免重啟灌一堆舊 setup)
		b.seeded = true
		return
	}
	now := time.Now().UTC()
	for _, coin := range s.emaCoins() {
		cs15, err := s.ex.BinanceKlines(coin+"USDT", "15m", smcKlimit)
		if err != nil || len(cs15) < 2 {
			continue
		}
		cs15 = cs15[:len(cs15)-1] // 丟掉進行中的那根
		if len(cs15) < 2*smcPivotK+2 {
			continue
		}
		s.smcRun(coin, cs15, now)
		time.Sleep(25 * time.Millisecond) // pace REST
	}
}

// smcRun 處理單一幣種的最新一根 15M 收盤棒:先管理已開單(出場),否則推進狀態機。
func (s *Store) smcRun(coin string, cs15 []exchange.Candle, now time.Time) {
	last := cs15[len(cs15)-1]

	b := s.smcBook
	b.mu.Lock()
	var open *PaperTrade
	for _, tr := range b.trades {
		if tr.Coin == coin && tr.Status == "open" {
			open = tr
			break
		}
	}

	var dirty *PaperTrade
	opened, closed := false, false

	if open != nil {
		// 出場:SL=sweepLow(固定)、TP 固定 1:2。保守假設同棒觸及 SL 與 TP → 以 SL 計。
		if last.Low <= open.SL {
			closeTrade(open, open.SL, "sl", now)
			closed = true
		} else if last.High >= open.TP {
			closeTrade(open, open.TP, "tp", now)
			closed = true
		} else {
			open.Cur = roundPx(last.Close)
			open.PnLPct = round2(pnl(open.Dir, open.Entry, last.Close))
		}
		dirty = open
		if closed {
			delete(b.states, coin) // 交易後 → 重置回 Stage 1
		}
	} else if s.StrategyEnabled("smc") {
		// 聚合高週期 + 趨勢
		t1h := smcTrend(smcAggregate(cs15, 4), smcHTFPivotK)
		t4h := smcTrend(smcAggregate(cs15, 16), smcHTFPivotK)
		st := b.states[coin]
		if st == nil {
			st = &smcState{stage: 1}
			b.states[coin] = st
		}
		if tr := s.smcAdvance(coin, st, cs15, t1h, t4h, now); tr != nil {
			b.trades = append(b.trades, tr)
			smcTrimBook(b)
			dirty = tr
			opened = true
			delete(b.states, coin) // 進場後狀態機重置(單子接手)
		}
	}
	b.mu.Unlock()

	if dirty != nil && s.db != nil {
		s.db.upsertTrade("smc", dirty)
	}
	if opened {
		s.notifyOpenBook("smc", dirty)
	}
	if closed {
		s.notifyCloseBook("smc", dirty, now, false)
	}
}

// smcAdvance 跑一根 15M 棒的失效檢查 + 狀態推進。回傳非 nil = 這根棒觸發進場。
// 呼叫時持有 b.mu。
func (s *Store) smcAdvance(coin string, st *smcState, cs15 []exchange.Candle, t1h, t4h int, now time.Time) *PaperTrade {
	last := cs15[len(cs15)-1]
	longOK := t1h == 1 && t4h == 1

	// ---- 失效規則(每棒收盤,優先於推進)----
	if st.stage > 1 {
		if !longOK { // 規則1:方向不再同時為多
			smcReset(st)
			return nil
		}
		if st.stageStart > 0 && (last.Ts-st.stageStart)/(smcBarSec*1000) > smcTimeoutBars { // 規則2:逾時
			smcReset(st)
			return nil
		}
	}
	if st.stage >= 3 && last.Close < st.hz.bot { // 規則3:跌破 1H 區下緣
		smcReset(st)
		return nil
	}
	if st.stage >= 4 && last.Close < st.sweepLow { // 規則4:跌破保護低點 → 退回 Stage 2
		st.stage = 2
	}
	if st.stage == 7 && last.Close < st.pz.bot { // 規則5:跌破掛單區下緣 → 退回 Stage 5(撤單)
		st.stage = 5
	}

	// ---- 狀態推進 ----
	switch st.stage {
	case 1: // 方向
		if longOK {
			st.stage = 2
			st.stageStart = last.Ts
		}

	case 2: // 觸碰 1H 需求區
		zones := smcBullFVGs(smcAggregate(cs15, 4), smcZoneLookback)
		for _, z := range zones {
			if last.Low <= z.top && last.Close >= z.bot { // 碰到任一有效區
				st.hz = z
				st.stage = 3
				break
			}
		}

	case 3: // IDM 掃蕩
		idm, ok := smcNearestPivotLowBelow(cs15, last.Close, smcIDMLookback)
		if ok && last.Low < idm && last.Close > idm { // 插針掃過、收盤收回
			sw, okS := smcLastPivotHigh(cs15)
			if okS {
				st.sweepLow = last.Low
				st.lastSwing = sw
				st.stage = 4
			}
		}

	case 4: // MSS
		if last.Close > st.lastSwing {
			st.mssBar = last.Ts
			st.stage = 5
		}

	case 5: // BOS:MSS 之後新形成且已確認的第一個 15M pivot high
		if lvl, ok := smcFirstPivotHighAfter(cs15, st.mssBar); ok {
			st.bosLevel = lvl
			if last.Close > st.bosLevel {
				st.bosBar = last.Ts
				st.stage = 6
			}
		}

	case 6: // 回踩區:bosBar 之後新生成、最近的、未跌破的 15M 多方 FVG
		if z, ok := smcLatestBullFVGAfter(cs15, st.bosBar); ok {
			st.pz = z
			st.stage = 7
		}

	case 7: // 進場(觸碰):掛限價在 pz.top,本棒 low<=pz.top 即成交
		if last.Low <= st.pz.top {
			entry := roundPx(st.pz.top)
			sl := roundPx(st.sweepLow)
			if sl <= 0 || entry <= sl { // 結構止損無效 → 放棄本次
				smcReset(st)
				return nil
			}
			// 後台「最大止損%」濾網(0=不限制)
			if !s.slWithinCap("smc", entry, sl) {
				smcReset(st)
				return nil
			}
			tp := roundPx(entry + smcRR*(entry-sl))
			t := time.UnixMilli(last.Ts).UTC()
			return &PaperTrade{
				ID:       fmt.Sprintf("smc|%s|%d", coin, now.UnixMilli()),
				Coin:     coin,
				Dir:      "long",
				Entry:    entry,
				SL:       sl,
				TP:       tp,
				Cur:      entry,
				Status:   "open",
				OpenTime: t,
			}
		}
	}
	return nil
}

// smcReset 清回 Stage 1(保留 map 佔位,下一棒重新評估)。
func smcReset(st *smcState) {
	*st = smcState{stage: 1}
}

// smcNearestPivotLowBelow 找價格下方最近(時間上最新)的已確認 15M pivot low,
// 值 < ref、且落在最近 lookback 根內。
func smcNearestPivotLowBelow(cs []exchange.Candle, ref float64, lookback int) (float64, bool) {
	pl := pivotLowIdx(cs, smcPivotK)
	n := len(cs)
	for i := len(pl) - 1; i >= 0; i-- { // 由新到舊
		idx := pl[i]
		if idx < n-lookback {
			break
		}
		if cs[idx].Low < ref {
			return cs[idx].Low, true
		}
	}
	return 0, false
}

// smcLastPivotHigh 回傳最近(時間上最新)的已確認 15M pivot high 值。
func smcLastPivotHigh(cs []exchange.Candle) (float64, bool) {
	ph := pivotHighIdx(cs, smcPivotK)
	if len(ph) == 0 {
		return 0, false
	}
	return cs[ph[len(ph)-1]].High, true
}

// smcFirstPivotHighAfter 回傳「棒時戳 > afterTs」的第一個(最早)已確認 pivot high。
func smcFirstPivotHighAfter(cs []exchange.Candle, afterTs int64) (float64, bool) {
	for _, idx := range pivotHighIdx(cs, smcPivotK) {
		if cs[idx].Ts > afterTs {
			return cs[idx].High, true
		}
	}
	return 0, false
}

// smcLatestBullFVGAfter 回傳生成於 afterTs 之後、最近的、未被跌破的 15M 多方 FVG。
func smcLatestBullFVGAfter(cs []exchange.Candle, afterTs int64) (smcZone, bool) {
	zones := smcBullFVGs(cs, len(cs)) // 全序列掃,再用 ts 過濾
	for i := len(zones) - 1; i >= 0; i-- {
		if zones[i].ts > afterTs {
			return zones[i], true
		}
	}
	return smcZone{}, false
}

func smcTrimBook(b *smcBook) {
	if len(b.trades) <= smcKeep {
		return
	}
	// 保留所有 open + 最近的 closed,總數壓到 smcKeep
	var open, closedTr []*PaperTrade
	for _, tr := range b.trades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closedTr = append(closedTr, tr)
		}
	}
	keep := smcKeep - len(open)
	if keep < 0 {
		keep = 0
	}
	if len(closedTr) > keep {
		closedTr = closedTr[len(closedTr)-keep:]
	}
	b.trades = append(open, closedTr...)
}

// SMCMarkTick 用 WS 現價即時檢查已開單的止盈止損(進場仍為 15M 收 K)。
// 出場邏輯與 smcRun 的收盤 backstop 一致:SL 優先於 TP。
func (s *Store) SMCMarkTick() {
	px := s.livePrices()
	if len(px) == 0 {
		return
	}
	now := time.Now()
	b := s.smcBook
	b.mu.Lock()
	var toNotify []*PaperTrade
	for _, tr := range b.trades {
		if tr.Status != "open" {
			continue
		}
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		if p <= tr.SL {
			closeTrade(tr, tr.SL, "sl", now)
			toNotify = append(toNotify, tr)
			delete(b.states, tr.Coin)
		} else if p >= tr.TP {
			closeTrade(tr, tr.TP, "tp", now)
			toNotify = append(toNotify, tr)
			delete(b.states, tr.Coin)
		} else {
			tr.Cur = roundPx(p)
			tr.PnLPct = round2(pnl(tr.Dir, tr.Entry, p))
		}
	}
	b.mu.Unlock()
	for _, tr := range toNotify {
		if s.db != nil {
			s.db.upsertTrade("smc", tr)
		}
		s.notifyCloseBook("smc", tr, now, false)
	}
}

// SMCState 回傳 open/closed/stats(前端分頁用)。
func (s *Store) SMCState() PaperState {
	px := s.livePrices()
	b := s.smcBook
	b.mu.Lock()
	defer b.mu.Unlock()
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	var sum, grossWin, grossLoss float64
	for _, tr := range b.trades {
		if tr.Status == "open" {
			st.Open = append(st.Open, tr)
			continue
		}
		st.Closed = append(st.Closed, tr)
		st.Stats.Closed++
		sum += tr.PnLPct
		if tr.PnLPct > 0 {
			st.Stats.Wins++
		} else {
			st.Stats.Losses++
		}
	}
	markLiveOpen(st.Open, px)
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].OpenTime.After(st.Open[j].OpenTime) })
	sort.Slice(st.Closed, func(i, j int) bool {
		return st.Closed[i].CloseTime != nil && st.Closed[j].CloseTime != nil && st.Closed[i].CloseTime.After(*st.Closed[j].CloseTime)
	})
	if st.Stats.Closed > 0 {
		st.Stats.WinRate = round2(float64(st.Stats.Wins) / float64(st.Stats.Closed) * 100)
		st.Stats.AvgPnl = round2(sum / float64(st.Stats.Closed))
		st.Stats.TotalPnl = round2(sum)
		if grossLoss > 0 {
			st.Stats.ProfitFactor = round2(grossWin / grossLoss)
		} else if grossWin > 0 {
			st.Stats.ProfitFactor = 99.99
		}
	}
	return st
}
