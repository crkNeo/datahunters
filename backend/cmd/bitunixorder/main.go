// Command bitunixorder is a manual Bitunix USDT-M futures order tool. It sizes a
// MARKET order as: margin = available balance × pct%, notional = margin × lev,
// qty(base coin) = notional / mark price, floored to the symbol's precision.
//
//	go run ./cmd/bitunixorder -symbol BTCUSDT -side long -pct 1 -lev 25 -dry
//
// Keys come from BITUNIX_API_KEY / BITUNIX_API_SECRET (env or a ./.env file).
// -dry computes and prints everything WITHOUT setting leverage or sending the
// order. Drop -dry to actually set leverage and place the order.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const host = "https://fapi.bitunix.com"

type Client struct {
	key, secret string
	http        *http.Client
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func nonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b) // 32 hex chars
}

// do performs a request. signed=true adds the auth headers (double-SHA256).
// query is signed as sorted key+value with NO separators (e.g. "marginCoinUSDT");
// body is the compact JSON string (no spaces). Both must match exactly.
func (c *Client) do(method, path string, query map[string]string, bodyObj any, signed bool) ([]byte, error) {
	var bodyStr string
	if bodyObj != nil {
		b, err := json.Marshal(bodyObj) // Go marshals compact (no spaces)
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
		sign := sha256Hex(digest + c.secret)
		req.Header.Set("api-key", c.key)
		req.Header.Set("nonce", n)
		req.Header.Set("timestamp", ts)
		req.Header.Set("sign", sign)
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

// ---- response shapes ----

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
	MarginCoin         string `json:"marginCoin"`
	Available          string `json:"available"`
	Margin             string `json:"margin"`
	PositionMode       string `json:"positionMode"` // ONE_WAY | HEDGE
	CrossUnrealizedPNL string `json:"crossUnrealizedPNL"`
}

type tickerData struct {
	Symbol    string `json:"symbol"`
	LastPrice string `json:"lastPrice"`
	MarkPrice string `json:"markPrice"`
}

func atof(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

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
		return pairData{}, fmt.Errorf("decode trading_pairs: %v (%s)", err, string(raw))
	}
	for _, p := range r.Data {
		if strings.EqualFold(p.Symbol, symbol) {
			return p, nil
		}
	}
	return pairData{}, fmt.Errorf("symbol %s not found in trading_pairs", symbol)
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
		return 0, fmt.Errorf("decode tickers: %v (%s)", err, string(raw))
	}
	for _, t := range r.Data {
		if strings.EqualFold(t.Symbol, symbol) {
			p := atof(t.MarkPrice)
			if p <= 0 {
				p = atof(t.LastPrice)
			}
			if p > 0 {
				return p, nil
			}
		}
	}
	return 0, fmt.Errorf("no price for %s", symbol)
}

