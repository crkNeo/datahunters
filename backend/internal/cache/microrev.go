package cache

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// microrev.go: three admin-only mean-reversion strategies, evaluated once per
// closed bar over the 銀河 (emaCoins) universe — same shape as scanpool/convergence.
//
//	1. 逆勢超買空 (rsifade)  30m 只做空:RSI(3)>90 且收盤 < EMA200(空頭反彈)→ 放空。
//	                        止損 +2.5 ATR,目標 −2.0 ATR,最多 16 根,冷卻 4 根。
//	2. 布林重回 (bollfade)  1h 雙向:前一根收盤在布林(20,2σ)外、本根收回通道內(過度延伸
//	                        失敗)且方向與 EMA200 同側 → 朝中軌交易。止損 2.5 ATR,目標=中軌,
//	                        RR 需 0.4–3.0。
//	3. 乖離回歸 (meanrev)   1h 雙向:收盤偏離 EMA20 超過 2 ATR、且與 EMA200 同側(上方接多、
//	                        下方接空)→ 朝 EMA20 回歸。止損 3 ATR,目標=EMA20。
//
// All display-only (admin). Entry + TP/SL exit are both judged on the CLOSED bar;
// open positions are marked to the live WS price for display.

// microBook is one strategy's config + simulated trade state.
type microBook struct {
	name     string // db book name + trade-id prefix (rsifade|bollfade|meanrev)
	tf       string // "30m" | "1h"
	barSec   int64  // bar length in seconds (bucketing + expiry)
	klimit   int
	minBars  int
	expiry   int     // max hold in bars → market exit ("expired")
	cooldown int     // bars to wait after a close before re-entering the same coin
	keep     int     // closed-trade cap
	plan     *tpPlan // 分批止盈 config (nil = single TP)
	signal   func(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool)

	mu     sync.Mutex
	trades []*PaperTrade
	bucket int64 // last processed wall-clock bar bucket (single ticker goroutine)
	seeded bool  // first tick only sets the baseline bucket — no boot-time backfill of entries
}

// ---- indicator helpers (aligned full-length series, like emaSeries/atrSeries) ----

// rsiSeries is the Wilder RSI over period p.
func rsiSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p+1 {
		return out
	}
	rsi := func(ag, al float64) float64 {
		if al == 0 {
			return 100
		}
		return 100 - 100/(1+ag/al)
	}
	var gain, loss float64
	for i := 1; i <= p; i++ {
		if d := cs[i].Close - cs[i-1].Close; d >= 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	ag, al := gain/float64(p), loss/float64(p)
	out[p] = rsi(ag, al)
	for i := p + 1; i < n; i++ {
		g, l := 0.0, 0.0
		if d := cs[i].Close - cs[i-1].Close; d >= 0 {
			g = d
		} else {
			l = -d
		}
		ag = (ag*float64(p-1) + g) / float64(p)
		al = (al*float64(p-1) + l) / float64(p)
		out[i] = rsi(ag, al)
	}
	return out
}

// smaSeries is the p-bar simple moving average of closes.
func smaSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p {
		return out
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += cs[i].Close
		if i >= p {
			sum -= cs[i-p].Close
		}
		if i >= p-1 {
			out[i] = sum / float64(p)
		}
	}
	return out
}

// stdevSeries is the p-bar population standard deviation of closes (for Bollinger).
func stdevSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p {
		return out
	}
	for i := p - 1; i < n; i++ {
		var m float64
		for j := i - p + 1; j <= i; j++ {
			m += cs[j].Close
		}
		m /= float64(p)
		var v float64
		for j := i - p + 1; j <= i; j++ {
			d := cs[j].Close - m
			v += d * d
		}
		out[i] = math.Sqrt(v / float64(p))
	}
	return out
}

// ---- strategy signals ----

// rsiFadeSignal: 30m short — RSI(3)>90 and close below EMA200 (a bounce inside a
// downtrend). SL +2.5 ATR, TP −2.0 ATR.
func rsiFadeSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	rsi := rsiSeries(cs, 3)[n-1]
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema200 <= 0 {
		return
	}
	price := cs[n-1].Close
	if rsi > 90 && price < ema200 {
		return "short", roundPx(price), roundPx(price + 2.5*atr), roundPx(price - 2.0*atr), true
	}
	return
}

