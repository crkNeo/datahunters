package exchange

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client wraps public market-data calls to OKX and Binance.
// All endpoints used here are public and require no authentication.
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) get(url string, out any) error {
	resp, err := c.http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// ---------- OKX ----------

const okxBase = "https://www.okx.com"

type okxEnvelope struct {
	Code string            `json:"code"`
	Msg  string            `json:"msg"`
	Data [][]string        `json:"data"`
}

// Candle is a normalised OHLCV bar.
type Candle struct {
	Ts       int64
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64 // base volume
	TakerBuy float64 // taker (aggressive) buy base volume; 0 if unavailable (OKX)
	QuoteVol float64 // quote (USDT) volume; 0 if unavailable (OKX)
	Trades   float64 // number of trades in the bar; 0 if unavailable (OKX)
}

// OKXCandles fetches OHLCV for a SWAP instrument, e.g. "BTC-USDT-SWAP".
func (c *Client) OKXCandles(instID, bar string, limit int) ([]Candle, error) {
	url := fmt.Sprintf("%s/api/v5/market/candles?instId=%s&bar=%s&limit=%d",
		okxBase, instID, bar, limit)
	var env okxEnvelope
	if err := c.get(url, &env); err != nil {
		return nil, err
	}
	if env.Code != "0" {
		return nil, fmt.Errorf("okx error: %s", env.Msg)
	}
	out := make([]Candle, 0, len(env.Data))
	for _, row := range env.Data {
		if len(row) < 6 {
			continue
		}
		ts, _ := strconv.ParseInt(row[0], 10, 64)
		out = append(out, Candle{
			Ts:     ts,
			Open:   atof(row[1]),
			High:   atof(row[2]),
			Low:    atof(row[3]),
			Close:  atof(row[4]),
			Volume: atof(row[5]),
		})
	}
	return out, nil
}

type okxFundingEnvelope struct {
	Code string `json:"code"`
	Data []struct {
		InstID      string `json:"instId"`
		FundingRate string `json:"fundingRate"`
	} `json:"data"`
}

// OKXFundingRate returns the current funding rate for a SWAP instrument.
func (c *Client) OKXFundingRate(instID string) (float64, error) {
	url := fmt.Sprintf("%s/api/v5/public/funding-rate?instId=%s", okxBase, instID)
	var env okxFundingEnvelope
	if err := c.get(url, &env); err != nil {
		return 0, err
	}
	if env.Code != "0" || len(env.Data) == 0 {
		return 0, fmt.Errorf("okx funding empty")
	}
	return atof(env.Data[0].FundingRate), nil
}

type okxOIEnvelope struct {
	Code string `json:"code"`
	Data []struct {
		InstID string `json:"instId"`
		OI     string `json:"oi"`
		OICcy  string `json:"oiCcy"`
	} `json:"data"`
}

// OKXOpenInterest returns the current open interest (in contracts and ccy).
func (c *Client) OKXOpenInterest(instID string) (float64, error) {
	url := fmt.Sprintf("%s/api/v5/public/open-interest?instId=%s", okxBase, instID)
	var env okxOIEnvelope
	if err := c.get(url, &env); err != nil {
		return 0, err
	}
	if env.Code != "0" || len(env.Data) == 0 {
		return 0, fmt.Errorf("okx oi empty")
	}
	return atof(env.Data[0].OICcy), nil
}

// ---------- Binance ----------

const binanceFapi = "https://fapi.binance.com"
const binanceSpot = "https://api.binance.com"

type binanceKline []any

// parseKlines normalises Binance kline arrays (same format for spot & futures).
func parseKlines(raw []binanceKline) []Candle {
	out := make([]Candle, 0, len(raw))
	for _, k := range raw {
		if len(k) < 6 {
			continue
		}
		c := Candle{
			Ts:     int64(toFloat(k[0])),
			Open:   toFloat(k[1]),
			High:   toFloat(k[2]),
			Low:    toFloat(k[3]),
			Close:  toFloat(k[4]),
			Volume: toFloat(k[5]),
		}
		if len(k) > 7 { // index 7 = quote (USDT) volume
			c.QuoteVol = toFloat(k[7])
		}
		if len(k) > 8 { // index 8 = number of trades
			c.Trades = toFloat(k[8])
		}
		if len(k) > 9 { // index 9 = taker buy base volume
			c.TakerBuy = toFloat(k[9])
		}
		out = append(out, c)
	}
	return out
}

// BinanceKlines fetches USDT-M perpetual klines, e.g. symbol "BTCUSDT".
func (c *Client) BinanceKlines(symbol, interval string, limit int) ([]Candle, error) {
	url := fmt.Sprintf("%s/fapi/v1/klines?symbol=%s&interval=%s&limit=%d",
		binanceFapi, symbol, interval, limit)
	var raw []binanceKline
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	return parseKlines(raw), nil
}

// BinanceSpotKlines fetches SPOT klines from the spot API, e.g. "BTCUSDT".
// Same array format as futures, so it carries taker-buy volume too.
func (c *Client) BinanceSpotKlines(symbol, interval string, limit int) ([]Candle, error) {
	url := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=%s&limit=%d",
		binanceSpot, symbol, interval, limit)
	var raw []binanceKline
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	return parseKlines(raw), nil
}

