package cache

import (
	"fmt"
	"math"
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// support.go implements the "支撐跌破 3R" strategy: a short taken when the latest
// CLOSED 1h bar closes below a tested support level. It runs only on the four
// coins below, is evaluated bar-by-bar (deterministic, zero REST — all klines
// come from the in-memory WS feed), and its entries alert admins only (Telegram
// + admin Web Push). Trades are simulated in memory (not persisted).

// supportCoins is the fixed universe this strategy scans.
var supportCoins = []string{"BTC", "ETH", "SOL", "BNB"}

const (
	supLook       = 60    // swing-low lookback (closed 1h bars)
	supWing       = 3     // fractal wing: bars required lower on each side
	supBand       = 0.004 // cluster low points within 0.4% into one support band
	supMinTouch   = 3     // require the band to be tested >= 3 times
	supSLPad      = 0.006 // SL = support * (1 + 0.6%)
	supRR         = 3.0   // target = entry - 3R
	supUnitU      = 10.0  // 1R sized at 10 USDT (SL = -10U, TP = +30U)
	supExpiryBars = 200   // settle at market after 200 bars
	supKeepClose  = 500   // in-memory closed-trade cap
)

// SupportInfo is the current support read for one coin, shown on the admin page
// whether or not a trade is open.
type SupportInfo struct {
	Coin    string  `json:"coin"`
	Support float64 `json:"support"`  // 0 if no band qualifies
	Touches int     `json:"touches"`  // how many swing lows formed the band
	Price   float64 `json:"price"`    // live price
	DistPct float64 `json:"dist_pct"` // (price - support)/support*100: distance above support
	HasOpen bool    `json:"has_open"` // a trade is currently open on this coin
	OK      bool    `json:"ok"`       // a support level was found
}

// SupportTrade is one simulated support-breakdown short.
type SupportTrade struct {
	ID        string     `json:"-"`
	Coin      string     `json:"coin"`
	Support   float64    `json:"support"` // the level it broke
	Entry     float64    `json:"entry"`   // breakdown bar close
	SL        float64    `json:"sl"`
	TP        float64    `json:"tp"`
	Cur       float64    `json:"cur"`     // latest price (live for open, exit for closed)
	R         float64    `json:"-"`       // risk in price (SL - entry)
	PnLR      float64    `json:"pnl_r"`   // R multiple (live or final)
	PnLU      float64    `json:"pnl_u"`   // PnLR * 10 (USDT)
	Status    string     `json:"status"`  // open | closed
	Outcome   string     `json:"outcome"` // "" | tp | sl | expired
	OpenTime  time.Time  `json:"open_time"`
	CloseTime *time.Time `json:"close_time,omitempty"`
	OpenBar   int64      `json:"-"` // Ts (ms) of the breakdown bar, for 200-bar expiry
}

// SupportStats summarises closed trades (P&L in USDT at 1R = 10U).
type SupportStats struct {
	Closed  int     `json:"closed"`
	Wins    int     `json:"wins"`
	Losses  int     `json:"losses"`
	WinRate float64 `json:"win_rate"`
	TotalU  float64 `json:"total_u"`
}

// SupportState is the admin page payload.
type SupportState struct {
	Supports []SupportInfo   `json:"supports"`
	Open     []*SupportTrade `json:"open"`
	Closed   []*SupportTrade `json:"closed"`
	Stats    SupportStats    `json:"stats"`
}

// closed1h returns a coin's most recent CLOSED 1h candles (oldest→newest), up to
// limit. Prefers the WS feed's closed buffer (free); falls back to REST and drops
// the last bar (Binance includes the in-progress candle as the final element).
func (s *Store) closed1h(coin string, limit int) []exchange.Candle {
	if s.feed != nil && s.feed.Healthy() {
		if cs := s.feed.Klines(coin); len(cs) > 0 {
			if len(cs) > limit {
				cs = cs[len(cs)-limit:]
			}
			return cs
		}
	}
	cs, _ := s.ex.BinanceKlines(coin+"USDT", "1h", limit+1)
	if len(cs) > 0 {
		cs = cs[:len(cs)-1] // drop the still-forming last bar
	}
	if len(cs) > limit {
		cs = cs[len(cs)-limit:]
	}
	return cs
}

// swingLows returns the Low prices of fractal swing lows in cs: a bar whose Low
// is strictly below the `wing` bars on each side. Only bars with `wing` confirmed
// bars after them qualify, so a swing low is never revised once formed.
func swingLows(cs []exchange.Candle, wing int) []float64 {
	var out []float64
	for i := wing; i < len(cs)-wing; i++ {
		l := cs[i].Low
		ok := true
		for j := 1; j <= wing; j++ {
			if cs[i-j].Low <= l || cs[i+j].Low <= l {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, l)
		}
	}
	return out
}

// findSupport builds the strongest support from swing lows in the last `look`
// closed bars: fractal lows (wing), grouped within `band`, take the most-tested
// group with >= minTouch touches; band price = the group's mean low.
func findSupport(cs []exchange.Candle, look, wing int, band float64, minTouch int) (price float64, touches int, ok bool) {
	if len(cs) > look {
		cs = cs[len(cs)-look:]
	}
	lows := swingLows(cs, wing)
	if len(lows) == 0 {
		return 0, 0, false
	}
	for _, anchor := range lows { // try clustering around each low
		var sum float64
		n := 0
		for _, l := range lows {
			if math.Abs(l-anchor)/anchor <= band {
				sum += l
				n++
			}
		}
		if n > touches {
			touches = n
			price = sum / float64(n)
		}
	}
	if touches < minTouch {
		return 0, 0, false
	}
	return price, touches, true
}

// SupportTick refreshes the four coins' current support (for display) and, once
// per newly closed 1h bar, evaluates open trades and opens new breakdown shorts.
// Call on a ticker; the per-bar logic is throttled to real bar closes.
func (s *Store) SupportTick() {
	px := s.livePrices()

	// per-coin: current support read + the latest two closed bars.
	infos := make(map[string]SupportInfo, len(supportCoins))
	barClose := map[string]float64{}   // latest closed bar close
	prevClose := map[string]float64{}  // the bar before it
	barHL := map[string][2]float64{}   // latest closed bar [High, Low]
	var newBar int64                   // newest closed-bar Ts across the coins (bar clock)
	for _, coin := range supportCoins {
		cs := s.closed1h(coin, supLook)
		info := SupportInfo{Coin: coin, Price: px[coin]}
		if p, t, ok := findSupport(cs, supLook, supWing, supBand, supMinTouch); ok {
			info.Support, info.Touches, info.OK = p, t, true
			if px[coin] > 0 {
				info.DistPct = round2((px[coin] - p) / p * 100)
			}
		}
		infos[coin] = info
		if n := len(cs); n > 0 {
			last := cs[n-1]
			barClose[coin] = last.Close
			barHL[coin] = [2]float64{last.High, last.Low}
			if last.Ts > newBar {
				newBar = last.Ts
			}
			if n > 1 {
				prevClose[coin] = cs[n-2].Close
			}
		}
	}

	s.supMu.Lock()
	defer s.supMu.Unlock()

	hasOpen := map[string]bool{}
	for _, tr := range s.supTrades {
		if tr.Status == "open" {
			hasOpen[tr.Coin] = true
		}
	}
	for coin, info := range infos {
		info.HasOpen = hasOpen[coin]
		s.supInfo[coin] = info
	}

	// live floating P&L for open trades (display only; closes happen on bar close).
	for _, tr := range s.supTrades {
		if tr.Status != "open" {
			continue
		}
		if p := px[tr.Coin]; p > 0 {
			tr.Cur = p
			tr.PnLR = round2((tr.Entry - p) / tr.R) // short
			tr.PnLU = round2(tr.PnLR * supUnitU)
		}
	}

	// per-bar logic runs only when a new closed bar appears.
	if newBar == 0 || newBar == s.supBar {
		return
	}
	firstRun := s.supBar == 0
	s.supBar = newBar
	if firstRun {
		return // seed the bar clock; never act on history at startup
	}
	now := time.Now()

	// 1) evaluate open trades against the just-closed bar's High/Low.
	for _, tr := range s.supTrades {
		if tr.Status != "open" {
			continue
		}
		hl, ok := barHL[tr.Coin]
		if !ok {
			continue
		}
		high, low := hl[0], hl[1]
		switch {
		case high >= tr.SL: // same-bar both touched → SL (conservative)
			s.closeSupport(tr, tr.SL, "sl", now)
		case low <= tr.TP:
			s.closeSupport(tr, tr.TP, "tp", now)
		case (newBar-tr.OpenBar)/3_600_000 >= supExpiryBars: // 200 bars → market settle
			s.closeSupport(tr, barClose[tr.Coin], "expired", now)
		}
		if tr.Status == "closed" {
			s.notifySupportClose(tr, now)
		}
	}

	// 2) fresh breakdown (prev close >= support, latest close < support) → open short.
	for _, coin := range supportCoins {
		if hasOpen[coin] {
			continue
		}
		info := s.supInfo[coin]
		if !info.OK {
			continue
		}
		latest, ok := barClose[coin]
		prev, okp := prevClose[coin]
		if !ok || !okp || !(prev >= info.Support && latest < info.Support) {
			continue
		}
		sl := info.Support * (1 + supSLPad)
		r := sl - latest
		if r <= 0 {
			continue
		}
		tr := &SupportTrade{
			ID: fmt.Sprintf("support|%s|%d", coin, now.UnixMilli()),
			Coin:    coin,
			Support: roundPx(info.Support),
			Entry:   roundPx(latest),
			SL:      roundPx(sl),
			TP:      roundPx(latest - supRR*r),
			Cur:     roundPx(latest),
			R:       r,
			Status:  "open",
			OpenTime: now,
			OpenBar:  newBar,
		}
		s.supTrades = append(s.supTrades, tr)
		hasOpen[coin] = true
		info.HasOpen = true
		s.supInfo[coin] = info
		s.notifySupportOpen(tr)
	}

	s.trimSupport()
}

func (s *Store) closeSupport(tr *SupportTrade, exit float64, outcome string, now time.Time) {
	tr.Status = "closed"
	tr.Outcome = outcome
	tr.Cur = roundPx(exit)
	tr.PnLR = round2((tr.Entry - exit) / tr.R) // short
	tr.PnLU = round2(tr.PnLR * supUnitU)
	t := now
	tr.CloseTime = &t
}

func (s *Store) trimSupport() {
	var open, closed []*SupportTrade
	for _, tr := range s.supTrades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closed = append(closed, tr)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime.After(*closed[j].CloseTime) })
	if len(closed) > supKeepClose {
		closed = closed[:supKeepClose]
	}
	s.supTrades = append(open, closed...)
}

