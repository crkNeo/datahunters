// Package hyperliquid reads public perp positions from Hyperliquid's info API
// (free, no key). Hyperliquid is fully on-chain transparent: any address's live
// positions — coin, side, size, entry, notional, uPnL, leverage, liquidation
// price — are public. Used by the 名人動向 (whale watch) board. Best-effort:
// on any fetch/parse error the caller keeps the previous snapshot.
package hyperliquid

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

var client = &http.Client{Timeout: 12 * time.Second}

// Position is one open perp position (normalised, price-independent size).
type Position struct {
	Coin     string  `json:"coin"`
	Side     string  `json:"side"`     // long | short
	Size     float64 `json:"size"`     // base units (abs)
	Entry    float64 `json:"entry"`    // entry price
	Mark     float64 `json:"mark"`     // current mark (notional/size)
	Notional float64 `json:"notional"` // position value, USD
	UPnl     float64 `json:"upnl"`     // unrealized PnL, USD
	LiqPx    float64 `json:"liq_px"`   // liquidation price (0 = none)
	LiqDist  float64 `json:"liq_dist"` // % distance from mark to liq (smaller = closer)
	Lev      int     `json:"lev"`      // leverage
}

// FetchPositions returns an address's open perp positions + account value (USD).
func FetchPositions(address string) ([]Position, float64, error) {
	body := []byte(`{"type":"clearinghouseState","user":"` + address + `"}`)
	req, err := http.NewRequest("POST", "https://api.hyperliquid.xyz/info", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		MarginSummary struct {
			AccountValue string `json:"accountValue"`
		} `json:"marginSummary"`
		AssetPositions []struct {
			Position struct {
				Coin          string `json:"coin"`
				Szi           string `json:"szi"`
				EntryPx       string `json:"entryPx"`
				PositionValue string `json:"positionValue"`
				UnrealizedPnl string `json:"unrealizedPnl"`
				LiquidationPx string `json:"liquidationPx"`
				Leverage      struct {
					Value int `json:"value"`
				} `json:"leverage"`
			} `json:"position"`
		} `json:"assetPositions"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, err
	}
	acct := pf(out.MarginSummary.AccountValue)
	positions := make([]Position, 0, len(out.AssetPositions))
	for _, ap := range out.AssetPositions {
		p := ap.Position
		szi := pf(p.Szi)
		if szi == 0 {
			continue
		}
		side := "long"
		if szi < 0 {
			side = "short"
		}
		size := math.Abs(szi)
		notional := pf(p.PositionValue)
		mark := 0.0
		if size > 0 {
			mark = notional / size
		}
		liq := pf(p.LiquidationPx)
		liqDist := 0.0
		if liq > 0 && mark > 0 {
			liqDist = math.Abs(mark-liq) / mark * 100
		}
		positions = append(positions, Position{
			Coin: p.Coin, Side: side, Size: size, Entry: pf(p.EntryPx),
			Mark: round4(mark), Notional: round2(notional), UPnl: round2(pf(p.UnrealizedPnl)),
			LiqPx: round4(liq), LiqDist: round2(liqDist), Lev: p.Leverage.Value,
		})
	}
	return positions, round2(acct), nil
}

// LbRow is one leaderboard row: address, account value, and the 30-day window
// performance (PnL/ROI/volume). Enough to derive both the biggest-account pool
// and the "近30日最賺" smart-money ranking from one fetch.
type LbRow struct {
	Addr     string
	Acct     float64
	PnlMonth float64
	RoiMonth float64
	VlmMonth float64
}

// FetchLeaderboard returns all rows from Hyperliquid's public leaderboard JSON
// (~40MB). Decoded with a streaming decoder; only address/accountValue/month-window
// are kept (other windows are skipped). Poll rarely (the file is large).
func FetchLeaderboard() ([]LbRow, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	resp, err := c.Get("https://stats-data.hyperliquid.xyz/Mainnet/leaderboard")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Rows []struct {
			Addr string               `json:"ethAddress"`
			Acct string               `json:"accountValue"`
			WP   [][2]json.RawMessage `json:"windowPerformances"`
		} `json:"leaderboardRows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	all := make([]LbRow, 0, len(out.Rows))
	for _, r := range out.Rows {
		if r.Addr == "" {
			continue
		}
		lr := LbRow{Addr: r.Addr, Acct: pf(r.Acct)}
		for _, w := range r.WP {
			var name string
			if json.Unmarshal(w[0], &name) != nil || name != "month" {
				continue
			}
			var p struct{ Pnl, Roi, Vlm string }
			if json.Unmarshal(w[1], &p) == nil {
				lr.PnlMonth, lr.RoiMonth, lr.VlmMonth = pf(p.Pnl), pf(p.Roi), pf(p.Vlm)
			}
		}
		all = append(all, lr)
	}
	return all, nil
}

// TopByAcct returns the n rows with the largest account value.
func TopByAcct(rows []LbRow, n int) []LbRow {
	out := append([]LbRow{}, rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Acct > out[j].Acct })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// TopByMonthPnl returns the n rows with the largest 30-day PnL among traders whose
// 30-day ROI clears minRoi — an ROI floor filters out mega-accounts / vaults / market
// makers that book huge PnL on tiny % (they're not "smart money" worth following).
func TopByMonthPnl(rows []LbRow, n int, minRoi float64) []LbRow {
	out := make([]LbRow, 0, len(rows))
	for _, r := range rows {
		if r.PnlMonth > 0 && r.RoiMonth >= minRoi {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PnlMonth > out[j].PnlMonth })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func pf(s string) float64      { v, _ := strconv.ParseFloat(s, 64); return v }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