// bollFadeSignal: 1h both — prior bar closed OUTSIDE the Bollinger band and this
// bar closed back INSIDE (failed over-extension), aligned with the EMA200 side.
// Target = middle band; SL = 2.5 ATR; keep only RR in [0.4, 3.0].
func bollFadeSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	sma := smaSeries(cs, 20)
	std := stdevSeries(cs, 20)
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema200 <= 0 || std[n-1] <= 0 || std[n-2] <= 0 {
		return
	}
	price, prev, mid := cs[n-1].Close, cs[n-2].Close, sma[n-1]
	upPrev, loPrev := sma[n-2]+2*std[n-2], sma[n-2]-2*std[n-2]
	upNow, loNow := sma[n-1]+2*std[n-1], sma[n-1]-2*std[n-1]
	switch {
	case prev > upPrev && price <= upNow && price < ema200: // poked above, back in, downtrend → short
		if mid >= price {
			return
		}
		s := price + 2.5*atr
		if rr := (price - mid) / (s - price); rr < 0.4 || rr > 3.0 {
			return
		}
		return "short", roundPx(price), roundPx(s), roundPx(mid), true
	case prev < loPrev && price >= loNow && price > ema200: // poked below, back in, uptrend → long
		if mid <= price {
			return
		}
		s := price - 2.5*atr
		if rr := (mid - price) / (price - s); rr < 0.4 || rr > 3.0 {
			return
		}
		return "long", roundPx(price), roundPx(s), roundPx(mid), true
	}
	return
}

// meanRevSignal: 1h both — close deviates > 2 ATR from EMA20, trend-aligned with
// EMA200 (above → long only, below → short only). Target = EMA20; SL = 3 ATR.
func meanRevSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	ema20 := emaSeries(cs, 20)[n-1]
	ema200 := emaSeries(cs, 200)[n-1]
	atr := atrSeries(cs, 14)[n-1]
	if atr <= 0 || ema20 <= 0 || ema200 <= 0 {
		return
	}
	price := cs[n-1].Close
	dev := price - ema20
	switch {
	case price > ema200 && dev < -2.0*atr: // uptrend dip → long back to EMA20
		return "long", roundPx(price), roundPx(price - 3.0*atr), roundPx(ema20), true
	case price < ema200 && dev > 2.0*atr: // downtrend spike → short back to EMA20
		return "short", roundPx(price), roundPx(price + 3.0*atr), roundPx(ema20), true
	}
	return
}

// ---- generic engine ----

// microTick evaluates one book once per newly closed bar over 銀河 coins.
func (s *Store) microTick(b *microBook) {
	bkt := time.Now().UTC().Unix() / b.barSec
	if bkt == b.bucket {
		return
	}
	b.bucket = bkt
	if !b.seeded { // boot: just set the baseline; only bars that close from now on can open trades
		b.seeded = true
		return
	}
	now := time.Now().UTC()
	for _, coin := range s.emaCoins() {
		cs, err := s.ex.BinanceKlines(coin+"USDT", b.tf, b.klimit)
		if err != nil || len(cs) < 2 {
			continue
		}
		cs = cs[:len(cs)-1] // drop the still-forming bar
		if len(cs) < b.minBars {
			continue
		}
		s.microRun(b, coin, cs, now)
		time.Sleep(25 * time.Millisecond) // pace the REST batch
	}
}

