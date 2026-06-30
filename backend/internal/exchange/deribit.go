package exchange

import "fmt"

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
