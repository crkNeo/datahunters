package exchange

import "fmt"

// 指定時間點的歷史查詢(補資料工具 cmd/pulsartag 用)。Binance 的 OI / 多空比歷史只保留約 30 天。
// endMs 為「含」上限:只回傳時間戳 ≤ endMs 的資料點。

// BinanceKlinesRange 回傳 [startMs, endMs] 內的永續 K 線(舊→新)。startMs/endMs 為 0 表示不限。
func (c *Client) BinanceKlinesRange(symbol, interval string, startMs, endMs int64, limit int) ([]Candle, error) {
	url := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=%s&limit=%d", binanceFapi, symbol, interval, limit)
	if startMs > 0 {
		url += fmt.Sprintf("&startTime=%d", startMs)
	}
	if endMs > 0 {
		url += fmt.Sprintf("&endTime=%d", endMs)
	}
	var raw []binanceKline
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	return parseKlines(raw), nil
}

// BinanceOIHistAt 回傳截至 endMs 為止最近 limit 個 OI 歷史點(舊→新)。
func (c *Client) BinanceOIHistAt(symbol, period string, endMs int64, limit int) ([]OIPoint, error) {
	url := fmt.Sprintf("%s/futures/data/openInterestHist?symbol=%s&period=%s&limit=%d&endTime=%d",
		binanceFapi, symbol, period, limit, endMs)
	var raw []struct {
		SumOpenInterest      string `json:"sumOpenInterest"`
		SumOpenInterestValue string `json:"sumOpenInterestValue"`
		Timestamp            int64  `json:"timestamp"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := make([]OIPoint, 0, len(raw))
	for _, p := range raw {
		out = append(out, OIPoint{Ts: p.Timestamp, SumOI: atof(p.SumOpenInterest), SumOIValue: atof(p.SumOpenInterestValue)})
	}
	return out, nil
}

// BinanceLongShortAt 回傳截至 endMs 為止最近一筆散戶多空帳戶比。
func (c *Client) BinanceLongShortAt(symbol, period string, endMs int64) (LSPoint, error) {
	pts, err := c.lsHist(fmt.Sprintf("%s/futures/data/globalLongShortAccountRatio?symbol=%s&period=%s&limit=1&endTime=%d",
		binanceFapi, symbol, period, endMs))
	if err != nil {
		return LSPoint{}, err
	}
	if len(pts) == 0 {
		return LSPoint{}, fmt.Errorf("binance long/short empty")
	}
	return pts[len(pts)-1], nil
}

// BinanceFundingAt 回傳 endMs 之前最近一次「已結算」的資金費率。
func (c *Client) BinanceFundingAt(symbol string, endMs int64) (FundingPoint, error) {
	url := fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&limit=1&endTime=%d", binanceFapi, symbol, endMs)
	var raw []struct {
		FundingTime int64  `json:"fundingTime"`
		FundingRate string `json:"fundingRate"`
	}
	if err := c.get(url, &raw); err != nil {
		return FundingPoint{}, err
	}
	if len(raw) == 0 {
		return FundingPoint{}, fmt.Errorf("binance funding empty")
	}
	p := raw[len(raw)-1]
	return FundingPoint{Ts: p.FundingTime, Rate: atof(p.FundingRate)}, nil
}
