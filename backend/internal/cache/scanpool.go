package cache

import (
	"fmt"
	"math"
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// scanpool.go: the "30幣掃描池 · 1H 高勝率正期望" strategy (admin, display-only).
//
//	進場: EMA50 上穿 EMA200(金叉)且 收盤 > EMA800(1H) → 開倉(進場≈訊號K收盤/次根開盤)
//	出場: 持倉最高收盤 −8×ATR 吊燈停損,或 EMA50 下穿 EMA200
//	早鎖利: 浮盈達 +2×ATR 後,止損下限上移至 進場+0.5×ATR(之後吊燈續跟蹤)
//
// LONG-only, evaluated once per CLOSED 1H bar. Needs EMA800 (~800h history) which
// the WS feed can't hold, so it REST-fetches 900 1h bars per coin once per hour.
// Universe = top-30 by volume (subset of the 銀河 pool), to bound the REST load.

const (
	poolTopN       = 30  // scan-pool size (top-N by volume)
	poolKlimit     = 900 // 1h bars fetched (enough for EMA800 warmup)
	poolMinBars    = 820
	poolKeepClosed = 500
)

// poolCoins returns the top-N-by-volume scan pool (a slice of the 銀河 universe).
func (s *Store) poolCoins() []string {
	c := s.emaCoins()
	if len(c) > poolTopN {
		c = c[:poolTopN]
	}
	return c
}

// ---- indicators (local: atrSeries + peak/entry helpers) ----

func atrSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p+1 {
		return out
	}
	tr := func(i int) float64 {
		v := cs[i].High - cs[i].Low
		if d := math.Abs(cs[i].High - cs[i-1].Close); d > v {
			v = d
		}
		if d := math.Abs(cs[i].Low - cs[i-1].Close); d > v {
			v = d
		}
		return v
	}
	var sum float64
	for i := 1; i <= p; i++ {
		sum += tr(i)
	}
	atr := sum / float64(p)
	out[p] = atr
	for i := p + 1; i < n; i++ {
		atr = (atr*float64(p-1) + tr(i)) / float64(p)
		out[i] = atr
	}
	return out
}

// entryIdx is the index of the last bar whose Ts <= openMs (the entry bar).
func entryIdx(cs []exchange.Candle, openMs int64) int {
	idx := 0
	for i, c := range cs {
		if c.Ts <= openMs {
			idx = i
		} else {
			break
		}
	}
	return idx
}

// peakHighSince returns the highest High from the entry bar (inclusive) to the end.
func peakHighSince(cs []exchange.Candle, openMs int64) float64 {
	m := 0.0
	for i := entryIdx(cs, openMs); i < len(cs); i++ {
		if cs[i].High > m {
			m = cs[i].High
		}
	}
	return m
}

// ---- strategy signals ----

func poolEnter(cs []exchange.Candle) bool {
	n := len(cs)
	e50, e200, e800 := emaSeries(cs, 50), emaSeries(cs, 200), emaSeries(cs, 800)
	return e50[n-2] <= e200[n-2] && e50[n-1] > e200[n-1] && cs[n-1].Close > e800[n-1]
}

// poolStop returns the current 8×ATR chandelier / 早鎖利 stop level and its outcome
// label. ok=false when ATR isn't ready. Stored on the trade each 1H bar so the
// live tick can enforce it intrabar.
func poolStop(cs []exchange.Candle, tr *PaperTrade) (stop float64, outcome string, ok bool) {
	n := len(cs)
	atr := atrSeries(cs, 22)
	atrNow := atr[n-1]
	if atrNow <= 0 {
		return 0, "", false
	}
	peak := peakHighSince(cs, tr.OpenTime.UnixMilli())
	stop = peak - 8*atrNow // 8×ATR chandelier
	outcome = "chandelier"
	if ea := atr[entryIdx(cs, tr.OpenTime.UnixMilli())]; ea > 0 && peak-tr.Entry >= 2*ea {
		if floor := tr.Entry + 0.5*ea; floor > stop { // 早鎖利: 止損下限上移至 進場+0.5×ATR
			stop, outcome = floor, "lock"
		}
	}
	return stop, outcome, true
}

// poolExit returns (exit, outcome). outcome: signal | chandelier | lock. The
// death-cross (regime) exit needs a closed bar; the stop is also enforced live.
func poolExit(cs []exchange.Candle, tr *PaperTrade) (bool, string) {
	n := len(cs)
	if emaSeries(cs, 50)[n-1] < emaSeries(cs, 200)[n-1] {
		return true, "signal"
	}
	stop, outcome, ok := poolStop(cs, tr)
	if !ok {
		return false, ""
	}
	if cs[n-1].Close < stop {
		return true, outcome
	}
	return false, ""
}

