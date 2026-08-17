package exchange

import (
	"fmt"
	"math"
	"strings"
)

// collector_feed.go holds the extra public endpoints the 1m snapshot collector
// needs. They are kept apart from client.go so the collector can evolve without
// churning the endpoints the live dashboard depends on.
//
// Everything here is public + keyless, and every Binance call goes through the
// same get() lane machinery (pacing / 418-429 circuit breaker / used-weight
// watch) as the rest of the package.

// PremiumIndex is one row of /fapi/v1/premiumIndex — mark price, index price,
// the current funding rate and when it settles. One batch call covers every
// symbol, which is why the collector reads funding/mark/index from here instead
// of paying a per-symbol request for each.
type PremiumIndex struct {
	Symbol        string
	Mark          float64
	Index         float64
	Funding       float64
	NextFundingTs int64 // unix ms
}

// BinancePremiumIndexAll returns the premium index for every USDT-margined
// perpetual in ONE request, keyed by symbol (e.g. "BTCUSDT").
//
// Note the difference from BinanceAllFunding, which keys by COIN and returns
// only the rate: the collector needs mark/index (for basis) and the settlement
// timestamp (to tell directional OI from funding-arb OI), so it wants the full
// row keyed by symbol.
func (c *Client) BinancePremiumIndexAll() (map[string]PremiumIndex, error) {
	var raw []struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
	}
	if err := c.get(binanceFapi+"/fapi/v1/premiumIndex", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]PremiumIndex, len(raw))
	for _, r := range raw {
		if !strings.HasSuffix(r.Symbol, "USDT") || strings.Contains(r.Symbol, "_") {
			continue
		}
		out[r.Symbol] = PremiumIndex{
			Symbol:        r.Symbol,
			Mark:          atof(r.MarkPrice),
			Index:         atof(r.IndexPrice),
			Funding:       atof(r.LastFundingRate),
			NextFundingTs: r.NextFundingTime,
		}
	}
	return out, nil
}

// Depth is an order-book snapshot reduced to the only thing the screener cares
// about: how many USD sit within a given distance of mid.
//
// This is the "拉抬成本" term. Two coins with identical OI and funding can
// deliver a 5% move and a 40% move off the same forced-liquidation flow purely
// because one book is ten times thinner. Nothing derived from price/volume
// alone can substitute for it.
type Depth struct {
	Bid1      float64
	Ask1      float64
	SpreadBps float64
	// USD notional resting within 0.5% / 1% / 2% of mid, per side.
	BidUSD05, BidUSD1, BidUSD2 float64
	AskUSD05, AskUSD1, AskUSD2 float64
}

// BinanceDepth fetches the order book and reduces it to cumulative USD depth
// bands. limit follows Binance's allowed values (5/10/20/50/100/500/1000); the
// request weight climbs with it, so the collector defaults to a modest limit
// and a slower cadence than the 1m snapshot.
//
// A limit too small to span ±2% silently under-reports the outer bands, so the
// truncated flag is set when the book ran out before the widest band — treat a
// truncated row as a lower bound, not a measurement.
func (c *Client) BinanceDepth(symbol string, limit int) (Depth, bool, error) {
	url := fmt.Sprintf("%s/fapi/v1/depth?symbol=%s&limit=%d", binanceFapi, symbol, limit)
	var raw struct {
		Bids [][]any `json:"bids"`
		Asks [][]any `json:"asks"`
	}
	if err := c.get(url, &raw); err != nil {
		return Depth{}, false, err
	}
	if len(raw.Bids) == 0 || len(raw.Asks) == 0 {
		return Depth{}, false, fmt.Errorf("binance depth %s: empty book", symbol)
	}
	var d Depth
	d.Bid1 = toFloat(raw.Bids[0][0])
	d.Ask1 = toFloat(raw.Asks[0][0])
	mid := (d.Bid1 + d.Ask1) / 2
	if mid <= 0 {
		return Depth{}, false, fmt.Errorf("binance depth %s: bad mid", symbol)
	}
	d.SpreadBps = (d.Ask1 - d.Bid1) / mid * 10000

	// Walk each side outward, accumulating notional into the bands it falls in.
	// Levels arrive best-first, so the first level outside 2% ends that side.
	sum := func(levels [][]any, sign float64, b05, b1, b2 *float64) (spanned bool) {
		for _, lv := range levels {
			if len(lv) < 2 {
				continue
			}
			px, qty := toFloat(lv[0]), toFloat(lv[1])
			if px <= 0 || qty <= 0 {
				continue
			}
			dist := sign * (px - mid) / mid // ≥0 as we move away from mid
			if dist > 0.02 {
				return true // book reached past the widest band — not truncated
			}
			usd := px * qty
			if dist <= 0.005 {
				*b05 += usd
			}
			if dist <= 0.01 {
				*b1 += usd
			}
			*b2 += usd
		}
		return false
	}
	bidSpanned := sum(raw.Bids, -1, &d.BidUSD05, &d.BidUSD1, &d.BidUSD2)
	askSpanned := sum(raw.Asks, +1, &d.AskUSD05, &d.AskUSD1, &d.AskUSD2)
	return d, !(bidSpanned && askSpanned), nil
}

