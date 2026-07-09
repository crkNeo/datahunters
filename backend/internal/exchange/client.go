package exchange

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Binance IP-rate-limit guards. All Binance REST calls funnel through get(),
// which routes each request onto a "lane" — a distinct outbound source IP (the
// direct connection, plus any proxies in BINANCE_PROXIES). Each lane enforces,
// PER IP:
//  1. pacing        — a minimum gap between requests, so cold-start bursts can't
//     spike the per-minute weight;
//  2. circuit breaker — on 418/429 the lane's ban-until is remembered and it's
//     skipped (rotate to the next lane) until it clears, so we never extend a
//     ban by hammering, and one banned IP doesn't take the whole app down;
//  3. weight watch  — X-MBX-USED-WEIGHT-1M (limit 2400/min per IP); past a soft
//     cap the lane is paused until the next minute window.
const (
	binMinGap        = 100 * time.Millisecond // ≥100ms between requests on ONE lane
	binSoftWeightCap = 1200                   // pause a lane past this used weight (per IP)
)

var bannedUntilRe = regexp.MustCompile(`banned until (\d{10,})`)

// nextMinute is the start of the next clock minute — where Binance's 1-minute
// used-weight window resets. We hold until then (not a short Retry-After) so a
// 429 can't escalate to a long 418 by resuming while the weight is still maxed.
func nextMinute(t time.Time) time.Time { return t.Truncate(time.Minute).Add(time.Minute) }

// lane is one outbound path for Binance requests — a source IP (direct or a
// proxy). Each has its OWN per-minute weight budget and ban state, so when one
// IP is rate-limit-banned we simply fail over to the next lane.
type lane struct {
	name     string
	http     *http.Client
	mu       sync.Mutex
	last     time.Time // pacing
	banUntil time.Time // 418/429 circuit breaker (per IP)
	pauseTo  time.Time // soft-weight backoff (per IP)
}

// Client wraps public market-data calls to OKX and Binance.
// All endpoints used here are public and require no authentication.
type Client struct {
	http  *http.Client // direct client — OKX / Yahoo / etc. (non-Binance)
	lanes []*lane      // Binance request lanes; lanes[0] is the direct connection
}

func NewClient() *Client {
	direct := &http.Client{Timeout: 10 * time.Second}
	c := &Client{http: direct, lanes: []*lane{{name: "direct", http: direct}}}
	// extra source IPs via proxies (HTTP or SOCKS5), comma-separated:
	//   BINANCE_PROXIES=http://user:pass@ip1:port,socks5://ip2:port
	if env := strings.TrimSpace(os.Getenv("BINANCE_PROXIES")); env != "" {
		for _, p := range strings.Split(env, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			pu, err := url.Parse(p)
			if err != nil {
				log.Printf("binance proxy skipped (bad url %q): %v", p, err)
				continue
			}
			c.lanes = append(c.lanes, &lane{
				name: pu.Host,
				http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}},
			})
		}
	}
	if len(c.lanes) > 1 {
		log.Printf("binance: %d lanes (1 direct + %d proxy) — rotates to the next IP on a ban", len(c.lanes), len(c.lanes)-1)
	}
	return c
}

func isBinance(url string) bool { return strings.Contains(url, "binance.com") }

// pickBinanceLane returns the first lane available now (not banned, not weight-
// paused), reserving its pacing slot. Fails fast if EVERY lane is down.
func (c *Client) pickBinanceLane() (*lane, error) {
	now := time.Now()
	var soonest time.Time
	for _, ln := range c.lanes {
		ln.mu.Lock()
		if now.Before(ln.banUntil) || now.Before(ln.pauseTo) {
			t := ln.banUntil
			if ln.pauseTo.After(t) {
				t = ln.pauseTo
			}
			if soonest.IsZero() || t.Before(soonest) {
				soonest = t
			}
			ln.mu.Unlock()
			continue
		}
		next := ln.last.Add(binMinGap)
		if next.Before(now) {
			next = now
		}
		ln.last = next
		ln.mu.Unlock()
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		return ln, nil
	}
	return nil, fmt.Errorf("binance: all %d lanes rate-limited (next free in %s)",
		len(c.lanes), time.Until(soonest).Round(time.Second))
}

// AllBanned reports whether EVERY Binance lane is currently ban/weight-paused
// (i.e. Binance is effectively unusable), and how long until the soonest frees up.
func (c *Client) AllBanned() (bool, time.Duration) {
	now := time.Now()
	var soonest time.Time
	for _, ln := range c.lanes {
		ln.mu.Lock()
		blocked := now.Before(ln.banUntil) || now.Before(ln.pauseTo)
		t := ln.banUntil
		if ln.pauseTo.After(t) {
			t = ln.pauseTo
		}
		ln.mu.Unlock()
		if !blocked {
			return false, 0 // at least one lane is free
		}
		if soonest.IsZero() || t.Before(soonest) {
			soonest = t
		}
	}
	return len(c.lanes) > 0, time.Until(soonest)
}

