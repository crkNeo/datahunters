package cache

import (
	"fmt"
	"log"
	"math"
	"time"

	"datahunter/internal/exchange"
)

// emaEntryWindow is the hard entry gate for the standalone EMA book: an entry
// may only fill within this long after the hourly close that produced its
// signal. If the pipeline was delayed past it (Binance errors → eval retries,
// slow per-coin fetches, radar recompute), the signal expires unused — entries
// must never fire mid-hour at stale-signal prices.
const emaEntryWindow = 5 * time.Minute

// emaState is the cached 1h EMA read for one coin, computed on CLOSED bars only
// (the still-forming last candle is dropped). Two latched flags drive entries:
// golden state (EMA5>EMA20) and price above EMA50 (mirror for shorts). It backs
// the "狙擊+EMA" filter and the standalone "EMA策略" scanner.
type emaState struct {
	longReady   bool    // EMA5>EMA20 AND close>EMA50 on the last closed bar
	shortReady  bool    // EMA5<EMA20 AND close<EMA50
	justLong    bool    // readiness just went false→true (observed live) — entry trigger
	justShort   bool    // short readiness just went false→true
	above50     bool    // close > EMA50 (raw flag, for the watchlist)
	golden      bool    // EMA5 > EMA20 (raw flag; false ≈ death cross)
	swingLow20  float64 // lowest low of the last 20 closed bars (long stop)
	swingHigh20 float64 // highest high of the last 20 closed bars (short stop)
	atr1h       float64
	sigHour     int64 // UTC hour bucket of this eval — one entry per signal hour
	ok          bool
}

// emaReady is the last OBSERVED readiness for a coin, kept across refreshes so we
// only enter on a genuine live transition. On the first observation we seed it and
// do NOT enter — so a server that boots into an already-true state won't chase it.
type emaReady struct{ long, short bool }

// emaSeries returns the EMA(p) of the candle closes.
func emaSeries(cs []exchange.Candle, p int) []float64 {
	out := make([]float64, len(cs))
	if len(cs) == 0 {
		return out
	}
	k := 2.0 / float64(p+1)
	out[0] = cs[0].Close
	for i := 1; i < len(cs); i++ {
		out[i] = cs[i].Close*k + out[i-1]*(1-k)
	}
	return out
}

// atr14 is the 14-bar Average True Range ending at the last candle.
func atr14(cs []exchange.Candle) float64 {
	n := len(cs)
	if n < 15 {
		return 0
	}
	var sum float64
	for i := n - 14; i < n; i++ {
		tr := cs[i].High - cs[i].Low
		if d := math.Abs(cs[i].High - cs[i-1].Close); d > tr {
			tr = d
		}
		if d := math.Abs(cs[i].Low - cs[i-1].Close); d > tr {
			tr = d
		}
		sum += tr
	}
	return sum / 14
}

