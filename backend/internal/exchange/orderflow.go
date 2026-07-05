package exchange

import (
	"fmt"
	"strconv"
)

// LiqEvent is one liquidation fill (the side that got liquidated, price, size).
type LiqEvent struct {
	Px      float64
	Sz      float64 // contracts
	PosSide string  // long | short (position that was liquidated)
	Ts      int64
}

// OKXLiquidations fetches recent liquidation orders for a SWAP family, e.g.
// instFamily "BTC-USDT" (free, no auth). Size is in contracts (see OKXContractVal).
func (c *Client) OKXLiquidations(instFamily string) ([]LiqEvent, error) {
	var raw struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				BkPx    string `json:"bkPx"`
				Sz      string `json:"sz"`
				PosSide string `json:"posSide"`
				Ts      string `json:"ts"`
			} `json:"details"`
		} `json:"data"`
	}
	url := fmt.Sprintf("%s/api/v5/public/liquidation-orders?instType=SWAP&instFamily=%s&state=filled&limit=100", okxBase, instFamily)
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	var out []LiqEvent
	for _, d := range raw.Data {
		for _, x := range d.Details {
			ts, _ := strconv.ParseInt(x.Ts, 10, 64)
			out = append(out, LiqEvent{Px: atof(x.BkPx), Sz: atof(x.Sz), PosSide: x.PosSide, Ts: ts})
		}
	}
	return out, nil
}

// OKXContractVal returns coin -> contract value (base units per contract) for
// USDT SWAPs, so liquidation size in contracts can be converted to notional USD.
func (c *Client) OKXContractVal() (map[string]float64, error) {
	var raw struct {
		Data []struct {
			InstFamily string `json:"instFamily"`
			CtVal      string `json:"ctVal"`
			SettleCcy  string `json:"settleCcy"`
		} `json:"data"`
	}
	url := okxBase + "/api/v5/public/instruments?instType=SWAP"
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, d := range raw.Data {
		if d.SettleCcy != "USDT" || d.InstFamily == "" {
			continue
		}
		coin := d.InstFamily
		if i := len(coin) - len("-USDT"); i > 0 && coin[i:] == "-USDT" {
			coin = coin[:i]
		}
		out[coin] = atof(d.CtVal)
	}
	return out, nil
}
