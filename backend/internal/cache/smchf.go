package cache

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// smchf.go — 獵戶座(SMC 共振・高頻檔,市價觸發版)。規格:smcconf_hf_deploy.md v1.0(2026-10-29)。
//
// 每根 1h 收盤(UTC 整點)檢查結構:
//
//	樞紐        某根高 / 低點是前後各 5 根的極值 → 5 根後確認(SWING_K=5)
//	BOS         收盤上穿最近確認的擺動高(做多)/ 下穿擺動低(做空)
//	訂單塊 OB   往回 12 根內最近一根「反向」K(做多找陰線、做空找陽線)
//	計畫        進場 = OB 中點;SL = OB 下緣 − 0.15×ATR14(空:上緣 + 0.15×ATR);TP1 = 1R、TP2 = 1.5R
//	幣池        成交額前 200 的正規加密幣(規格書原本是 top80,依需求放大)
//	預檢        前極值(OB 之後的最高 / 最低)離進場 ≥ 1.5R;共振至少 1 項:
//	            FVG 與 OB 重疊 / OB 中點離 1h EMA50 ≤ 0.5 ATR / 4h、1d EMA20 斜率都跟方向一致(只看已收盤)
//
// 計畫建立後凍結(之後的收盤不重算),掛 10 根 1h 沒觸發就撤。市價觸發:每 10 秒看即時價,價格回到
// OB 中點就以當下價格進場;離中點超過 0.08%(跳過去了)不進、計畫繼續等;先碰到 SL 就撤計畫。
// 成交後用實際成交價重算 R:TP1 = ±1R 平 50%、TP2 = ±1.5R 平剩下;SL 永不移動(TP1 後不保本)。
// 每幣同時一單(計畫或持倉),全部持倉最多 8 筆。損益顯示「真實分批加權」(不用最高止盈階梯口徑)。
// 觀察用:observeOnlyBooks 保證不實單;待觸發的計畫只在記憶體(重啟就消失),成交後才寫 DB。
const (
	shfTF         = "1h"
	shfBarMs      = int64(time.Hour / time.Millisecond)
	shfKlines     = 200
	shfSwingK     = 5
	shfOBLookback = 12
	shfSLATR      = 0.15
	shfTP1R       = 1.0
	shfTP2R       = 1.5
	shfMinRR      = 1.5
	shfEMA50Near  = 0.5 // OB 中點離 EMA50 幾個 ATR 內算共振
	shfPlanBars   = 10  // 掛單壽命(根)
	shfMaxDev     = 0.0008
	shfMaxOpen    = 8
	shfPace       = 60 * time.Millisecond
)

// realPnLBooks:損益用「真實分批加權」(Realized + 剩餘部位浮動)顯示的書,而不是最高止盈階梯。
var realPnLBooks = map[string]bool{"smchf": true}

// realPnL 回傳真實分批加權損益 %(已實現 + 剩餘倉位以 price 計)。
func realPnL(tr *PaperTrade, price float64) float64 {
	return round2(tr.Realized + (1-tr.Filled)*pnl(tr.Dir, tr.Entry, price))
}

type smcHFEngine struct {
	mu      sync.Mutex
	lastBar map[string]int64 // coin → 已處理過的最新 1h 收盤棒(開盤 ms)
	seeded  bool
}

func newSMCHFBook() *microBook {
	return &microBook{name: "smchf", tf: shfTF, barSec: 3600, keep: 500}
}

// smcHFUniverse = 成交額前 200 的幣(與 EMA200 監控 / 爆發雷達同一批,已排除穩定幣)中的正規加密幣
// (再排除代幣化股票 / 商品)。剛開機爆量脈搏還沒掃到行情時,先用銀河 top-80 頂著。
func (s *Store) smcHFUniverse() []string {
	base := s.e2Universe()
	if len(base) == 0 {
		base = s.emaCoins()
	}
	var out []string
	for _, c := range base {
		if s.cryptoOnly(c, nil) {
			out = append(out, c)
		}
	}
	return out
}

// smcHFPlan 是一筆凍結的掛單計畫。
type smcHFPlan struct {
	Dir             string
	Entry, SL       float64 // OB 中點、停損
	ZoneTop, ZoneBt float64
}

