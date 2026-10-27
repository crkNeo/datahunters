package newsfeed

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ---- Polymarket 加密類市場機率 ----

// PolyMarket 是一個預測市場的當下「是」機率。
type PolyMarket struct {
	ID       string
	Question string
	Slug     string
	Yes      float64 // 0..1
	Vol24h   float64
}

// Polymarket 抓 24h 成交量最大的加密類市場與「是」的機率(Gamma 公開接口)。
func (c *Client) Polymarket(limit int) ([]PolyMarket, error) {
	var raw []struct {
		ID            string  `json:"id"`
		Question      string  `json:"question"`
		Slug          string  `json:"slug"`
		OutcomePrices string  `json:"outcomePrices"`
		Volume24hr    float64 `json:"volume24hr"`
	}
	url := fmt.Sprintf("https://gamma-api.polymarket.com/markets?tag_slug=crypto&active=true&closed=false&order=volume24hr&ascending=false&limit=%d", limit)
	if err := c.getJSON(url, nil, &raw); err != nil {
		return nil, err
	}
	var out []PolyMarket
	for _, m := range raw {
		var prices []string
		if json.Unmarshal([]byte(m.OutcomePrices), &prices) != nil || len(prices) == 0 {
			continue
		}
		yes, err := strconv.ParseFloat(prices[0], 64)
		if err != nil {
			continue
		}
		out = append(out, PolyMarket{ID: m.ID, Question: m.Question, Slug: m.Slug, Yes: yes, Vol24h: m.Volume24hr})
	}
	return out, nil
}

// ---- Upbit 韓元行情(韓國溢價)----

// UpbitKRWPrices 回傳 Upbit 所有韓元市場的最新成交價(幣種 → KRW),含 USDT(拿來當匯率)。
func (c *Client) UpbitKRWPrices() (map[string]float64, error) {
	var markets []struct {
		Market string `json:"market"`
	}
	if err := c.getJSON("https://api.upbit.com/v1/market/all?isDetails=false", nil, &markets); err != nil {
		return nil, err
	}
	var krw []string
	for _, m := range markets {
		if strings.HasPrefix(m.Market, "KRW-") {
			krw = append(krw, m.Market)
		}
	}
	out := map[string]float64{}
	for i := 0; i < len(krw); i += 100 { // 一次最多查 100 個市場
		j := i + 100
		if j > len(krw) {
			j = len(krw)
		}
		var ts []struct {
			Market     string  `json:"market"`
			TradePrice float64 `json:"trade_price"`
		}
		if err := c.getJSON("https://api.upbit.com/v1/ticker?markets="+strings.Join(krw[i:j], ","), nil, &ts); err != nil {
			return nil, err
		}
		for _, t := range ts {
			out[strings.TrimPrefix(t.Market, "KRW-")] = t.TradePrice
		}
	}
	if out["USDT"] <= 0 {
		return out, fmt.Errorf("Upbit 沒有 KRW-USDT 價格,算不出匯率")
	}
	return out, nil
}