// observeBinance updates one lane's ban / weight state from its response.
func (c *Client) observeBinance(ln *lane, resp *http.Response, body []byte) {
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 418 {
		until := nextMinute(time.Now())
		if m := bannedUntilRe.FindSubmatch(body); m != nil {
			if ms, err := strconv.ParseInt(string(m[1]), 10, 64); err == nil {
				if t := time.UnixMilli(ms); t.After(until) {
					until = t
				}
			}
		}
		ln.mu.Lock()
		if until.After(ln.banUntil) {
			ln.banUntil = until
			log.Printf("binance[%s]: rate-limited (HTTP %d) — lane down until %s, rotating to next lane",
				ln.name, resp.StatusCode, until.Format("15:04:05"))
		}
		ln.mu.Unlock()
		return
	}
	if w := resp.Header.Get("X-Mbx-Used-Weight-1m"); w != "" {
		if used, err := strconv.Atoi(w); err == nil && used >= binSoftWeightCap {
			pause := nextMinute(time.Now())
			ln.mu.Lock()
			if pause.After(ln.pauseTo) {
				ln.pauseTo = pause
				log.Printf("binance[%s]: used weight %d/2400 ≥ soft cap %d — lane paused until %s",
					ln.name, used, binSoftWeightCap, pause.Format("15:04:05"))
			}
			ln.mu.Unlock()
		}
	}
}

func (c *Client) get(url string, out any) error {
	client := c.http
	var ln *lane
	if isBinance(url) {
		var err error
		if ln, err = c.pickBinanceLane(); err != nil {
			return err
		}
		client = ln.http
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if ln != nil {
		c.observeBinance(ln, resp, body)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// ---------- OKX ----------

const okxBase = "https://www.okx.com"

type okxEnvelope struct {
	Code string     `json:"code"`
	Msg  string     `json:"msg"`
	Data [][]string `json:"data"`
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
		InstID          string `json:"instId"`
		FundingRate     string `json:"fundingRate"`
		NextFundingTime string `json:"nextFundingTime"`
	} `json:"data"`
}

// OKXFundingRate returns the current funding rate for a SWAP instrument.
func (c *Client) OKXFundingRate(instID string) (float64, error) {
	rate, _, err := c.OKXFundingInfo(instID)
	return rate, err
}

// OKXFundingInfo returns the current funding rate + next funding time (ms) for a
// SWAP instrument, e.g. "BTC-USDT-SWAP".
func (c *Client) OKXFundingInfo(instID string) (rate float64, nextMs int64, err error) {
	url := fmt.Sprintf("%s/api/v5/public/funding-rate?instId=%s", okxBase, instID)
	var env okxFundingEnvelope
	if err = c.get(url, &env); err != nil {
		return
	}
	if env.Code != "0" || len(env.Data) == 0 {
		return 0, 0, fmt.Errorf("okx funding empty")
	}
	rate = atof(env.Data[0].FundingRate)
	nextMs, _ = strconv.ParseInt(env.Data[0].NextFundingTime, 10, 64)
	return
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

// BinanceAllFunding returns the latest funding rate for every USDT perp in a
// single call (premiumIndex), keyed by coin (e.g. "BTC" -> 0.0001).
func (c *Client) BinanceAllFunding() (map[string]float64, error) {
	var raw []struct {
		Symbol          string `json:"symbol"`
		LastFundingRate string `json:"lastFundingRate"`
	}
	if err := c.get(binanceFapi+"/fapi/v1/premiumIndex", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(raw))
	for _, p := range raw {
		if strings.HasSuffix(p.Symbol, "USDT") && !strings.Contains(p.Symbol, "_") {
			out[strings.TrimSuffix(p.Symbol, "USDT")] = atof(p.LastFundingRate)
		}
	}
	return out, nil
}

// BinanceSymbolTypes returns coin -> underlyingType ("COIN" for crypto,
// "EQUITY"/"COMMODITY"/"INDEX"/… for tokenized stocks etc.) for USDT perps,
// so the radar can cleanly separate crypto from tokenized equities.
func (c *Client) BinanceSymbolTypes() (map[string]string, error) {
	var raw struct {
		Symbols []struct {
			Symbol         string `json:"symbol"`
			UnderlyingType string `json:"underlyingType"`
		} `json:"symbols"`
	}
	if err := c.get(binanceFapi+"/fapi/v1/exchangeInfo", &raw); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw.Symbols))
	for _, s := range raw.Symbols {
		if strings.HasSuffix(s.Symbol, "USDT") && !strings.Contains(s.Symbol, "_") {
			out[strings.TrimSuffix(s.Symbol, "USDT")] = s.UnderlyingType
		}
	}
	return out, nil
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
// BinanceAllPrices returns the last price for every futures symbol in one
// cheap call (/fapi/v1/ticker/price, weight 2 — vs 40 for the full 24h stats).
// Use it when only prices are needed (e.g. the paper-book tick).
func (c *Client) BinanceAllPrices() (map[string]float64, error) {
	url := fmt.Sprintf("%s/fapi/v1/ticker/price", binanceFapi)
	var raw []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := c.get(url, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(raw))
	for _, r := range raw {
		out[r.Symbol] = atof(r.Price)
	}
	return out, nil
}

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