// smcHFBuild 用已收盤的 1h K(最後一根 = 剛收的那根)判斷是否成立 BOS 並建立計畫。
// htfOK(dir) 回報 4h / 1d EMA20 斜率是否都跟方向一致(只有其他共振都不成立時才會呼叫,省請求)。
func smcHFBuild(cs []exchange.Candle, htfOK func(dir string) bool) (smcHFPlan, bool) {
	n := len(cs)
	if n < 60 {
		return smcHFPlan{}, false
	}
	t := n - 1
	// 最近一個「在 t-1 收盤前已確認」的擺動高 / 低(樞紐 i 在 i+5 收盤確認)
	swHi, swLo := math.NaN(), math.NaN()
	for i := shfSwingK; i+shfSwingK <= t-1; i++ {
		hi, lo := true, true
		for j := i - shfSwingK; j <= i+shfSwingK; j++ {
			if j == i {
				continue
			}
			if cs[j].High >= cs[i].High {
				hi = false
			}
			if cs[j].Low <= cs[i].Low {
				lo = false
			}
		}
		if hi {
			swHi = cs[i].High
		}
		if lo {
			swLo = cs[i].Low
		}
	}
	dir := ""
	switch {
	case !math.IsNaN(swHi) && cs[t].Close > swHi && cs[t-1].Close <= swHi:
		dir = "long"
	case !math.IsNaN(swLo) && cs[t].Close < swLo && cs[t-1].Close >= swLo:
		dir = "short"
	default:
		return smcHFPlan{}, false
	}
	// OB:往回 12 根內最近一根反向 K
	ob := -1
	for i := t; i > t-shfOBLookback && i >= 0; i-- {
		if (dir == "long" && cs[i].Close < cs[i].Open) || (dir == "short" && cs[i].Close > cs[i].Open) {
			ob = i
			break
		}
	}
	if ob < 0 {
		return smcHFPlan{}, false
	}
	atr := atrSeries(cs, 14)[t]
	if atr <= 0 {
		return smcHFPlan{}, false
	}
	top, bot := cs[ob].High, cs[ob].Low
	mid := (top + bot) / 2
	p := smcHFPlan{Dir: dir, Entry: mid, ZoneTop: top, ZoneBt: bot}
	ext := 0.0 // OB 之後到現在的前極值
	if dir == "long" {
		p.SL = bot - shfSLATR*atr
		for i := ob; i <= t; i++ {
			ext = math.Max(ext, cs[i].High)
		}
		if mid >= cs[t].Close || p.SL >= mid || (ext-mid)/(mid-p.SL) < shfMinRR {
			return smcHFPlan{}, false
		}
	} else {
		p.SL = top + shfSLATR*atr
		ext = math.Inf(1)
		for i := ob; i <= t; i++ {
			ext = math.Min(ext, cs[i].Low)
		}
		if mid <= cs[t].Close || p.SL <= mid || (mid-ext)/(p.SL-mid) < shfMinRR {
			return smcHFPlan{}, false
		}
	}
	if smcHFFVG(cs, ob, t, dir, top, bot) {
		return p, true
	}
	if math.Abs(mid-emaSeries(cs, 50)[t]) <= shfEMA50Near*atr {
		return p, true
	}
	if htfOK != nil && htfOK(dir) {
		return p, true
	}
	return smcHFPlan{}, false
}

// smcHFFVG:OB 到現在之間有沒有跟 OB 區間重疊、方向一致的 FVG(三根缺口)。
func smcHFFVG(cs []exchange.Candle, ob, t int, dir string, top, bot float64) bool {
	for j := max(ob, 2); j <= t; j++ {
		var lo, hi float64
		if dir == "long" { // 多方缺口:第 j 根低點 > 第 j-2 根高點
			if cs[j].Low <= cs[j-2].High {
				continue
			}
			lo, hi = cs[j-2].High, cs[j].Low
		} else {
			if cs[j].High >= cs[j-2].Low {
				continue
			}
			lo, hi = cs[j].High, cs[j-2].Low
		}
		if lo <= top && hi >= bot {
			return true
		}
	}
	return false
}

// smcHFSlope:已收盤的 EMA20 斜率 > 0(多)/ < 0(空)。
func smcHFSlope(cs []exchange.Candle, dir string) bool {
	if len(cs) < 25 {
		return false
	}
	e := emaSeries(cs, 20)
	d := e[len(e)-1] - e[len(e)-2]
	return (dir == "long" && d > 0) || (dir == "short" && d < 0)
}

// smcHFHTF 抓 4h、1d K(丟掉還在走的那根)看 EMA20 斜率。
func (s *Store) smcHFHTF(coin, dir string) bool {
	for _, tf := range []string{"4h", "1d"} {
		v, err := s.candleCache.get(fmt.Sprintf("%s|%s|40", coin, tf), func() (any, error) {
			return s.ex.BinanceKlines(coin+"USDT", tf, 40)
		})
		if err != nil {
			return false
		}
		cs := v.([]exchange.Candle)
		if len(cs) < 2 || !smcHFSlope(cs[:len(cs)-1], dir) {
			return false
		}
	}
	return true
}