// PoolTick evaluates the scan-pool strategy once per newly closed 1H bar.
func (s *Store) PoolTick() {
	now := time.Now().UTC()
	b1 := now.Unix() / 3600
	if b1 == s.poolBucket {
		return
	}
	s.poolBucket = b1
	if !s.poolSeeded { // boot: set the baseline only; don't backfill entries from the pre-startup bar
		s.poolSeeded = true
		return
	}
	for _, coin := range s.poolCoins() {
		cs, err := s.ex.BinanceKlines(coin+"USDT", "1h", poolKlimit)
		if err != nil || len(cs) < 2 {
			continue
		}
		cs = cs[:len(cs)-1] // drop the still-forming bar
		if len(cs) < poolMinBars {
			continue
		}
		s.runPool(coin, cs, now)
		time.Sleep(30 * time.Millisecond) // pace the REST batch
	}
}

func (s *Store) runPool(coin string, cs []exchange.Candle, now time.Time) {
	price := cs[len(cs)-1].Close
	s.poolMu.Lock()
	var open *PaperTrade
	for _, tr := range s.poolTrades {
		if tr.Coin == coin && tr.Status == "open" {
			open = tr
			break
		}
	}
	var dirty *PaperTrade
	if open != nil {
		open.Cur = roundPx(price)
		open.PnLPct = round2(pnl("long", open.Entry, price))
		if stop, _, ok := poolStop(cs, open); ok {
			open.SL = roundPx(stop) // persist the live stop so PoolMarkTick can enforce it intrabar
		}
		if ex, outcome := poolExit(cs, open); ex {
			open.Status = "closed"
			open.Outcome = outcome
			open.PnLPct = round2(pnl("long", open.Entry, price))
			t := now
			open.CloseTime = &t
		}
		dirty = open
	} else if poolEnter(cs) && s.StrategyEnabled("pool") {
		tr := &PaperTrade{
			ID:       fmt.Sprintf("pool|%s|%d", coin, now.UnixMilli()),
			Coin:     coin,
			Dir:      "long",
			Entry:    roundPx(price),
			Cur:      roundPx(price),
			Status:   "open",
			OpenTime: time.UnixMilli(cs[len(cs)-1].Ts).UTC(), // entry bar open time → entryIdx anchor
		}
		if stop, _, ok := poolStop(cs, tr); ok {
			tr.SL = roundPx(stop) // enforce the stop live from the entry bar, not only next bar
		}
		s.poolTrades = append(s.poolTrades, tr)
		dirty = tr
		s.poolTrim()
	}
	s.poolMu.Unlock()
	if dirty != nil && s.db != nil {
		s.db.upsertTrade("pool", dirty)
	}
}

func (s *Store) poolTrim() {
	var open, closed []*PaperTrade
	for _, tr := range s.poolTrades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closed = append(closed, tr)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime.After(*closed[j].CloseTime) })
	if len(closed) > poolKeepClosed {
		closed = closed[:poolKeepClosed]
	}
	s.poolTrades = append(open, closed...)
}

// PoolMarkTick marks open positions to the live WS price and exits any that break
// the stored chandelier / 早鎖利 stop intrabar. The death-cross exit still waits
// for the 1H bar close in PoolTick; entries stay on closed 1H bars.
func (s *Store) PoolMarkTick() {
	px := s.livePrices()
	if len(px) == 0 {
		return
	}
	now := time.Now()
	var dirty []*PaperTrade
	s.poolMu.Lock()
	for _, tr := range s.poolTrades {
		if tr.Status != "open" || tr.SL <= 0 {
			continue // SL is set on the first 1H management bar after entry
		}
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		tr.Cur = roundPx(p)
		tr.PnLPct = round2(pnl("long", tr.Entry, p))
		if p < tr.SL {
			outcome := "chandelier"
			if tr.SL >= tr.Entry {
				outcome = "lock" // stop已升到進場之上 → 早鎖利
			}
			tr.Status = "closed"
			tr.Outcome = outcome
			tr.Cur = roundPx(tr.SL)
			tr.PnLPct = round2(pnl("long", tr.Entry, tr.SL))
			t := now
			tr.CloseTime = &t
			dirty = append(dirty, tr)
		}
	}
	s.poolMu.Unlock()
	if s.db != nil {
		for _, tr := range dirty {
			s.db.upsertTrade("pool", tr)
		}
	}
}

// PoolState returns the scan-pool strategy's simulated open/closed/stats.
func (s *Store) PoolState() PaperState {
	px := s.livePrices() // read before the lock; open positions get live 現價 (strategy only ticks per 1H bar)
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	var sum float64
	for _, tr := range s.poolTrades {
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
	markLiveOpen(st.Open, px) // display-only: live 現價 between 1H bars
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].OpenTime.After(st.Open[j].OpenTime) })
	sort.Slice(st.Closed, func(i, j int) bool {
		return st.Closed[i].CloseTime != nil && st.Closed[j].CloseTime != nil && st.Closed[i].CloseTime.After(*st.Closed[j].CloseTime)
	})
	if st.Stats.Closed > 0 {
		st.Stats.WinRate = round2(float64(st.Stats.Wins) / float64(st.Stats.Closed) * 100)
		st.Stats.AvgPnl = round2(sum / float64(st.Stats.Closed))
		st.Stats.TotalPnl = round2(sum)
	}
	return st
}
