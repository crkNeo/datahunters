package cache

import (
	"fmt"
	"sort"
	"time"
)

// LiqRow is one liquidation event (normalised to USD notional).
type LiqRow struct {
	Coin string  `json:"coin"`
	Side string  `json:"side"` // long | short (position liquidated)
	Px   float64 `json:"px"`
	USD  float64 `json:"usd"`
	Time int64   `json:"time"` // unix ms
}

// LiqData is the /api/liquidations payload: a recent feed + 1h side totals.
type LiqData struct {
	Recent     []LiqRow `json:"recent"`       // newest first
	LongUSD1h  float64  `json:"long_usd_1h"`  // longs liquidated in last 1h
	ShortUSD1h float64  `json:"short_usd_1h"` // shorts liquidated in last 1h
	UpdatedAt  string   `json:"updated_at"`
	Note       string   `json:"note"`
}

// liqKey is the in-memory de-dupe key for one liquidation, built from the
// rounded/stored fields so a SQLite warm-load reproduces it exactly.
func liqKey(r LiqRow) string {
	return fmt.Sprintf("%s|%s|%d|%.4g|%.4g", r.Coin, r.Side, r.Time, r.Px, r.USD)
}

// LiqTick polls OKX liquidations for the tracked coins, de-dupes, persists new
// events, and keeps a rolling in-memory feed. Call on a ticker.
func (s *Store) LiqTick() {
	// contract values (base units per contract) for USD conversion, cached daily
	s.liqMu.Lock()
	if s.ctVal == nil || time.Since(s.ctValTime) > 24*time.Hour {
		if m, err := s.ex.OKXContractVal(); err == nil && len(m) > 0 {
			s.ctVal = m
			s.ctValTime = time.Now()
		}
	}
	ctVal := s.ctVal
	s.liqMu.Unlock()

	now := time.Now()
	var fresh []LiqRow
	for _, coin := range s.coins {
		evs, err := s.ex.OKXLiquidations(coin + "-USDT")
		if err != nil {
			continue
		}
		cv := ctVal[coin]
		if cv <= 0 {
			cv = 1 // fall back to raw contracts if unknown
		}
		for _, e := range evs {
			side := "long"
			if e.PosSide == "short" {
				side = "short"
			}
			usd := e.Sz * cv * e.Px
			row := LiqRow{Coin: coin, Side: side, Px: round2(e.Px), USD: round2(usd), Time: e.Ts}
			// dedupe key uses the *stored* (rounded) fields so a startup warm-load
			// from SQLite builds the identical key — see liqKey / recentLiquidations.
			key := liqKey(row)
			s.liqMu.Lock()
			seen := s.liqSeen[key]
			if !seen {
				s.liqSeen[key] = true
			}
			s.liqMu.Unlock()
			if seen {
				continue
			}
			fresh = append(fresh, row)
			if s.db != nil {
				s.db.insertLiquidation(row)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}

	s.liqMu.Lock()
	s.liqFeed = append(s.liqFeed, fresh...)
	// keep last 24h in memory
	cut := now.Add(-24 * time.Hour).UnixMilli()
	kept := s.liqFeed[:0]
	for _, r := range s.liqFeed {
		if r.Time >= cut {
			kept = append(kept, r)
		}
	}
	s.liqFeed = append([]LiqRow{}, kept...)
	// bound the dedupe set
	if len(s.liqSeen) > 20000 {
		s.liqSeen = map[string]bool{}
	}
	s.liqTime = now
	s.liqMu.Unlock()
}

// LiqHeatBucket is one price band of the liquidation heat map.
type LiqHeatBucket struct {
	Hi    float64 `json:"hi"`    // 該價帶上緣
	Long  float64 `json:"long"`  // 多單被清 USD
	Short float64 `json:"short"` // 空單被清 USD
}

// LiqHeatData is the /api/liquidations/heat payload: one coin's liquidations
// bucketed by price over a selectable window (1h/4h/24h), plus the coin list
// ranked by liquidation volume in that same window.
type LiqHeatData struct {
	Coin    string          `json:"coin"`
	Window  string          `json:"window"`
	Coins   []string        `json:"coins"`   // 該視窗內清算量大→小
	Buckets []LiqHeatBucket `json:"buckets"` // 高價在上
	Max     float64         `json:"max"`     // 單桶 long+short 最大值(前端算寬度用)
}

// LiquidationHeat aggregates the in-memory 24h feed into price buckets for a
// single coin over the requested window. Honest: this is the *actual* recent
// liquidation distribution by price, not an OI-based magnet prediction.
func (s *Store) LiquidationHeat(coin, window string) LiqHeatData {
	var winMs int64
	switch window {
	case "4h":
		winMs = 4 * 3600 * 1000
	case "24h", "1d":
		window, winMs = "24h", 24*3600*1000
	default:
		window, winMs = "1h", 3600*1000
	}
	cut := time.Now().UnixMilli() - winMs

	s.liqMu.Lock()
	feed := make([]LiqRow, 0, len(s.liqFeed))
	for _, r := range s.liqFeed {
		if r.Time >= cut {
			feed = append(feed, r)
		}
	}
	s.liqMu.Unlock()

	// coin list ranked by liquidation USD in this window
	tot := map[string]float64{}
	for _, r := range feed {
		tot[r.Coin] += r.USD
	}
	coins := make([]string, 0, len(tot))
	for c := range tot {
		coins = append(coins, c)
	}
	sort.Slice(coins, func(i, j int) bool { return tot[coins[i]] > tot[coins[j]] })

	out := LiqHeatData{Coin: coin, Window: window, Coins: coins, Buckets: []LiqHeatBucket{}}
	if out.Coin == "" && len(coins) > 0 {
		out.Coin = coins[0]
	}
	if out.Coin == "" {
		return out
	}

	// events for the selected coin
	evs := feed[:0:0]
	for _, r := range feed {
		if r.Coin == out.Coin && r.Px > 0 {
			evs = append(evs, r)
		}
	}
	if len(evs) < 3 {
		return out // 樣本太少,不畫誤導人的圖
	}
	lo, hi := evs[0].Px, evs[0].Px
	for _, e := range evs {
		if e.Px < lo {
			lo = e.Px
		}
		if e.Px > hi {
			hi = e.Px
		}
	}
	if hi <= lo {
		return out
	}
	const N = 12
	longs := make([]float64, N)
	shorts := make([]float64, N)
	his := make([]float64, N)
	for i := 0; i < N; i++ {
		his[i] = lo + (hi-lo)*float64(i+1)/N
	}
	for _, e := range evs {
		idx := int((e.Px - lo) / (hi - lo) * N)
		if idx >= N {
			idx = N - 1
		}
		if idx < 0 {
			idx = 0
		}
		if e.Side == "long" {
			longs[idx] += e.USD
		} else {
			shorts[idx] += e.USD
		}
	}
	max := 1.0
	for i := 0; i < N; i++ {
		if longs[i]+shorts[i] > max {
			max = longs[i] + shorts[i]
		}
	}
	for i := N - 1; i >= 0; i-- { // 高價在上
		out.Buckets = append(out.Buckets, LiqHeatBucket{Hi: round2(his[i]), Long: round2(longs[i]), Short: round2(shorts[i])})
	}
	out.Max = round2(max)
	return out
}

// Liquidations returns the recent feed (newest first) + 1h side totals.
func (s *Store) Liquidations() LiqData {
	s.liqMu.Lock()
	defer s.liqMu.Unlock()
	out := LiqData{
		Recent:    []LiqRow{},
		UpdatedAt: s.liqTime.Format(time.RFC3339),
		Note:      "清算事件(OKX 永續,即時免費)· 非回測訊號;已往 SQLite 累積",
	}
	hourAgo := time.Now().Add(-time.Hour).UnixMilli()
	for _, r := range s.liqFeed {
		if r.Time >= hourAgo {
			if r.Side == "long" {
				out.LongUSD1h += r.USD
			} else {
				out.ShortUSD1h += r.USD
			}
		}
	}
	out.LongUSD1h, out.ShortUSD1h = round2(out.LongUSD1h), round2(out.ShortUSD1h)
	// newest first, cap to 100
	feed := append([]LiqRow{}, s.liqFeed...)
	sort.Slice(feed, func(i, j int) bool { return feed[i].Time > feed[j].Time })
	if len(feed) > 100 {
		feed = feed[:100]
	}
	out.Recent = feed
	return out
}
