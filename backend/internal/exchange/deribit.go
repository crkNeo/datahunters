package exchange

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DVOLPoint is one Deribit volatility-index (DVOL) bar (close only).
type DVOLPoint struct {
	Ts    int64   // ms
	Value float64 // DVOL: annualised implied volatility, %
}

// DeribitDVOL fetches the DVOL implied-volatility index history for a currency
// ("BTC" or "ETH") between start/end (ms) at the given resolution (seconds).
// DVOL is the market's forward-looking volatility expectation, unlike ATR.
func (c *Client) DeribitDVOL(currency string, startMs, endMs int64, resSec int) ([]DVOLPoint, error) {
	url := fmt.Sprintf("https://www.deribit.com/api/v2/public/get_volatility_index_data?currency=%s&start_timestamp=%d&end_timestamp=%d&resolution=%d",
		currency, startMs, endMs, resSec)
	var out struct {
		Result struct {
			Data [][]float64 `json:"data"` // [ts, open, high, low, close]
		} `json:"result"`
	}
	if err := c.get(url, &out); err != nil {
		return nil, err
	}
	pts := make([]DVOLPoint, 0, len(out.Result.Data))
	for _, row := range out.Result.Data {
		if len(row) < 5 {
			continue
		}
		pts = append(pts, DVOLPoint{Ts: int64(row[0]), Value: row[4]})
	}
	return pts, nil
}

// OptionQuote is one parsed Deribit option summary row.
type OptionQuote struct {
	Strike       float64
	IsCall       bool
	ExpiryMs     int64
	ExpiryLabel  string  // e.g. "31JUL26"
	MarkIV       float64 // %, annualised
	OpenInterest float64 // contracts (coin units)
	Volume       float64
	Underlying   float64
}

var deribitMonths = map[string]time.Month{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

// parseDeribitExpiry parses "31JUL26" → unix ms (08:00 UTC expiry).
func parseDeribitExpiry(s string) (int64, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i+3 > len(s) {
		return 0, false
	}
	day, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, false
	}
	mon, ok := deribitMonths[s[i:i+3]]
	if !ok {
		return 0, false
	}
	yr, err := strconv.Atoi(s[i+3:])
	if err != nil {
		return 0, false
	}
	return time.Date(2000+yr, mon, day, 8, 0, 0, 0, time.UTC).UnixMilli(), true
}

// DeribitOptions fetches and parses the current option chain for a currency
// ("BTC"/"ETH") via the public book-summary endpoint (no auth, no history).
func (c *Client) DeribitOptions(currency string) ([]OptionQuote, error) {
	url := fmt.Sprintf("https://www.deribit.com/api/v2/public/get_book_summary_by_currency?currency=%s&kind=option", currency)
	var out struct {
		Result []struct {
			InstrumentName  string  `json:"instrument_name"`
			MarkIV          float64 `json:"mark_iv"`
			OpenInterest    float64 `json:"open_interest"`
			Volume          float64 `json:"volume"`
			UnderlyingPrice float64 `json:"underlying_price"`
		} `json:"result"`
	}
	if err := c.get(url, &out); err != nil {
		return nil, err
	}
	quotes := make([]OptionQuote, 0, len(out.Result))
	for _, r := range out.Result {
		parts := strings.Split(r.InstrumentName, "-") // BTC-31JUL26-69000-C
		if len(parts) != 4 {
			continue
		}
		expMs, ok := parseDeribitExpiry(parts[1])
		if !ok {
			continue
		}
		strike, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			continue
		}
		quotes = append(quotes, OptionQuote{
			Strike:       strike,
			IsCall:       parts[3] == "C",
			ExpiryMs:     expMs,
			ExpiryLabel:  parts[1],
			MarkIV:       r.MarkIV,
			OpenInterest: r.OpenInterest,
			Volume:       r.Volume,
			Underlying:   r.UnderlyingPrice,
		})
	}
	return quotes, nil
}
