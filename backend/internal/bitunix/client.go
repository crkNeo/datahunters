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

// snapPow10 rounds x to the nearest power of ten (1, 1000, 1e6 …). Used to turn a
// noisy mark/entry ratio into a clean bundle factor.
func snapPow10(x float64) float64 {
	if x <= 0 {
		return 1
	}
	return math.Pow(10, math.Round(math.Log10(x)))
}

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
	IsApiSupported bool   `json:"isApiSupported"` // false for UI-only symbols (tokenised stocks, metals)
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

// resolveSymbol maps a requested "COINUSDT" to the actual Bitunix contract and its
// price multiplier. Bitunix lists high-supply memecoins bundled ×1000 / ×1e6
// (1000PEPE, 1000SHIB, 1000000MOG, 1MBABYDOGE), whose price is factor× the raw
// coin. The caller must scale price-based inputs (TP/SL) by the returned factor.
func (c *Client) resolveSymbol(reqSymbol string) (pairData, float64, error) {
	raw, err := c.do(http.MethodGet, "/api/v1/futures/market/trading_pairs", nil, nil, false)
	if err != nil {
		return pairData{}, 0, err
	}
	var r struct {
		codeMsg
		Data []pairData `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return pairData{}, 0, fmt.Errorf("decode trading_pairs: %v", err)
	}
	base := strings.TrimSuffix(strings.ToUpper(reqSymbol), "USDT")
	// exact first (factor 1), then bundled variants — order matters.
	cands := []struct {
		sym    string
		factor float64
	}{
		{reqSymbol, 1},
		{"1000" + base + "USDT", 1000},
		{"1000000" + base + "USDT", 1e6},
		{"1M" + base + "USDT", 1e6},
	}
	for _, cd := range cands {
		for _, p := range r.Data {
			if strings.EqualFold(p.Symbol, cd.sym) {
				return p, cd.factor, nil
			}
		}
	}
	return pairData{}, 0, fmt.Errorf("%s 在 Bitunix 找不到合約(含 1000/1M 變體)", reqSymbol)
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
	Lev              int // leverage actually used (after max-resolve / clamp)
	Raw              json.RawMessage
}

// Open places a MARKET entry sized by margin, notional = margin×lev, qty =
// notional/mark (floored to precision), with TP/SL attached so the exchange
// manages the exit. tp/sl are absolute prices (0 = omit that leg).
//
// Sizing: fixedMargin > 0 pins margin to that many marginCoin (e.g. 1.5 USDT) —
// the account is still queried to reject orders larger than the free balance.
// Otherwise margin = available × pct%.
//
// Leverage: lev <= 0 means "use this coin's max leverage"; any other value is
// clamped into the coin's [minLeverage, maxLeverage] range (so a fixed 30 still
// trades a 20x-cap coin at 20x instead of failing outright).
//
// entry is the strategy's raw entry price; when >0 it derives the price-scale
// factor empirically (mark/entry snapped to a power of ten), which self-corrects
// bundled contracts (1000PEPE priced ×1000) regardless of the symbol name.
func (c *Client) Open(symbol, dir string, pct float64, lev int, entry, tp, sl float64, marginCoin string, fixedMargin float64) (*OpenResult, error) {
	if marginCoin == "" {
		marginCoin = "USDT"
	}
	pair, nameFactor, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, err
	}
	symbol = pair.Symbol // use the resolved venue symbol (e.g. 1000PEPEUSDT) below
	if !pair.IsApiSupported {
		return nil, fmt.Errorf("%s 不支援 API 交易 (isApiSupported=false — 代幣股/貴金屬僅限網頁/App)", symbol)
	}
	// resolve leverage: 0 ⇒ coin max; else clamp into the coin's allowed range.
	switch {
	case lev <= 0:
		lev = pair.MaxLeverage
	case lev < pair.MinLeverage:
		lev = pair.MinLeverage
	case lev > pair.MaxLeverage:
		lev = pair.MaxLeverage
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
	if fixedMargin > 0 {
		if fixedMargin > avail {
			return nil, fmt.Errorf("fixed margin %.4f%s exceeds available %.4f%s", fixedMargin, marginCoin, avail, marginCoin)
		}
		margin = fixedMargin
	}
	notional := margin * float64(lev)
	qty := floorTo(notional/mark, pair.BasePrecision)
	if qty <= 0 || qty < atof(pair.MinTradeVolume) {
		return nil, fmt.Errorf("qty %.*f below min %s (餘額/pct/槓桿太小)", pair.BasePrecision, qty, pair.MinTradeVolume)
	}
	if err := c.setLeverage(symbol, marginCoin, lev); err != nil {
		return nil, fmt.Errorf("set leverage: %w", err)
	}
	// price-scale TP/SL onto this contract. Prefer the empirical factor
	// (venue mark ÷ strategy entry, snapped to a power of ten) so bundled
	// contracts self-correct; fall back to the symbol-name factor.
	scale := nameFactor
	if entry > 0 && mark > 0 {
		scale = snapPow10(mark / entry)
	}
	if scale != 1 {
		if tp > 0 {
			tp *= scale
		}
		if sl > 0 {
			sl *= scale
		}
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
		Margin: margin, Notional: notional, Price: mark, Lev: lev, Raw: raw,
	}, nil
}