// SMCHFScanTick 每分鐘:每個幣的新 1h 收盤棒各處理一次,BOS + 預檢 + 共振通過就建立待觸發計畫。
func (s *Store) SMCHFScanTick() {
	b, e := s.smcHFBook, s.smcHF
	if b == nil || e == nil {
		return
	}
	lastBar := (time.Now().UnixMilli()/shfBarMs - 1) * shfBarMs // 最新已收 1h 的開盤時戳
	enabled := s.StrategyEnabled(b.strat())
	added := 0
	for _, coin := range s.smcHFUniverse() {
		e.mu.Lock()
		done := e.lastBar[coin] == lastBar
		e.mu.Unlock()
		if done {
			continue
		}
		cs, err := s.ex.BinanceKlines(coin+"USDT", shfTF, shfKlines)
		time.Sleep(shfPace)
		if err != nil || len(cs) < 2 {
			continue
		}
		cs = cs[:len(cs)-1] // 丟掉還在走的那根
		if cs[len(cs)-1].Ts != lastBar {
			continue // 交易所新棒還沒生成 → 下一輪再算
		}
		e.mu.Lock()
		e.lastBar[coin] = lastBar
		seeded := e.seeded
		e.mu.Unlock()
		if !seeded || !enabled {
			continue // 開機第一輪只建基準
		}
		p, ok := smcHFBuild(cs, func(dir string) bool { return s.smcHFHTF(coin, dir) })
		if !ok {
			continue
		}
		now := time.Now().UTC()
		b.mu.Lock()
		busy := false
		for _, tr := range b.trades {
			if tr.Coin == coin && (tr.Status == "open" || tr.Status == "pending") {
				busy = true
				break
			}
		}
		var plan *PaperTrade
		if !busy {
			plan = &PaperTrade{ID: fmt.Sprintf("smchf|%s|%d", coin, now.UnixMilli()), Coin: coin, Dir: p.Dir,
				Entry: roundPx(p.Entry), SL: roundPx(p.SL), Cur: cs[len(cs)-1].Close, Status: "pending",
				OpenTime: time.UnixMilli(lastBar + shfBarMs).UTC()}
			b.trades = append(b.trades, plan)
			added++
		}
		b.mu.Unlock()
		if plan != nil && s.db != nil {
			s.db.upsertTrade(b.name, plan) // 待觸發計畫也存 DB,重啟後照樣等觸發
		}
	}
	e.mu.Lock()
	e.seeded = true
	e.mu.Unlock()
	if added > 0 {
		log.Printf("策略[smchf] 新增待觸發計畫 %d 筆", added)
	}
}

// SMCHFTriggerTick 每 10 秒:用即時價觸發計畫、管理持倉(TP1 / TP2 / SL)、撤掉過期或先碰 SL 的計畫。
func (s *Store) SMCHFTriggerTick() {
	b := s.smcHFBook
	if b == nil {
		return
	}
	b.mu.Lock()
	has := hasLive(b.trades)
	b.mu.Unlock()
	if !has {
		return
	}
	// 全市場即時價(一次請求,權重 2):不限 WS 有訂閱的幣,宇宙裡每個幣都有價
	all, err := s.ex.BinanceAllPrices()
	if err != nil {
		return
	}
	px := make(map[string]float64, len(all))
	for sym, p := range all {
		px[coinOf(sym)] = p
	}
	now := time.Now().UTC()
	b.mu.Lock()
	opened, tp1, closed, dropped := smcHFProcess(b, px, now)
	b.mu.Unlock()
	if s.db != nil {
		for _, tr := range dropped {
			s.db.deleteTrade(tr.ID) // 撤掉的計畫不留紀錄
		}
	}
	for _, tr := range opened {
		if s.db != nil {
			s.db.fillTrade(tr) // 成交價 / 時間 / TP 跟計畫不同,upsert 不會改這幾欄
			s.db.upsertTrade(b.name, tr)
		}
		s.notifyOpenBook(b.name, tr)
	}
	for _, tr := range tp1 {
		if s.db != nil {
			s.db.upsertTrade(b.name, tr)
		}
		if s.pushMgr != nil && s.notifyOn(b.name, "tp") { // 這本 TP1 後止損不動(跟其他書的「移保本」不同)
			s.PushSendTopic(stratTopic(b.name), "🎯 獵戶座 TP1 達成",
				fmt.Sprintf("%s %s @ $%s · 已平 50%% · 止損不動", tr.Coin, dirCN(tr.Dir), fmtPx(tr.TP1)), "/?tab="+bookTab(b.name))
		}
	}
	for _, tr := range closed {
		if s.db != nil {
			s.db.upsertTrade(b.name, tr)
		}
		s.notifyCloseBook(b.name, tr, now, false)
	}
}