// refreshEMA re-evaluates the 1h EMA state ONCE per UTC hour, right after the
// hourly candle closes (the first PaperTick of the new hour). Between hours it's
// a no-op — the closed-bar readiness can't change until the next close, so entries
// land within ~60s of the hourly close and each 1h bar is evaluated exactly once.
// Network only; must be called BEFORE taking paperMu (like refreshFunding).
func (s *Store) refreshEMA(now time.Time) {
	hourBucket := now.UTC().Unix() / 3600
	s.emaMu.Lock()
	if s.emaMap != nil && s.emaHour == hourBucket {
		s.emaMu.Unlock()
		return // already evaluated this hour's close
	}
	prevHour := s.emaHour
	s.emaHour = hourBucket // claim so concurrent/next ticks don't double-fetch
	s.emaMu.Unlock()
	if late := now.UTC().Sub(time.Unix(hourBucket*3600, 0)); late > emaEntryWindow {
		log.Printf("emaonly: hourly eval running %s past the hour (earlier retries failed?) — entries this hour will expire", late.Round(time.Second))
	}

	// pass 1 (no lock): fetch + compute the CURRENT readiness per coin
	type snap struct {
		coin                  string
		nowLong, nowShort     bool
		nowAbove50, nowGolden bool
		swingLo, swingHi      float64
		atr                   float64
	}
	var snaps []snap
	for _, coin := range s.emaCoins() { // top-N by volume (same universe as radar)
		k1h := s.klines1h(coin, 120)      // fresh 1h klines (once per hour), REST fallback
		time.Sleep(40 * time.Millisecond) // pace the once-per-hour fetch, avoid a burst
		if len(k1h) < 60 {
			continue
		}
		c := k1h[:len(k1h)-1] // drop the still-forming bar
		e5 := emaSeries(c, 5)
		e20 := emaSeries(c, 20)
		e50 := emaSeries(c, 50)
		n := len(c) - 1
		if n < 0 {
			continue
		}
		above50 := c[n].Close > e50[n]
		golden := e5[n] > e20[n]
		// swing extremes of the last 20 closed bars (the 20 bars before the entry
		// bar) — the EMA-strategy stop-loss reference.
		lo, hi := c[n].Low, c[n].High
		for j := n - 19; j <= n; j++ {
			if j < 0 {
				continue
			}
			if c[j].Low < lo {
				lo = c[j].Low
			}
			if c[j].High > hi {
				hi = c[j].High
			}
		}
		snaps = append(snaps, snap{
			coin:       coin,
			nowLong:    golden && above50,
			nowShort:   e5[n] < e20[n] && c[n].Close < e50[n],
			nowAbove50: above50,
			nowGolden:  golden,
			swingLo:    lo,
			swingHi:    hi,
			atr:        atr14(c),
		})
	}
	if len(snaps) == 0 {
		// transient fetch failure: roll back the claim so the next tick retries
		s.emaMu.Lock()
		if s.emaHour == hourBucket {
			s.emaHour = prevHour
		}
		s.emaMu.Unlock()
		return
	}

	// pass 2 (locked): arm justLong/justShort only on a false→true transition vs
	// the last OBSERVED readiness. First sighting of a coin is seeded, never armed.
	s.emaMu.Lock()
	if s.emaPrev == nil {
		s.emaPrev = map[string]emaReady{}
	}
	out := make(map[string]emaState, len(snaps))
	for _, sn := range snaps {
		prev, seen := s.emaPrev[sn.coin]
		jl, js := false, false
		if seen {
			jl = sn.nowLong && !prev.long
			js = sn.nowShort && !prev.short
		}
		s.emaPrev[sn.coin] = emaReady{sn.nowLong, sn.nowShort}
		out[sn.coin] = emaState{
			longReady:   sn.nowLong,
			shortReady:  sn.nowShort,
			justLong:    jl,
			justShort:   js,
			above50:     sn.nowAbove50,
			golden:      sn.nowGolden,
			swingLow20:  sn.swingLo,
			swingHigh20: sn.swingHi,
			atr1h:       sn.atr,
			sigHour:     hourBucket,
			ok:          true,
		}
	}
	s.emaMap = out
	s.emaMu.Unlock()
}

// emaLookup returns the cached read for a coin (ok=false if missing/invalid).
func (s *Store) emaLookup(coin string) (emaState, bool) {
	s.emaMu.Lock()
	defer s.emaMu.Unlock()
	st, ok := s.emaMap[coin]
	return st, ok && st.ok
}

// emaConfirm is the filter used by the "狙擊+EMA" book: the trade direction must
// agree with the current 1h EMA state (golden + above EMA50 for long; mirror).
func (s *Store) emaConfirm(coin, dir string) bool {
	st, ok := s.emaLookup(coin)
	if !ok {
		return false
	}
	if dir == "long" {
		return st.longReady
	}
	return st.shortReady
}

// ManualCloseEMA force-closes an open 銀河 (standalone-EMA) trade at the current
// market price, recorded as an "expired" (逾時) exit, and fires the usual close
// alert (Telegram + Web Push). id is the trade's ID. Returns false if no OPEN
// trade in the EMA book matches id.
func (s *Store) ManualCloseEMA(id string) bool {
	px := s.livePrices() // read before the lock (livePrices does its own locking)
	now := time.Now()
	s.paperMu.Lock()
	var target *PaperTrade
	for _, tr := range s.paperEMA.trades {
		if tr.ID == id && tr.Status == "open" {
			target = tr
			break
		}
	}
	if target == nil {
		s.paperMu.Unlock()
		return false
	}
	exit := px[target.Coin]
	if exit <= 0 {
		exit = target.Cur // fall back to the last seen price
	}
	if exit <= 0 {
		exit = target.Entry
	}
	closeTrade(target, exit, "expired", now) // outcome "expired" → UI shows 逾時
	s.paperMu.Unlock()

	s.notifyTradeClose(s.paperEMA, target, now) // TG + Web Push, same as an auto close
	if s.db != nil {
		s.db.upsertTrade("emaonly", target)
	}
	return true
}

