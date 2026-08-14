package cache

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// smcv2.go — SMC 共振回撤策略 (SMC_V2)，市價觸發版 v1.1（定版規格 2026-08-07）。
//
// 流程:1h 收盤偵測 BOS(結構突破)→ 佈防價格觸發器(回踩 OB 邊緣)→ 現價觸及觸發器
// 即市價進場 → TP1 出 50% @ +1R(同時把止損移到進場價保本)/ TP2 出剩餘 @ 被突破的
// 擺動極值。多空對稱。全帳戶同時持倉上限 8。
// ⚠️ 「TP1 後移保本」與 v1.1 規格的「止損永不移動」不同(依你要求改成保本棘輪)。
//
// 持久化沿用 trades 表(book="smcv2"):Status="pending" = 已佈防的觸發器(存活 10h,
// 重啟續用);"open" = 已成交持倉;"closed" = 已出場/逾時。偵測在 1h 收盤 tick(SMCV2Tick),
// 觸發與出場在現價 mark-tick(SMCV2MarkTick)。樞紐/FVG/HTF 每次收盤由 K 線重算,不另存。
const (
	smcv2TF         = "1h"
	smcv2Klimit     = 320
	smcv2MinBars    = 300
	smcv2SwingK     = 5
	smcv2OBLookback = 12
	smcv2SLBufATR   = 0.15
	smcv2EMAConf    = 50
	smcv2EMATolATR  = 0.5
	smcv2HTFEMALen  = 20
	smcv2MinConf    = 1
	smcv2MinRR      = 1.5
	smcv2TrigLifeH  = 10 // 觸發器壽命(小時)
	smcv2TP1R       = 1.0
	smcv2TP1Pct     = 0.5
	smcv2MaxConc    = 8
)

type smcv2Book struct {
	mu     sync.Mutex
	trades []*PaperTrade
	bucket int64 // 上次處理的 1h 桶(避免同一根棒處理兩次)
	seeded bool  // 開機第一次只建基準,不回補歷史
}

func newSMCV2Book() *smcv2Book { return &smcv2Book{} }

// smcv2LatestFVG 回傳 K 線裡最近一組 FVG。bull:Low[i] > High[i-2] → top=Low[i]、
// bot=High[i-2]。bear 鏡像:High[i] < Low[i-2] → top=Low[i-2]、bot=High[i]。
func smcv2LatestFVG(cs []exchange.Candle, bull bool) (top, bot float64, ok bool) {
	for i := len(cs) - 1; i >= 2; i-- {
		if bull {
			if cs[i].Low > cs[i-2].High {
				return cs[i].Low, cs[i-2].High, true
			}
		} else if cs[i].High < cs[i-2].Low {
			return cs[i-2].Low, cs[i].High, true
		}
	}
	return 0, 0, false
}

// smcv2OB:從最後一根往回 OB_LOOKBACK 根內,找最近一根反向 K。long → 陰線(收<開),
// 回傳其 High=zoneTop、Low=zoneBottom;short → 陽線。
func smcv2OB(cs []exchange.Candle, long bool) (top, bot float64, ok bool) {
	n := len(cs)
	lo := n - smcv2OBLookback
	if lo < 0 {
		lo = 0
	}
	for i := n - 1; i >= lo; i-- {
		bear := cs[i].Close < cs[i].Open
		bull := cs[i].Close > cs[i].Open
		if (long && bear) || (!long && bull) {
			return cs[i].High, cs[i].Low, true
		}
	}
	return 0, 0, false
}

// smcv2HTFTrend:高時框 EMA20 單棒斜率(只用已收盤棒)。+1 上升 / -1 下降 / 0 未定。
func (s *Store) smcv2HTFTrend(coin, tf string) int {
	cs, err := s.ex.BinanceKlines(coin+"USDT", tf, 60)
	if err != nil || len(cs) < smcv2HTFEMALen+3 {
		return 0
	}
	cs = cs[:len(cs)-1] // 去掉未收盤的當前棒
	e := emaSeries(cs, smcv2HTFEMALen)
	n := len(e)
	if n < 2 {
		return 0
	}
	switch {
	case e[n-1] > e[n-2]:
		return 1
	case e[n-1] < e[n-2]:
		return -1
	}
	return 0
}