type binanceOI struct {
	OpenInterest string `json:"openInterest"`
	Symbol       string `json:"symbol"`
}

// BinanceOpenInterest returns current OI for a USDT-M perpetual.
func (c *Client) BinanceOpenInterest(symbol string) (float64, error) {
	url := fmt.Sprintf("%s/fapi/v1/openInterest?symbol=%s", binanceFapi, symbol)
	var oi binanceOI
	if err := c.get(url, &oi); err != nil {
		return 0, err
	}
	return atof(oi.OpenInterest), nil
}

// BinanceAggTrade is one aggregated trade, used for CVD.
type BinanceAggTrade struct {
	Price        string `json:"p"`
	Qty          string `json:"q"`
	IsBuyerMaker bool   `json:"m"`
}

// BinanceAggTrades pulls recent aggregated trades (max 1000) for CVD calc.
func (c *Client) BinanceAggTrades(symbol string, limit int) ([]BinanceAggTrade, error) {
	if limit > 1000 {
		limit = 1000
	}
	url := fmt.Sprintf("%s/fapi/v1/aggTrades?symbol=%s&limit=%d", binanceFapi, symbol, limit)
	var trades []BinanceAggTrade
	if err := c.get(url, &trades); err != nil {
		return nil, err
	}
	return trades, nil
}

// Ticker24h is the normalised subset of the 24h rolling window we use.
type Ticker24h struct {
	ChangePct   float64 // priceChangePercent
	LastPrice   float64
	QuoteVolume float64 // 24h notional turnover in USDT
}

// Binance24h fetches the 24h rolling ticker for a USDT-M perpetual.
func (c *Client) Binance24h(symbol string) (Ticker24h, error) {
	url := fmt.Sprintf("%s/fapi/v1/ticker/24hr?symbol=%s", binanceFapi, symbol)
	var raw struct {
		PriceChangePercent string `json:"priceChangePercent"`
		LastPrice          string `json:"lastPrice"`
		QuoteVolume        string `json:"quoteVolume"`
	}
	if err := c.get(url, &raw); err != nil {
		return Ticker24h{}, err
	}
	return Ticker24h{
		ChangePct:   atof(raw.PriceChangePercent),
		LastPrice:   atof(raw.LastPrice),
		QuoteVolume: atof(raw.QuoteVolume),
	}, nil
}

// FundingPoint is one historical funding-rate sample.
type FundingPoint struct {
	Ts   int64 // fundingTime, ms
	Rate float64
}

