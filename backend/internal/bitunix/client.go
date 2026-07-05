// Package bitunix is a minimal Bitunix USDT-M futures REST client used to mirror
// strategy signals onto a real account. Signing is Bitunix's double-SHA256
// scheme; the high-level Open() sizes a MARKET order and attaches TP/SL so the
// exchange manages the exit.
package bitunix

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const host = "https://fapi.bitunix.com"

// Client holds one account's credentials.
type Client struct {
	key, secret string
	http        *http.Client
}

func New(key, secret string) *Client {
	return &Client{key: key, secret: secret, http: &http.Client{Timeout: 15 * time.Second}}
}

func sha256Hex(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func nonce() string             { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }
func atof(s string) float64     { f, _ := strconv.ParseFloat(s, 64); return f }

// floorTo rounds x DOWN to n decimals (never over-sizes an order).
func floorTo(x float64, n int) float64 { f := math.Pow10(n); return math.Floor(x*f) / f }

// do performs a request. signed=true adds Bitunix auth headers. query is signed
// as sorted key+value with NO separators ("marginCoinUSDT"); body is compact JSON.
func (c *Client) do(method, path string, query map[string]string, bodyObj any, signed bool) ([]byte, error) {
	var bodyStr string
	if bodyObj != nil {
		b, err := json.Marshal(bodyObj)
		if err != nil {
			return nil, err
		}
		bodyStr = string(b)
	}
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var signQuery, urlQuery strings.Builder
	for i, k := range keys {
		signQuery.WriteString(k)
		signQuery.WriteString(query[k])
		if i > 0 {
			urlQuery.WriteByte('&')
		}
		urlQuery.WriteString(url.QueryEscape(k) + "=" + url.QueryEscape(query[k]))
	}
	u := host + path
	if urlQuery.Len() > 0 {
		u += "?" + urlQuery.String()
	}
	var body io.Reader
	if bodyStr != "" {
		body = strings.NewReader(bodyStr)
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if signed {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		n := nonce()
		digest := sha256Hex(n + ts + c.key + signQuery.String() + bodyStr)
		req.Header.Set("api-key", c.key)
		req.Header.Set("nonce", n)
		req.Header.Set("timestamp", ts)
		req.Header.Set("sign", sha256Hex(digest+c.secret))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return raw, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

type codeMsg struct {
	Code json.Number `json:"code"`
	Msg  string      `json:"msg"`
}

func (c codeMsg) ok() bool { return c.Code.String() == "0" }

type pairData struct {
	Symbol         string `json:"symbol"`
	BasePrecision  int    `json:"basePrecision"`
	QuotePrecision int    `json:"quotePrecision"`
	MinTradeVolume string `json:"minTradeVolume"`
	MaxLeverage    int    `json:"maxLeverage"`
	MinLeverage    int    `json:"minLeverage"`
}

type acctData struct {
	MarginCoin   string `json:"marginCoin"`
	Available    string `json:"available"`
	PositionMode string `json:"positionMode"` // ONE_WAY | HEDGE
}

type tickerData struct {
	Symbol    string `json:"symbol"`
	LastPrice string `json:"lastPrice"`
	MarkPrice string `json:"markPrice"`
}

func (c *Client) tradingPair(symbol string) (pairData, error) {
	raw, err := c.do(http.MethodGet, "/api/v1/futures/market/trading_pairs", nil, nil, false)
	if err != nil {
		return pairData{}, err
	}
	var r struct {
		codeMsg
		Data []pairData `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return pairData{}, fmt.Errorf("decode trading_pairs: %v", err)
	}
	for _, p := range r.Data {
		if strings.EqualFold(p.Symbol, symbol) {
			return p, nil
		}
	}
	return pairData{}, fmt.Errorf("symbol %s not tradable on Bitunix", symbol)
}

func (c *Client) markPrice(symbol string) (float64, error) {
	raw, err := c.do(http.MethodGet, "/api/v1/futures/market/tickers", map[string]string{"symbols": symbol}, nil, false)
	if err != nil {
		return 0, err
	}
	var r struct {
		codeMsg
		Data []tickerData `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, err
	}
	for _, t := range r.Data {
		if strings.EqualFold(t.Symbol, symbol) {
			if p := atof(t.MarkPrice); p > 0 {
				return p, nil
			}
			return atof(t.LastPrice), nil
		}
	}
	return 0, fmt.Errorf("no price for %s", symbol)
}

// Account returns the available balance + position mode (signed request), used
// for sizing and for a "test connection" check on the settings page later.
func (c *Client) Account(marginCoin string) (available float64, positionMode string, err error) {
	raw, err := c.do(http.MethodGet, "/api/v1/futures/account", map[string]string{"marginCoin": marginCoin}, nil, true)
	if err != nil {
		return 0, "", err
	}
	var r struct {
		codeMsg
		Data acctData `json:"data"` // Bitunix returns a single object here, not an array
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, "", err
	}
	if !r.ok() {
		return 0, "", fmt.Errorf("account code=%s msg=%s", r.Code, r.Msg)
	}
	return atof(r.Data.Available), r.Data.PositionMode, nil
}

func (c *Client) setLeverage(symbol, marginCoin string, lev int) error {
	raw, err := c.do(http.MethodPost, "/api/v1/futures/account/change_leverage", nil,
		map[string]any{"symbol": symbol, "leverage": lev, "marginCoin": marginCoin}, true)
	if err != nil {
		return err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		return fmt.Errorf("change_leverage code=%s msg=%s", r.Code, r.Msg)
	}
	return nil
}

// OpenResult summarises a placed order for logging.
type OpenResult struct {
	Symbol, Dir, Qty string
	Margin, Notional float64
	Price            float64
	Raw              json.RawMessage
}

// Open places a MARKET entry sized by margin = available×pct%, notional =
// margin×lev, qty = notional/mark (floored to precision), with TP/SL attached so
// the exchange manages the exit. tp/sl are absolute prices (0 = omit that leg).
func (c *Client) Open(symbol, dir string, pct float64, lev int, tp, sl float64, marginCoin string) (*OpenResult, error) {
	if marginCoin == "" {
		marginCoin = "USDT"
	}
	pair, err := c.tradingPair(symbol)
	if err != nil {
		return nil, err
	}
	mark, err := c.markPrice(symbol)
	if err != nil {
		return nil, err
	}
	avail, posMode, err := c.Account(marginCoin)
	if err != nil {
		return nil, err
	}
	margin := avail * pct / 100.0
	notional := margin * float64(lev)
	qty := floorTo(notional/mark, pair.BasePrecision)
	if qty <= 0 || qty < atof(pair.MinTradeVolume) {
		return nil, fmt.Errorf("qty %.*f below min %s (餘額/pct/槓桿太小)", pair.BasePrecision, qty, pair.MinTradeVolume)
	}
	if lev < pair.MinLeverage || lev > pair.MaxLeverage {
		return nil, fmt.Errorf("leverage %d out of range %d-%d", lev, pair.MinLeverage, pair.MaxLeverage)
	}
	if err := c.setLeverage(symbol, marginCoin, lev); err != nil {
		return nil, fmt.Errorf("set leverage: %w", err)
	}
	side := "BUY"
	if dir == "short" {
		side = "SELL"
	}
	pxfmt := func(v float64) string { return strconv.FormatFloat(v, 'f', pair.QuotePrecision, 64) }
	body := map[string]any{
		"symbol":    symbol,
		"side":      side,
		"orderType": "MARKET",
		"qty":       strconv.FormatFloat(qty, 'f', pair.BasePrecision, 64),
	}
	if posMode == "HEDGE" {
		body["tradeSide"] = "OPEN"
	}
	if tp > 0 {
		body["tpPrice"], body["tpStopType"], body["tpOrderType"] = pxfmt(tp), "LAST_PRICE", "MARKET"
	}
	if sl > 0 {
		body["slPrice"], body["slStopType"], body["slOrderType"] = pxfmt(sl), "LAST_PRICE", "MARKET"
	}
	raw, err := c.do(http.MethodPost, "/api/v1/futures/trade/place_order", nil, body, true)
	if err != nil {
		return nil, err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		return nil, fmt.Errorf("place_order code=%s msg=%s", r.Code, r.Msg)
	}
	return &OpenResult{
		Symbol: symbol, Dir: dir, Qty: body["qty"].(string),
		Margin: margin, Notional: notional, Price: mark, Raw: raw,
	}, nil
}