// SMCV2Tick:每小時整點收盤後,對每幣偵測一次 BOS 並佈防觸發器。
func (s *Store) SMCV2Tick() {
	b := s.smcv2Book
	bkt := time.Now().UTC().Unix() / 3600
	b.mu.Lock()
	if bkt == b.bucket {
		b.mu.Unlock()
		return
	}
	b.bucket = bkt
	seeded := b.seeded
	b.seeded = true
	b.mu.Unlock()
	if !seeded { // 開機只建基準,不用第一根就開始佈防
		return
	}
	if !s.StrategyEnabled("smcv2") {
		return
	}
	now := time.Now().UTC()
	for _, coin := range s.emaCoins() {
		cs, err := s.ex.BinanceKlines(coin+"USDT", smcv2TF, smcv2Klimit)
		if err != nil || len(cs) < 2 {
			continue
		}
		cs = cs[:len(cs)-1] // 去掉未收盤的當前棒
		if len(cs) < smcv2MinBars {
			continue // 新上市幣資料不足
		}
		s.smcv2Detect(b, coin, cs, now)
		time.Sleep(25 * time.Millisecond) // pace REST
	}
}

// smcv2Detect:單幣一根 1h 收盤的偵測 + 佈防。
func (s *Store) smcv2Detect(b *smcv2Book, coin string, cs []exchange.Candle, now time.Time) {
	n := len(cs)
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 {
		return
	}
	ema50 := emaSeries(cs, smcv2EMAConf)[n-1]
	highIdx := pivotHighIdx(cs, smcv2SwingK)
	lowIdx := pivotLowIdx(cs, smcv2SwingK)
	if len(highIdx) == 0 && len(lowIdx) == 0 {
		return
	}
	var swH, swL float64
	okH, okL := len(highIdx) > 0, len(lowIdx) > 0
	if okH {
		swH = cs[highIdx[len(highIdx)-1]].High
	}
	if okL {
		swL = cs[lowIdx[len(lowIdx)-1]].Low
	}
	close, prev := cs[n-1].Close, cs[n-2].Close

	// 每幣一個計畫:已有觸發器或持倉就跳過
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.Coin == coin && (tr.Status == "pending" || tr.Status == "open") {
			b.mu.Unlock()
			return
		}
	}
	b.mu.Unlock()

	bosUp := okH && close > swH && prev <= swH
	bosDn := okL && close < swL && prev >= swL
	if !bosUp && !bosDn {
		return
	}
	long := bosUp

	obTop, obBot, obOK := smcv2OB(cs, long)
	if !obOK {
		return
	}
	var trigger, sl, tp2 float64
	if long {
		trigger, sl, tp2 = obTop, obBot-smcv2SLBufATR*atr, swH
		if !(tp2 > trigger && trigger > sl) {
			return
		}
	} else {
		trigger, sl, tp2 = obBot, obTop+smcv2SLBufATR*atr, swL
		if !(tp2 < trigger && trigger < sl) {
			return
		}
	}
	if rr := math.Abs(tp2-trigger) / math.Abs(trigger-sl); rr < smcv2MinRR {
		return
	}
	if minTP := s.stratMinTP("smcv2"); minTP > 0 { // 後台「最小止盈%」:止盈距離太近不佈防
		if math.Abs(tp2-trigger)/trigger*100 < minTP {
			return
		}
	}

	// 共振計分(≥1 才佈防)
	conf := 0
	if ft, fb, ok := smcv2LatestFVG(cs, long); ok && obBot <= ft && obTop >= fb {
		conf++ // FVG 與 OB 區間重疊
	}
	if mid := (obTop + obBot) / 2; math.Abs(mid-ema50) <= smcv2EMATolATR*atr {
		conf++ // OB 中點貼近 EMA50
	}
	want := 1
	if !long {
		want = -1
	}
	if s.smcv2HTFTrend(coin, "4h") == want && s.smcv2HTFTrend(coin, "1d") == want {
		conf++ // 4h 與 1d EMA20 斜率同向
	}
	if conf < smcv2MinConf {
		return
	}

	dir := "long"
	if !long {
		dir = "short"
	}
	tr := &PaperTrade{
		ID:       fmt.Sprintf("smcv2|%s|%s|%d", coin, dir, now.UnixMilli()),
		Coin:     coin,
		Dir:      dir,
		Entry:    roundPx(trigger), // pending 時為觸發價;成交後改為實際進場
		SL:       roundPx(sl),
		TP:       roundPx(tp2), // TP2(鎖定,不變)
		Cur:      roundPx(cs[n-1].Close),
		Status:   "pending",
		OpenTime: now, // 佈防時間(10h 壽命基準)
	}
	b.mu.Lock()
	b.trades = append(b.trades, tr)
	smcv2Trim(b)
	b.mu.Unlock()
	if s.db != nil {
		s.db.upsertTrade("smcv2", tr)
	}
}