func (s *Store) microRun(b *microBook, coin string, cs []exchange.Candle, now time.Time) {
	last := cs[len(cs)-1]
	barMs := b.barSec * 1000
	b.mu.Lock()
	var open *PaperTrade
	for _, tr := range b.trades {
		if tr.Coin == coin && tr.Status == "open" {
			open = tr
			break
		}
	}
	var dirty *PaperTrade
	if open != nil {
		// bar-close backstop for when the WS feed is down (partial TP1/TP2 are booked
		// on the live stepTP tick). Full-close only: final target / current stop / expiry.
		exit, outcome, px := false, "", 0.0
		if open.Dir == "long" {
			if last.Low <= open.SL {
				exit, outcome, px = true, slOutcome(open), open.SL
			} else if last.High >= open.TP {
				exit, outcome, px = true, "tp3", open.TP
			}
		} else {
			if last.High >= open.SL {
				exit, outcome, px = true, slOutcome(open), open.SL
			} else if last.Low <= open.TP {
				exit, outcome, px = true, "tp3", open.TP
			}
		}
		if !exit && (last.Ts-open.OpenTime.UnixMilli())/barMs >= int64(b.expiry) {
			exit, outcome, px = true, "expired", last.Close
		}
		if exit {
			if outcome == "tp3" {
				open.Legs = 3
			}
			closeTrade(open, px, outcome, now) // blends any realized tranches
		} else {
			open.Cur = roundPx(last.Close)
			open.PnLPct = round2(open.Realized + (1-open.Filled)*pnl(open.Dir, open.Entry, last.Close))
		}
		dirty = open
	} else if !microCooling(b, coin, last.Ts, barMs) {
		if dir, entry, sl, tp, ok := b.signal(cs); ok {
			tr := &PaperTrade{
				ID:       fmt.Sprintf("%s|%s|%d", b.name, coin, now.UnixMilli()),
				Coin:     coin,
				Dir:      dir,
				Entry:    entry,
				SL:       sl,
				TP:       tp,
				Cur:      entry,
				Status:   "open",
				OpenTime: time.UnixMilli(last.Ts).UTC(),
			}
			setupTP(tr, b.plan) // compute TP1/TP2 (分批止盈) at entry
			b.trades = append(b.trades, tr)
			dirty = tr
			microTrim(b)
		}
	}
	b.mu.Unlock()
	if dirty != nil && s.db != nil {
		s.db.upsertTrade(b.name, dirty)
	}
}

// microCooling reports whether coin is still in its post-exit cooldown window.
// Caller holds b.mu.
func microCooling(b *microBook, coin string, barTs, barMs int64) bool {
	cd := int64(b.cooldown) * barMs
	var recent int64
	for _, tr := range b.trades {
		if tr.Coin == coin && tr.Status == "closed" && tr.CloseTime != nil {
			if ms := tr.CloseTime.UnixMilli(); ms > recent {
				recent = ms
			}
		}
	}
	return recent > 0 && barTs-recent < cd
}

// microTrim bounds the closed-trade history. Caller holds b.mu.
func microTrim(b *microBook) {
	var open, closed []*PaperTrade
	for _, tr := range b.trades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closed = append(closed, tr)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime.After(*closed[j].CloseTime) })
	if len(closed) > b.keep {
		closed = closed[:b.keep]
	}
	b.trades = append(open, closed...)
}

// microMarkTick marks open positions to the live WS price and exits any that hit
// the fixed TP/SL intrabar. Entries are still judged on the closed bar in
// microTick; the closed-bar convExit in microRun stays a backstop for feed-down.
func (s *Store) microMarkTick(b *microBook) {
	px := s.livePrices()
	if len(px) == 0 {
		return
	}
	now := time.Now()
	var dirty []*PaperTrade
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.Status != "open" {
			continue
		}
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		before := tr.Legs
		closed := stepTP(tr, p, b.plan, now) // books partial TPs, ratchets stop, closes at TP3/SL
		if closed || tr.Legs != before {
			dirty = append(dirty, tr) // persist on any leg change or final close
		}
	}
	b.mu.Unlock()
	if s.db != nil {
		for _, tr := range dirty {
			s.db.upsertTrade(b.name, tr)
		}
	}
}

// microState returns the book's open/closed/stats, open positions marked live.
func (s *Store) microState(b *microBook) PaperState {
	px := s.livePrices() // read before the lock; open positions get live 現價
	b.mu.Lock()
	defer b.mu.Unlock()
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	st.Stats.MultiTP = b.plan != nil
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
		tpStats(tr, &st.Stats.Tp1, &st.Stats.Tp2, &st.Stats.Tp3, &grossWin, &grossLoss)
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
			st.Stats.ProfitFactor = 99.99 // no losers yet
		}
	}
	return st
}

// ---- per-book public wrappers (ticks + state) ----

func (s *Store) RSIFadeTick()  { s.microTick(s.rsiFadeBook) }
func (s *Store) BollFadeTick() { s.microTick(s.bollFadeBook) }
func (s *Store) MeanRevTick()  { s.microTick(s.meanRevBook) }