// BinanceFundingHist returns historical funding rates (oldest..newest), every 8h.
func (c *Client) BinanceFundingHist(symbol string, limit int) ([]FundingPoint, error) {
	url := fmt.Sprintf("%s/fapi/v1/fundingRate?symbol=%s&limit=%d", binanceFapi, symbol, limit)
	var raw []struct {
		FundingTime int64  `json:"fundingTime"`
		FundingRate string `json:"fundingRate"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := make([]FundingPoint, 0, len(raw))
	for _, p := range raw {
		out = append(out, FundingPoint{Ts: p.FundingTime, Rate: atof(p.FundingRate)})
	}
	return out, nil
}

// LSPoint is one historical long/short account-ratio sample.
type LSPoint struct {
	Ts          int64
	LongAccount float64
}

func (c *Client) lsHist(url string) ([]LSPoint, error) {
	var raw []struct {
		LongAccount string `json:"longAccount"`
		Timestamp   int64  `json:"timestamp"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := make([]LSPoint, 0, len(raw))
	for _, p := range raw {
		out = append(out, LSPoint{Ts: p.Timestamp, LongAccount: atof(p.LongAccount)})
	}
	return out, nil
}

// BinanceLongShortHist returns historical global (retail) long/short account
// ratios (oldest..newest). period e.g. "1h"; Binance retains ~30 days.
func (c *Client) BinanceLongShortHist(symbol, period string, limit int) ([]LSPoint, error) {
	return c.lsHist(fmt.Sprintf("%s/futures/data/globalLongShortAccountRatio?symbol=%s&period=%s&limit=%d",
		binanceFapi, symbol, period, limit))
}

// BinanceTopPositionHist returns top-trader long/short ratio by position size
// (smart-money positioning), oldest..newest.
func (c *Client) BinanceTopPositionHist(symbol, period string, limit int) ([]LSPoint, error) {
	return c.lsHist(fmt.Sprintf("%s/futures/data/topLongShortPositionRatio?symbol=%s&period=%s&limit=%d",
		binanceFapi, symbol, period, limit))
}

// PremiumPoint is one perpetual-premium (basis) sample.
type PremiumPoint struct {
	Ts      int64
	Premium float64 // premium index (perp vs index), e.g. 0.0003
}

// BinancePremiumKlines returns historical premium-index klines (basis),
// oldest..newest; the bar close is used as the premium value.
func (c *Client) BinancePremiumKlines(symbol, interval string, limit int) ([]PremiumPoint, error) {
	url := fmt.Sprintf("%s/fapi/v1/premiumIndexKlines?symbol=%s&interval=%s&limit=%d",
		binanceFapi, symbol, interval, limit)
	var raw [][]any
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := make([]PremiumPoint, 0, len(raw))
	for _, k := range raw {
		if len(k) < 5 {
			continue
		}
		out = append(out, PremiumPoint{Ts: int64(toFloat(k[0])), Premium: toFloat(k[4])})
	}
	return out, nil
}

// MarketTicker is the normalised 24h ticker for one contract.
type MarketTicker struct {
	Symbol   string
	Price    float64
	ChgPct   float64
	QuoteVol float64 // 24h notional turnover, USDT
}

// BinanceAllTickers fetches the 24h ticker for every contract in one call and
// returns only USDT-margined perpetuals (excludes dated futures like *_240927).
func (c *Client) BinanceAllTickers() ([]MarketTicker, error) {
	url := fmt.Sprintf("%s/fapi/v1/ticker/24hr", binanceFapi)
	var raw []struct {
		Symbol             string `json:"symbol"`
		LastPrice          string `json:"lastPrice"`
		PriceChangePercent string `json:"priceChangePercent"`
		QuoteVolume        string `json:"quoteVolume"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := make([]MarketTicker, 0, len(raw))
	for _, t := range raw {
		if !strings.HasSuffix(t.Symbol, "USDT") || strings.Contains(t.Symbol, "_") {
			continue
		}
		out = append(out, MarketTicker{
			Symbol:   t.Symbol,
			Price:    atof(t.LastPrice),
			ChgPct:   atof(t.PriceChangePercent),
			QuoteVol: atof(t.QuoteVolume),
		})
	}
	return out, nil
}

// LongShort holds the global long/short *account* ratio (retail positioning).
type LongShort struct {
	Ratio        float64 // long/short
	LongAccount  float64 // fraction 0..1
	ShortAccount float64 // fraction 0..1
}

// BinanceLongShort returns the most recent global long/short account ratio.
func (c *Client) BinanceLongShort(symbol, period string) (LongShort, error) {
	url := fmt.Sprintf("%s/futures/data/globalLongShortAccountRatio?symbol=%s&period=%s&limit=1",
		binanceFapi, symbol, period)
	var raw []struct {
		LongShortRatio string `json:"longShortRatio"`
		LongAccount    string `json:"longAccount"`
		ShortAccount   string `json:"shortAccount"`
	}
	if err := c.get(url, &raw); err != nil {
		return LongShort{}, err
	}
	if len(raw) == 0 {
		return LongShort{}, fmt.Errorf("binance long/short empty")
	}
	r := raw[len(raw)-1]
	return LongShort{
		Ratio:        atof(r.LongShortRatio),
		LongAccount:  atof(r.LongAccount),
		ShortAccount: atof(r.ShortAccount),
	}, nil
}

// OIPoint is one open-interest history sample.
type OIPoint struct {
	Ts         int64
	SumOI      float64 // contracts / base units
	SumOIValue float64 // USDT notional
}

// BinanceOIHist returns open-interest history oldest..newest for a perpetual.
// period is e.g. "5m", "1h"; max limit 500.
func (c *Client) BinanceOIHist(symbol, period string, limit int) ([]OIPoint, error) {
	url := fmt.Sprintf("%s/futures/data/openInterestHist?symbol=%s&period=%s&limit=%d",
		binanceFapi, symbol, period, limit)
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
		out = append(out, OIPoint{
			Ts:         p.Timestamp,
			SumOI:      atof(p.SumOpenInterest),
			SumOIValue: atof(p.SumOpenInterestValue),
		})
	}
	return out, nil
}

// ---------- helpers ----------

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		return atof(x)
	default:
		return 0
	}
}
