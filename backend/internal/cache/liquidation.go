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
			key := fmt.Sprintf("%s|%s|%d|%.4g|%.4g", coin, side, e.Ts, e.Px, e.Sz)
			s.liqMu.Lock()
			seen := s.liqSeen[key]
			if !seen {
				s.liqSeen[key] = true
			}
			s.liqMu.Unlock()
			if seen {
				continue
			}
			row := LiqRow{Coin: coin, Side: side, Px: round2(e.Px), USD: round2(usd), Time: e.Ts}
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
