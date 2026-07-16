package cache

import (
	"fmt"
	"math"
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// support.go: a 支撐壓力 (support/resistance) monitor over the in-memory coin
// universe (s.coins — every coin is streamed by the WS feed). It does NOT trade —
// it just tracks each coin's nearest tested support (swing lows) and resistance
// (swing highs), and alerts VIP+ users when the latest CLOSED 1h bar breaks below
// support (跌破) or above resistance (突破). Levels are recomputed once per closed
// 1h bar from the feed's in-memory klines, so it costs ZERO exchange REST.

const (
	srLook     = 60    // swing lookback (closed 1h bars)
	srWing     = 3     // fractal wing: bars required lower/higher on each side
	srBand     = 0.004 // cluster pivots within 0.4% into one level
	srMinTouch = 3     // require a level to be tested >= 3 times
)

// SRLevel is the current support/resistance read for one coin (for the VIP page).
type SRLevel struct {
	Coin       string  `json:"coin"`
	Price      float64 `json:"price"`
	Support    float64 `json:"support"`
	Resistance float64 `json:"resistance"`
	SupTouches int     `json:"sup_touches"`
	ResTouches int     `json:"res_touches"`
	SupOK      bool    `json:"sup_ok"`
	ResOK      bool    `json:"res_ok"`
	Status     string  `json:"status"` // break_down | break_up | range
}

// SRData is the VIP page payload.
type SRData struct {
	Levels    []SRLevel `json:"levels"`
	UpdatedAt string    `json:"updated_at"`
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

// swingLows / swingHighs return the fractal pivot prices: a bar whose Low (High)
// is strictly below (above) the `wing` bars on each side. Only bars with `wing`
// confirmed bars after them qualify, so a pivot is never revised once formed.
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

func swingHighs(cs []exchange.Candle, wing int) []float64 {
	var out []float64
	for i := wing; i < len(cs)-wing; i++ {
		h := cs[i].High
		ok := true
		for j := 1; j <= wing; j++ {
			if cs[i-j].High >= h || cs[i+j].High >= h {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, h)
		}
	}
	return out
}

// cluster groups pivots within `band` and returns the most-tested cluster's mean
// price + touch count, requiring >= minTouch touches.
func cluster(pivots []float64, band float64, minTouch int) (price float64, touches int, ok bool) {
	if len(pivots) == 0 {
		return 0, 0, false
	}
	for _, anchor := range pivots {
		var sum float64
		n := 0
		for _, p := range pivots {
			if anchor > 0 && math.Abs(p-anchor)/anchor <= band {
				sum += p
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

// SRTick keeps the support/resistance monitor fresh. Every call it updates the
// live price + status from CACHED levels (cheap). Only when a new 1h bar closes
// (detected via BTC, which is always in the feed) does it recompute levels for
// the whole 銀河 universe and alert on fresh breaks — so klines are fetched at most
// once per hour. Call on a ticker.
func (s *Store) SRTick() {
	px := s.livePrices()

	// new-bar clock from BTC (always in the WS feed → free).
	var newBar int64
	if cs := s.closed1h("BTC", 1); len(cs) > 0 {
		newBar = cs[0].Ts
	}

	s.srMu.Lock()
	isNew := newBar != 0 && newBar != s.srBar
	firstRun := s.srBar == 0
	if !isNew {
		// between bars: just refresh price + status off the cached levels (no klines).
		for coin, info := range s.srInfo {
			if p := px[coin]; p > 0 {
				info.Price = p
				info.Status = srStatus(info, p)
				s.srInfo[coin] = info
			}
		}
		s.srMu.Unlock()
		return
	}
	s.srBar = newBar
	s.srMu.Unlock()

	// NEW BAR: recompute levels for the in-memory (WS feed) coin universe — every
	// coin is already streamed, so closed1h reads from RAM and costs zero REST.
	coins := s.coins
	infos := make(map[string]SRLevel, len(coins))
	barClose := map[string]float64{}
	prevClose := map[string]float64{}
	for _, coin := range coins {
		cs := s.closed1h(coin, srLook)
		info := SRLevel{Coin: coin, Price: px[coin], Status: "range"}
		if p, t, ok := cluster(swingLows(cs, srWing), srBand, srMinTouch); ok {
			info.Support, info.SupTouches, info.SupOK = p, t, true
		}
		if p, t, ok := cluster(swingHighs(cs, srWing), srBand, srMinTouch); ok {
			info.Resistance, info.ResTouches, info.ResOK = p, t, true
		}
		info.Status = srStatus(info, px[coin])
		infos[coin] = info
		if n := len(cs); n > 0 {
			barClose[coin] = cs[n-1].Close
			if n > 1 {
				prevClose[coin] = cs[n-2].Close
			}
		}
	}

	s.srMu.Lock()
	s.srInfo = infos // replace wholesale so coins that dropped out of the universe vanish
	if firstRun {
		s.srMu.Unlock()
		return // seed only; never alert on history at startup
	}
	type breach struct {
		coin, kind   string
		level, price float64
	}
	var breaches []breach
	for coin, info := range infos {
		latest, ok := barClose[coin]
		prev, okp := prevClose[coin]
		if !ok || !okp {
			continue
		}
		switch {
		case info.SupOK && prev >= info.Support && latest < info.Support && s.srState[coin] != "down":
			s.srState[coin] = "down"
			breaches = append(breaches, breach{coin, "down", info.Support, latest})
		case info.ResOK && prev <= info.Resistance && latest > info.Resistance && s.srState[coin] != "up":
			s.srState[coin] = "up"
			breaches = append(breaches, breach{coin, "up", info.Resistance, latest})
		case (!info.SupOK || latest >= info.Support) && (!info.ResOK || latest <= info.Resistance):
			s.srState[coin] = "" // back inside the range → re-arm
		}
	}
	s.srMu.Unlock()

	for _, b := range breaches {
		s.notifySR(b.coin, b.kind, b.level, b.price)
	}
}

// srStatus classifies where price sits relative to the levels (display-only).
func srStatus(info SRLevel, price float64) string {
	if price <= 0 {
		return "range"
	}
	if info.SupOK && price < info.Support {
		return "break_down"
	}
	if info.ResOK && price > info.Resistance {
		return "break_up"
	}
	return "range"
}

// BTCSR returns BTC's support/resistance only, freshened with the live price.
// Public: it feeds the 戰場 (BTC battlefield) walls on the home page. The full
// multi-coin board + breach alerts stay VIP (SR / handleSR).
func (s *Store) BTCSR() SRLevel {
	px := s.livePrices()
	s.srMu.Lock()
	defer s.srMu.Unlock()
	info := s.srInfo["BTC"]
	info.Coin = "BTC"
	if p := px["BTC"]; p > 0 {
		info.Price = p
		info.Status = srStatus(info, p)
	}
	return info
}

// SR returns the current support/resistance levels (only coins that have at least
// one qualifying level), freshened with the live price, sorted by coin. VIP page.
func (s *Store) SR() SRData {
	px := s.livePrices()
	s.srMu.Lock()
	defer s.srMu.Unlock()
	out := SRData{Levels: []SRLevel{}}
	for _, info := range s.srInfo {
		if !info.SupOK && !info.ResOK {
			continue // no tested level → skip (keeps the board meaningful)
		}
		if p := px[info.Coin]; p > 0 {
			info.Price = p
			info.Status = srStatus(info, p)
		}
		out.Levels = append(out.Levels, info)
	}
	sort.Slice(out.Levels, func(i, j int) bool { return out.Levels[i].Coin < out.Levels[j].Coin })
	out.UpdatedAt = time.Now().Format(time.RFC3339)
	return out
}

// notifySR alerts ALL subscribers (Web Push) and the Telegram channel that a
// mainstream coin's latest 1h close broke support (跌破) or resistance (突破).
func (s *Store) notifySR(coin, kind string, level, price float64) {
	var emoji, title, body string
	if kind == "down" {
		emoji, title = "🔻", coin+" 跌破支撐"
		body = fmt.Sprintf("%s 1h 收盤 $%s 跌破支撐 $%s", coin, fmtPx(price), fmtPx(level))
	} else {
		emoji, title = "🚀", coin+" 突破壓力"
		body = fmt.Sprintf("%s 1h 收盤 $%s 突破壓力 $%s", coin, fmtPx(price), fmtPx(level))
	}
	s.PushSend(emoji+" "+title, body, "/?tab=sr") // all subscribers
	if s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("%s <b>[支撐壓力] %s</b>\n%s", emoji, title, body))
	}
}