// SupportState snapshots the strategy for the admin page: the four current
// supports (freshened with the live price) plus open/closed trades and stats.
func (s *Store) SupportState() SupportState {
	px := s.livePrices()
	s.supMu.Lock()
	defer s.supMu.Unlock()
	st := SupportState{Supports: []SupportInfo{}, Open: []*SupportTrade{}, Closed: []*SupportTrade{}}
	for _, coin := range supportCoins {
		info := s.supInfo[coin]
		if info.Coin == "" {
			info.Coin = coin
		}
		if p := px[coin]; p > 0 {
			info.Price = p
			if info.OK && info.Support > 0 {
				info.DistPct = round2((p - info.Support) / info.Support * 100)
			}
		}
		st.Supports = append(st.Supports, info)
	}
	for _, tr := range s.supTrades {
		if tr.Status == "open" {
			st.Open = append(st.Open, tr)
			continue
		}
		st.Closed = append(st.Closed, tr)
		st.Stats.Closed++
		st.Stats.TotalU += tr.PnLU
		if tr.PnLU > 0 {
			st.Stats.Wins++
		} else {
			st.Stats.Losses++
		}
	}
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].OpenTime.After(st.Open[j].OpenTime) })
	sort.Slice(st.Closed, func(i, j int) bool { return st.Closed[i].CloseTime.After(*st.Closed[j].CloseTime) })
	if st.Stats.Closed > 0 {
		st.Stats.WinRate = round2(float64(st.Stats.Wins) / float64(st.Stats.Closed) * 100)
	}
	st.Stats.TotalU = round2(st.Stats.TotalU)
	return st
}