// SMCV2MarkTick:現價驅動。pending 觸發器檢查成交/逾時;open 持倉走 TP1/TP2/SL。
func (s *Store) SMCV2MarkTick() {
	b := s.smcv2Book
	px := s.livePrices()
	if len(px) == 0 {
		return
	}
	now := time.Now()
	enabled := s.StrategyEnabled("smcv2")
	var dirty, opened, closed []*PaperTrade
	b.mu.Lock()
	openCount := 0
	for _, tr := range b.trades {
		if tr.Status == "open" {
			openCount++
		}
	}
	for _, tr := range b.trades {
		switch tr.Status {
		case "pending":
			if now.Sub(tr.OpenTime) >= smcv2TrigLifeH*time.Hour { // 逾時解除
				cn := now
				tr.Status, tr.Outcome, tr.CloseTime = "closed", "cancel", &cn // 觸發器未成交撤銷(非交易結果)
				dirty = append(dirty, tr)
				continue
			}
			p := px[tr.Coin]
			if p <= 0 || !enabled {
				continue
			}
			fire := (tr.Dir == "long" && p <= tr.Entry) || (tr.Dir == "short" && p >= tr.Entry)
			if !fire {
				continue
			}
			if openCount >= smcv2MaxConc { // 全帳戶持倉滿 8 → 忽略此觸發
				cn := now
				tr.Status, tr.Outcome, tr.CloseTime = "closed", "cancel", &cn // 觸發器未成交撤銷(非交易結果)
				dirty = append(dirty, tr)
				continue
			}
			risk := math.Abs(tr.Entry - tr.SL)
			if risk <= 0 {
				cn := now
				tr.Status, tr.Outcome, tr.CloseTime = "closed", "cancel", &cn // 觸發器未成交撤銷(非交易結果)
				dirty = append(dirty, tr)
				continue
			}
			if tr.Dir == "long" {
				tr.TP1 = roundPx(tr.Entry + smcv2TP1R*risk)
			} else {
				tr.TP1 = roundPx(tr.Entry - smcv2TP1R*risk)
			}
			tr.Status = "open"
			tr.OpenTime = now // 成交時間
			tr.Legs, tr.Filled, tr.Realized = 0, 0, 0
			tr.Cur = roundPx(p)
			openCount++
			dirty = append(dirty, tr)
			opened = append(opened, tr)
		case "open":
			p := px[tr.Coin]
			if p <= 0 {
				continue
			}
			before := tr.Legs
			if s.smcv2Step(tr, p, now) {
				closed = append(closed, tr)
			}
			if tr.Status == "closed" || tr.Legs != before {
				dirty = append(dirty, tr)
			}
		}
	}
	b.mu.Unlock()
	if s.db != nil {
		for _, tr := range dirty {
			s.db.upsertTrade("smcv2", tr)
		}
	}
	for _, tr := range opened {
		s.notifyOpenBook("smcv2", tr) // 通知(smcv2 的 Bitunix 鏡射走專屬兩段止盈路徑,見 notifyOpenBook)
		s.mirrorSMCV2(tr)
	}
	for _, tr := range closed {
		s.notifyCloseBook("smcv2", tr, now, false)
	}
}