func (s *Store) RSIFadeMarkTick()  { s.microMarkTick(s.rsiFadeBook) }
func (s *Store) BollFadeMarkTick() { s.microMarkTick(s.bollFadeBook) }
func (s *Store) MeanRevMarkTick()  { s.microMarkTick(s.meanRevBook) }

// keepIf filters trades to those still open (closedOnly=true) or wipes all (false).
func keepIf(trades []*PaperTrade, closedOnly bool) []*PaperTrade {
	if !closedOnly {
		return nil
	}
	var open []*PaperTrade
	for _, tr := range trades {
		if tr.Status == "open" {
			open = append(open, tr)
		}
	}
	return open
}

// ClearStrategy resets a strategy book's simulated trades (memory + DB). closedOnly
// keeps open positions and drops only the closed history. Returns false for an
// unknown book.
func (s *Store) ClearStrategy(book string, closedOnly bool) bool {
	switch book {
	case "rsifade":
		s.rsiFadeBook.mu.Lock()
		s.rsiFadeBook.trades = keepIf(s.rsiFadeBook.trades, closedOnly)
		s.rsiFadeBook.mu.Unlock()
	case "bollfade":
		s.bollFadeBook.mu.Lock()
		s.bollFadeBook.trades = keepIf(s.bollFadeBook.trades, closedOnly)
		s.bollFadeBook.mu.Unlock()
	case "meanrev":
		s.meanRevBook.mu.Lock()
		s.meanRevBook.trades = keepIf(s.meanRevBook.trades, closedOnly)
		s.meanRevBook.mu.Unlock()
	case "pool":
		s.poolMu.Lock()
		s.poolTrades = keepIf(s.poolTrades, closedOnly)
		s.poolMu.Unlock()
	case "conv":
		s.convMu.Lock()
		s.convTrades = keepIf(s.convTrades, closedOnly)
		s.convMu.Unlock()
	case "main", "gamble", "emaonly":
		s.paperMu.Lock()
		b := s.paperMain
		if book == "gamble" {
			b = s.paperGamble
		} else if book == "emaonly" {
			b = s.paperEMA
		}
		b.trades = keepIf(b.trades, closedOnly)
		s.paperMu.Unlock()
	default:
		return false
	}
	if s.db != nil {
		if closedOnly {
			s.db.clearClosedTrades(book)
		} else {
			s.db.clearTrades(book)
		}
	}
	return true
}

// retrofitMultiTP backfills 分批止盈 levels onto OPEN trades that predate multi-TP,
// so on-going positions adopt the new rules. Idempotent: only trades with no TP1
// set (and no legs booked) are touched. Runs once at startup.
func (s *Store) retrofitMultiTP() {
	type dirtyRow struct {
		book string
		tr   *PaperTrade
	}
	var dirty []dirtyRow
	fill := func(book string, plan *tpPlan, trades []*PaperTrade) {
		if plan == nil {
			return
		}
		for _, tr := range trades {
			if tr.Status == "open" && tr.TP1 == 0 && tr.Legs == 0 && tr.Filled == 0 {
				setupTP(tr, plan)
				dirty = append(dirty, dirtyRow{book, tr})
			}
		}
	}
	s.rsiFadeBook.mu.Lock()
	fill("rsifade", s.rsiFadeBook.plan, s.rsiFadeBook.trades)
	s.rsiFadeBook.mu.Unlock()
	s.bollFadeBook.mu.Lock()
	fill("bollfade", s.bollFadeBook.plan, s.bollFadeBook.trades)
	s.bollFadeBook.mu.Unlock()
	s.meanRevBook.mu.Lock()
	fill("meanrev", s.meanRevBook.plan, s.meanRevBook.trades)
	s.meanRevBook.mu.Unlock()
	s.paperMu.Lock()
	fill("main", s.paperMain.plan, s.paperMain.trades)
	fill("gamble", s.paperGamble.plan, s.paperGamble.trades)
	fill("emaonly", s.paperEMA.plan, s.paperEMA.trades)
	s.paperMu.Unlock()
	if s.db != nil {
		for _, d := range dirty {
			s.db.upsertTrade(d.book, d.tr)
		}
	}
}

func (s *Store) RSIFadeState() PaperState  { return s.microState(s.rsiFadeBook) }
func (s *Store) BollFadeState() PaperState { return s.microState(s.bollFadeBook) }
func (s *Store) MeanRevState() PaperState  { return s.microState(s.meanRevBook) }