func (c *Client) account(marginCoin string) (acctData, error) {
	raw, err := c.do(http.MethodGet, "/api/v1/futures/account", map[string]string{"marginCoin": marginCoin}, nil, true)
	if err != nil {
		return acctData{}, err
	}
	var r struct {
		codeMsg
		Data []acctData `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return acctData{}, fmt.Errorf("decode account: %v (%s)", err, string(raw))
	}
	if !r.ok() {
		return acctData{}, fmt.Errorf("account error code=%s msg=%s", r.Code, r.Msg)
	}
	if len(r.Data) == 0 {
		return acctData{}, fmt.Errorf("account: empty data (%s)", string(raw))
	}
	return r.Data[0], nil
}

func (c *Client) changeLeverage(symbol, marginCoin string, lev int) error {
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

func (c *Client) placeOrder(body map[string]any) ([]byte, error) {
	raw, err := c.do(http.MethodPost, "/api/v1/futures/trade/place_order", nil, body, true)
	if err != nil {
		return raw, err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		return raw, fmt.Errorf("place_order code=%s msg=%s", r.Code, r.Msg)
	}
	return raw, nil
}

// floorTo rounds x DOWN to n decimal places (never over-sizes the order).
func floorTo(x float64, n int) float64 {
	f := math.Pow10(n)
	return math.Floor(x*f) / f
}

// loadDotEnv loads KEY=VALUE lines from ./.env without overriding real env.
func loadDotEnv() {
	f, err := os.Open(".env")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			k := strings.TrimSpace(line[:eq])
			v := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
			if k != "" && os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}

func main() {
	loadDotEnv()
	symbol := flag.String("symbol", "", "trading pair, e.g. BTCUSDT")
	side := flag.String("side", "long", "long | short")
	pct := flag.Float64("pct", 1.0, "margin as percent of available balance (1 = 1%)")
	lev := flag.Int("lev", 0, "leverage, e.g. 25")
	price := flag.Float64("price", 0, "limit price; 0 = MARKET order")
	marginCoin := flag.String("margin-coin", "USDT", "margin coin")
	dry := flag.Bool("dry", false, "compute only; do NOT set leverage or place the order")
	flag.Parse()

	if *symbol == "" || *lev <= 0 {
		log.Fatal("usage: -symbol BTCUSDT -side long -pct 1 -lev 25 [-price P] [-dry]")
	}
	if *side != "long" && *side != "short" {
		log.Fatal("-side must be long or short")
	}
	key, secret := os.Getenv("BITUNIX_API_KEY"), os.Getenv("BITUNIX_API_SECRET")
	if key == "" || secret == "" {
		log.Fatal("set BITUNIX_API_KEY and BITUNIX_API_SECRET (env or ./.env)")
	}
	c := &Client{key: key, secret: secret, http: &http.Client{Timeout: 15 * time.Second}}

	pair, err := c.tradingPair(*symbol)
	if err != nil {
		log.Fatalf("trading pair: %v", err)
	}
	mark, err := c.markPrice(*symbol)
	if err != nil {
		log.Fatalf("price: %v", err)
	}
	acct, err := c.account(*marginCoin)
	if err != nil {
		log.Fatalf("account: %v", err)
	}

	avail := atof(acct.Available)
	margin := avail * (*pct) / 100.0
	notional := margin * float64(*lev)
	qty := floorTo(notional/mark, pair.BasePrecision)
	minQty := atof(pair.MinTradeVolume)

	fmt.Println("──────── Bitunix 下單試算 ────────")
	fmt.Printf("交易對      %s  (數量精度 %d 位, 最小量 %s, 槓桿 %d~%d)\n",
		pair.Symbol, pair.BasePrecision, pair.MinTradeVolume, pair.MinLeverage, pair.MaxLeverage)
	fmt.Printf("持倉模式    %s\n", acct.PositionMode)
	fmt.Printf("可用餘額    %.4f %s\n", avail, *marginCoin)
	fmt.Printf("標記價      %.6g\n", mark)
	fmt.Printf("方向/槓桿   %s  %dx\n", *side, *lev)
	fmt.Printf("保證金      %.4f %s  (= 本金 %.2f%%)\n", margin, *marginCoin, *pct)
	fmt.Printf("名目價值    %.4f %s  (= 保證金 × 槓桿)\n", notional, *marginCoin)
	fmt.Printf("下單數量    %.*f %s\n", pair.BasePrecision, qty, pair.Symbol)
	fmt.Println("──────────────────────────────────")

	if qty < minQty || qty <= 0 {
		log.Fatalf("數量 %.*f 低於最小下單量 %s — 請提高 -pct/-lev 或本金", pair.BasePrecision, qty, pair.MinTradeVolume)
	}
	if *lev < pair.MinLeverage || *lev > pair.MaxLeverage {
		log.Fatalf("槓桿 %d 超出允許範圍 %d~%d", *lev, pair.MinLeverage, pair.MaxLeverage)
	}
	if *dry {
		fmt.Println("[dry] 只試算,未設定槓桿、未下單。移除 -dry 才會實際送出。")
		return
	}

	if err := c.changeLeverage(*symbol, *marginCoin, *lev); err != nil {
		log.Fatalf("設定槓桿失敗: %v", err)
	}
	fmt.Printf("✓ 槓桿已設為 %dx\n", *lev)

	sideAPI := "BUY"
	if *side == "short" {
		sideAPI = "SELL"
	}
	qtyStr := strconv.FormatFloat(qty, 'f', pair.BasePrecision, 64)
	body := map[string]any{
		"symbol":    *symbol,
		"side":      sideAPI,
		"orderType": "MARKET",
		"qty":       qtyStr,
	}
	if *price > 0 {
		body["orderType"] = "LIMIT"
		body["price"] = strconv.FormatFloat(*price, 'f', pair.QuotePrecision, 64)
		body["effect"] = "GTC"
	}
	// hedge mode needs tradeSide OPEN; one-way mode omits it.
	if acct.PositionMode == "HEDGE" {
		body["tradeSide"] = "OPEN"
	}

	raw, err := c.placeOrder(body)
	if err != nil {
		log.Fatalf("下單失敗: %v", err)
	}
	var pretty bytes.Buffer
	json.Indent(&pretty, raw, "", "  ")
	fmt.Println("✓ 下單成功:")
	fmt.Println(pretty.String())
}