// smcv2Step:兩段固定出場。SL 優先(保守);TP1 半出鎖利;TP2 全出。止損永不移動。
func (s *Store) smcv2Step(tr *PaperTrade, p float64, now time.Time) bool {
	long := tr.Dir == "long"
	if (long && p <= tr.SL) || (!long && p >= tr.SL) {
		closeTrade(tr, tr.SL, smcv2SLOutcome(tr), now)
		return true
	}
	if tr.Legs < 1 && ((long && p >= tr.TP1) || (!long && p <= tr.TP1)) {
		tr.Realized += smcv2TP1Pct * pnl(tr.Dir, tr.Entry, tr.TP1)
		tr.Filled += smcv2TP1Pct
		tr.Legs = 1
		tr.SL = roundPx(tr.Entry) // TP1 後把止損移到進場價(保本):剩餘 50% 不再變虧損
	}
	if (long && p >= tr.TP) || (!long && p <= tr.TP) {
		tr.Legs = 3
		closeTrade(tr, tr.TP, "tp3", now)
		return true
	}
	tr.Cur = roundPx(p)
	tr.PnLPct = round2(tr.Realized + (1-tr.Filled)*pnl(tr.Dir, tr.Entry, p))
	return false
}

// smcv2SLOutcome:摸過 TP1 才被止損 = tp1sl(半利半損);否則純 sl。
func smcv2SLOutcome(tr *PaperTrade) string {
	if tr.Legs >= 1 {
		return "tp1sl"
	}
	return "sl"
}

// smcv2Trim:保留所有 pending/open + 最近 500 筆已結束。
func smcv2Trim(b *smcv2Book) {
	const keep = 500
	var live, done []*PaperTrade
	for _, tr := range b.trades {
		if tr.Status == "pending" || tr.Status == "open" {
			live = append(live, tr)
		} else {
			done = append(done, tr)
		}
	}
	if len(done) > keep {
		done = done[len(done)-keep:]
	}
	b.trades = append(live, done...)
}

// SMCV2State 回傳前端分頁用的 open(含待觸發)/closed + 統計。未成交撤銷的觸發器
// (outcome="cancel")顯示在已結束、但不計入勝率/損益(它們不是交易)。
func (s *Store) SMCV2State() PaperState {
	px := s.livePrices()
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	st.Stats.MultiTP = true // TP1/TP2 分段
	var sum, grossWin, grossLoss float64
	b := s.smcv2Book
	b.mu.Lock()
	all := append([]*PaperTrade{}, b.trades...)
	b.mu.Unlock()
	for _, tr := range all {
		cp := *tr
		if p := px[tr.Coin]; p > 0 && (tr.Status == "open" || tr.Status == "pending") {
			cp.Cur = roundPx(p)
			if tr.Status == "open" {
				cp.PnLPct = round2(tr.Realized + (1-tr.Filled)*pnl(tr.Dir, tr.Entry, p))
			}
		}
		if tr.Status != "closed" {
			st.Open = append(st.Open, &cp)
			continue
		}
		st.Closed = append(st.Closed, &cp)
		if tr.Outcome == "cancel" { // 未成交觸發器,不計入績效
			continue
		}
		st.Stats.Closed++
		sum += tr.PnLPct
		if tr.PnLPct > 0 {
			st.Stats.Wins++
		} else {
			st.Stats.Losses++
		}
		tpStats(tr, &st.Stats.Tp1, &st.Stats.Tp2, &st.Stats.Tp3, &grossWin, &grossLoss)
	}
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