// smcHFProcess 是觸發 / 持倉管理的核心(呼叫端持 b.mu):過期或先碰 SL 的計畫撤掉、碰到中點的計畫成交、
// 持倉走 TP1 / TP2 / SL。回傳這輪成交、TP1 達成、全部平倉、撤掉的計畫(給呼叫端寫 DB / 推播)。
func smcHFProcess(b *microBook, px map[string]float64, now time.Time) (opened, tp1, closed, dropped []*PaperTrade) {
	nOpen := 0
	for _, tr := range b.trades {
		if tr.Status == "open" {
			nOpen++
		}
	}
	kept := b.trades[:0]
	for _, tr := range b.trades {
		p := px[tr.Coin]
		switch tr.Status {
		case "pending":
			if now.Sub(tr.OpenTime) >= shfPlanBars*time.Hour || (p > 0 && smcHFHitSL(tr.Dir, p, tr.SL)) {
				dropped = append(dropped, tr)
				continue // 過期 / 先碰 SL → 撤計畫(不留紀錄)
			}
			if p > 0 {
				tr.Cur = roundPx(p)
				if nOpen < shfMaxOpen && smcHFTouch(tr.Dir, p, tr.Entry) {
					smcHFFill(tr, p, now)
					nOpen++
					opened = append(opened, tr)
				}
			}
		case "open":
			if p > 0 {
				legs := tr.Legs
				if smcHFStep(tr, p, now) {
					closed = append(closed, tr)
				} else if tr.Legs > legs {
					tp1 = append(tp1, tr)
				}
			}
		}
		kept = append(kept, tr)
	}
	b.trades = kept
	microTrim(b)
	return opened, tp1, closed, dropped
}

func hasLive(trs []*PaperTrade) bool {
	for _, tr := range trs {
		if tr.Status != "closed" {
			return true
		}
	}
	return false
}

func smcHFHitSL(dir string, p, sl float64) bool {
	if dir == "long" {
		return p <= sl
	}
	return p >= sl
}

// smcHFTouch:價格回到 OB 中點,且離中點不超過 0.08%(跳過去太遠就不進,計畫繼續等)。
func smcHFTouch(dir string, p, mid float64) bool {
	if dir == "long" {
		return p <= mid && p >= mid*(1-shfMaxDev)
	}
	return p >= mid && p <= mid*(1+shfMaxDev)
}

// smcHFFill 以實際成交價進場,用成交價重算 R 與 TP1 / TP2(SL 不變)。
func smcHFFill(tr *PaperTrade, p float64, now time.Time) {
	tr.Status, tr.Entry, tr.Cur, tr.OpenTime = "open", roundPx(p), roundPx(p), now
	r := math.Abs(p - tr.SL)
	if tr.Dir == "long" {
		tr.TP1, tr.TP = roundPx(p+shfTP1R*r), roundPx(p+shfTP2R*r)
	} else {
		tr.TP1, tr.TP = roundPx(p-shfTP1R*r), roundPx(p-shfTP2R*r)
	}
	tr.PnLPct = 0
}

// smcHFStep 管理持倉:TP1 平 50%(止損不動)、TP2 平剩下、SL 全出。回傳是否已全部平倉。
func smcHFStep(tr *PaperTrade, p float64, now time.Time) bool {
	if g := pnl(tr.Dir, tr.Entry, p); g > tr.MaxGain {
		tr.MaxGain = round2(g)
	}
	long := tr.Dir == "long"
	reached := func(lv float64) bool { return lv > 0 && ((long && p >= lv) || (!long && p <= lv)) }
	if smcHFHitSL(tr.Dir, p, tr.SL) {
		out := "sl"
		if tr.Legs >= 1 {
			out = "tp1stop" // TP1 已平一半,剩下的打到原止損
		}
		smcHFClose(tr, tr.SL, out, now)
		return true
	}
	if tr.Legs < 1 && reached(tr.TP1) {
		tr.Realized += 0.5 * pnl(tr.Dir, tr.Entry, tr.TP1)
		tr.Filled, tr.Legs = 0.5, 1
	}
	if reached(tr.TP) {
		tr.Legs = 2
		smcHFClose(tr, tr.TP, "tp2", now)
		return true
	}
	tr.Cur = roundPx(p)
	tr.PnLPct = realPnL(tr, p)
	return false
}

func smcHFClose(tr *PaperTrade, exit float64, outcome string, now time.Time) {
	tr.Realized += (1 - tr.Filled) * pnl(tr.Dir, tr.Entry, exit)
	tr.Filled = 1
	tr.Status, tr.Outcome, tr.Cur = "closed", outcome, roundPx(exit)
	tr.PnLPct = round2(tr.Realized)
	t := now
	tr.CloseTime = &t
}
