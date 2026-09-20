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

func pf(s string) float64      { v, _ := strconv.ParseFloat(s, 64); return v }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