// SymbolInfo is the tradability metadata the universe table records daily.
// OnboardTs matters more than it looks: a coin listed hours ago has no history
// to compute a percentile against, and its first-day volatility is not
// comparable to anything — the label pipeline needs to be able to exclude it.
type SymbolInfo struct {
	Symbol    string
	Status    string // "TRADING", "SETTLING", "CLOSE", ...
	OnboardTs int64  // unix ms
}

// BinanceSymbolInfo returns status + onboard date for every USDT-margined
// PERPETUAL, in one call.
//
// The collector writes this to universe_1d every day, and that daily row is the
// only defence against survivorship bias: three months from now, a backtest
// that only knows today's symbol list will silently exclude every coin that got
// delisted in between — and delisted coins are disproportionately the ones that
// had violent moves.
func (c *Client) BinanceSymbolInfo() (map[string]SymbolInfo, error) {
	var raw struct {
		Symbols []struct {
			Symbol       string `json:"symbol"`
			Status       string `json:"status"`
			ContractType string `json:"contractType"`
			QuoteAsset   string `json:"quoteAsset"`
			OnboardDate  int64  `json:"onboardDate"`
		} `json:"symbols"`
	}
	if err := c.get(binanceFapi+"/fapi/v1/exchangeInfo", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]SymbolInfo, len(raw.Symbols))
	for _, s := range raw.Symbols {
		if s.QuoteAsset != "USDT" || s.ContractType != "PERPETUAL" {
			continue
		}
		out[s.Symbol] = SymbolInfo{Symbol: s.Symbol, Status: s.Status, OnboardTs: s.OnboardDate}
	}
	return out, nil
}

// BinanceSpotSymbols returns the set of SPOT symbols that are currently
// trading. The collector uses it to skip spot requests for perp-only coins
// instead of eating a 400 per symbol per minute.
func (c *Client) BinanceSpotSymbols() (map[string]bool, error) {
	var raw struct {
		Symbols []struct {
			Symbol string `json:"symbol"`
			Status string `json:"status"`
		} `json:"symbols"`
	}
	if err := c.get(binanceSpot+"/api/v3/exchangeInfo", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(raw.Symbols))
	for _, s := range raw.Symbols {
		if s.Status == "TRADING" {
			out[s.Symbol] = true
		}
	}
	return out, nil
}

// finite reports whether f is safe to hand to the MySQL driver. NaN and ±Inf
// are rejected by the wire protocol, and they are easy to produce from a
// division by an empty book or a zero price — so every derived value the
// collector stores is filtered through this.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// NullZero returns 0 for non-finite input, otherwise the value itself.
func NullZero(f float64) float64 {
	if !finite(f) {
		return 0
	}
	return f
}