func outcomeSupCN(o string) string {
	switch o {
	case "tp":
		return "止盈 +3R"
	case "sl":
		return "止損 −1R"
	case "expired":
		return "逾時結算"
	}
	return o
}

// notifySupportOpen / notifySupportClose alert ADMINS ONLY: Telegram (admin chat)
// + admin Web Push subscribers — never the whole member base.
func (s *Store) notifySupportOpen(tr *SupportTrade) {
	if s.pushMgr != nil && s.db != nil {
		if subs := s.db.adminSubs(); len(subs) > 0 {
			s.pushMgr.SendTo(subs, "📉 支撐跌破 開空",
				fmt.Sprintf("%s 跌破 $%s · 進場 $%s", tr.Coin, fmtPx(tr.Support), fmtPx(tr.Entry)), "/?tab=support")
		}
	}
	if s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("📉 <b>[支撐跌破] 開空</b> %s\n跌破支撐 $%s · 進場 $%s\nSL $%s(支撐+0.6%%) · TP $%s(3R) · 1R=%.0fU",
			tr.Coin, fmtPx(tr.Support), fmtPx(tr.Entry), fmtPx(tr.SL), fmtPx(tr.TP), supUnitU))
	}
}

func (s *Store) notifySupportClose(tr *SupportTrade, now time.Time) {
	if s.pushMgr != nil && s.db != nil {
		if subs := s.db.adminSubs(); len(subs) > 0 {
			s.pushMgr.SendTo(subs, "支撐跌破 平倉",
				fmt.Sprintf("%s %s · %+.0fU", tr.Coin, outcomeSupCN(tr.Outcome), tr.PnLU), "/?tab=support")
		}
	}
	if s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("🔴 <b>[支撐跌破] 平倉</b> %s\n結果 %s · 損益 %+.0fU (%+.2fR) · 持倉 %s\n進 $%s → 出 $%s",
			tr.Coin, outcomeSupCN(tr.Outcome), tr.PnLU, tr.PnLR, fmtDur(now.Sub(tr.OpenTime)), fmtPx(tr.Entry), fmtPx(tr.Cur)))
	}
}
