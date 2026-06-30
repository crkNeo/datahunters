package exchange

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// YahooQuote is a normalised index/futures quote (price + prior close).
type YahooQuote struct {
	Symbol    string
	Price     float64
	PrevClose float64
}

// ChgPct is the % change from the prior session close.
func (q YahooQuote) ChgPct() float64 {
	if q.PrevClose == 0 {
		return 0
	}
	return (q.Price - q.PrevClose) / q.PrevClose * 100
}

// YahooQuote fetches a single symbol via Yahoo Finance's public chart endpoint.
// A browser User-Agent is required — Yahoo rejects Go's default UA.
func (c *Client) YahooQuote(symbol string) (YahooQuote, error) {
	url := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?range=1d&interval=1d", symbol)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return YahooQuote{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return YahooQuote{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return YahooQuote{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return YahooQuote{}, fmt.Errorf("yahoo status %d", resp.StatusCode)
	}
	var out struct {
		Chart struct {
			Result []struct {
				Meta struct {
					RegularMarketPrice float64 `json:"regularMarketPrice"`
					ChartPreviousClose float64 `json:"chartPreviousClose"`
					PreviousClose      float64 `json:"previousClose"`
				} `json:"meta"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return YahooQuote{}, err
	}
	if len(out.Chart.Result) == 0 {
		return YahooQuote{}, fmt.Errorf("yahoo: no data for %s", symbol)
	}
	m := out.Chart.Result[0].Meta
	pc := m.ChartPreviousClose
	if pc == 0 {
		pc = m.PreviousClose
	}
	return YahooQuote{Symbol: symbol, Price: m.RegularMarketPrice, PrevClose: pc}, nil
}