// tickEMAOnly is the standalone "EMA策略" book: it does NOT use the radar. It
// enters when both 1h flags first hold — golden state (EMA5>EMA20) AND close
// above EMA50 for long (mirror for short). Stop-loss = the 20-bar swing extreme
// before entry; take-profit = 1:1 of that risk. Exits by TP/SL and expiry.
// Runs under paperMu.
func (s *Store) tickEMAOnly(px map[string]float64, now time.Time) {
	b := s.paperEMA

	active := map[string]bool{}
	open := 0
	for _, tr := range b.trades {
		if tr.Status == "closed" {
			continue
		}
		active[tr.Coin] = true
		open++
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		tr.Cur = p
		tr.PnLPct = pnl(tr.Dir, tr.Entry, p)
		if b.plan != nil {
			before := tr.Legs
			stepTP(tr, p, b.plan, now) // 分批止盈: partial TPs + trailing stop on live price
			if tr.Status == "open" && tr.Legs > before {
				s.notifyTPHit(b.name, tr, b.adminOnly, tr.Legs)
			}
		} else {
			switch tr.Dir {
			case "long":
				if p >= tr.TP {
					closeTrade(tr, tr.TP, "tp", now)
				} else if p <= tr.SL {
					closeTrade(tr, tr.SL, "sl", now)
				}
			case "short":
				if p <= tr.TP {
					closeTrade(tr, tr.TP, "tp", now)
				} else if p >= tr.SL {
					closeTrade(tr, tr.SL, "sl", now)
				}
			}
		}
		// no time expiry: EMA-strategy trades stay open until TP or SL.
		if tr.Status == "closed" {
			s.notifyTradeClose(b, tr, now)
		}
	}

	// entries: both flags just became true (rising edge) on the last closed bar
	for _, coin := range s.emaCoins() {
		if open >= paperMaxOpen {
			break
		}
		if active[coin] {
			continue
		}
		st, ok := s.emaLookup(coin)
		if !ok {
			continue
		}
		var dir string
		switch {
		case st.justLong:
			dir = "long"
		case st.justShort:
			dir = "short"
		default:
			continue
		}
		// hard gate at the FILL moment: however this tick got delayed, never
		// enter more than emaEntryWindow past the signal's hourly close.
		if age := now.Sub(time.Unix(st.sigHour*3600, 0)); age > emaEntryWindow {
			log.Printf("emaonly: %s %s signal expired (%s past the hour), skipped", coin, dir, age.Round(time.Second))
			continue
		}
		key := coin + "|" + dir
		// one entry per signal: skip if we already entered on THIS hour's signal.
		// After the trade closes, only a NEW hourly formation (different sigHour)
		// re-arms an entry — no time cooldown at all.
		if h, ok := b.lastOpenHour[key]; ok && h == st.sigHour {
			continue
		}
		p := px[coin]
		if p <= 0 {
			continue
		}
		// stop = swing extreme of the last 20 bars; take-profit = 1:1 R
		var tp, sl, risk float64
		if dir == "long" {
			sl = st.swingLow20
			risk = p - sl
			if sl <= 0 || risk <= 0 {
				continue // entry at/below the 20-bar low → no valid stop
			}
			tp = p + risk
		} else {
			sl = st.swingHigh20
			risk = sl - p
			if sl <= 0 || risk <= 0 {
				continue
			}
			tp = p - risk
		}
		tr := &PaperTrade{
			ID:   fmt.Sprintf("emaonly|%s|%s|%d", coin, dir, now.UnixMilli()),
			Coin: coin, Dir: dir, Entry: roundPx(p), TP: roundPx(tp), SL: roundPx(sl),
			Cur: roundPx(p), Status: "open", OpenTime: now,
		}
		setupTP(tr, b.plan) // 分批止盈: TP1/TP2 at entry (no-op if plan == nil)
		b.trades = append(b.trades, tr)
		s.notifyTradeOpen(b, tr)
		active[coin] = true
		b.lastOpenHour[key] = st.sigHour // remember we consumed this hour's signal
		open++
	}
	// consume the signals: justLong/justShort are one-shot. Whatever wasn't
	// entered THIS tick (blocked by max-open, missing price, invalid stop, an
	// open position, …) must NOT fire mid-hour on a later tick with an hour-old
	// signal — entries happen only on the tick right after the hourly eval.
	s.emaMu.Lock()
	for coin, st := range s.emaMap {
		if st.justLong || st.justShort {
			st.justLong, st.justShort = false, false
			s.emaMap[coin] = st
		}
	}
	s.emaMu.Unlock()
	b.trim()
}
