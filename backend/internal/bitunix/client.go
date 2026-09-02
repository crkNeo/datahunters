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
	// follow-mode (完全跟隨) needs these to drive partial closes + SL ratchets later,
	// without re-resolving the contract each time:
	Factor    float64 // 策略價 × Factor = 交易所價(1000PEPE 等綑綁合約用)
	QtyF      float64 // 成交數量(交易所 base 單位,float)
	BasePrec  int     // 數量精度(平倉格式化用)
	QuotePrec int     // 價格精度(改止損格式化用)
	PosMode   string  // ONE_WAY | HEDGE(平倉是否要帶 tradeSide=CLOSE)
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
		Factor: scale, QtyF: qty, BasePrec: pair.BasePrecision, QuotePrec: pair.QuotePrecision, PosMode: posMode,
	}, nil
}

// OpenSMC executes SMC_V2's market entry: full-position SL + TP2 attached to the
// market order, plus a reduce-only LIMIT TP1 for tp1Pct of the size. Slippage
// guard: if the live mark deviates from the trigger by more than maxSlipPct%, the
// whole entry is skipped (spec §4). tp1Fail (non-nil) means only the TP1 leg failed
// — the position is live with SL+TP2 and the caller should log it.
func (c *Client) OpenSMC(symbol, dir string, entry, sl, tp1, tp2, tp1Pct float64, lev int, marginCoin string, fixedMargin, pct, maxSlipPct float64) (res *OpenResult, tp1Fail error, err error) {
	if marginCoin == "" {
		marginCoin = "USDT"
	}
	pair, factor, err := c.resolveSymbol(symbol)
	if err != nil {
		return nil, nil, err
	}
	symbol = pair.Symbol
	if !pair.IsApiSupported {
		return nil, nil, fmt.Errorf("%s 不支援 API 交易 (isApiSupported=false)", symbol)
	}
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
		return nil, nil, err
	}
	slV, tp1V, tp2V := sl*factor, tp1*factor, tp2*factor // strategy scale → venue scale
	refTrig := entry * factor
	if maxSlipPct > 0 && refTrig > 0 && math.Abs(mark-refTrig)/refTrig*100 > maxSlipPct {
		return nil, nil, fmt.Errorf("滑價防護:現價 %.6g 偏離觸發 %.6g > %.2f%%", mark, refTrig, maxSlipPct)
	}
	avail, posMode, err := c.Account(marginCoin)
	if err != nil {
		return nil, nil, err
	}
	margin := avail * pct / 100.0
	if fixedMargin > 0 {
		if fixedMargin > avail {
			return nil, nil, fmt.Errorf("fixed margin %.4f%s exceeds available %.4f%s", fixedMargin, marginCoin, avail, marginCoin)
		}
		margin = fixedMargin
	}
	notional := margin * float64(lev)
	qty := floorTo(notional/mark, pair.BasePrecision)
	if qty <= 0 || qty < atof(pair.MinTradeVolume) {
		return nil, nil, fmt.Errorf("qty %.*f below min %s (餘額/pct/槓桿太小)", pair.BasePrecision, qty, pair.MinTradeVolume)
	}
	if err := c.setLeverage(symbol, marginCoin, lev); err != nil {
		return nil, nil, fmt.Errorf("set leverage: %w", err)
	}
	pxfmt := func(v float64) string { return strconv.FormatFloat(v, 'f', pair.QuotePrecision, 64) }
	side := "BUY"
	if dir == "short" {
		side = "SELL"
	}
	body := map[string]any{
		"symbol": symbol, "side": side, "orderType": "MARKET",
		"qty": strconv.FormatFloat(qty, 'f', pair.BasePrecision, 64),
	}
	if posMode == "HEDGE" {
		body["tradeSide"] = "OPEN"
	}
	if tp2V > 0 {
		body["tpPrice"], body["tpStopType"], body["tpOrderType"] = pxfmt(tp2V), "LAST_PRICE", "MARKET"
	}
	if slV > 0 {
		body["slPrice"], body["slStopType"], body["slOrderType"] = pxfmt(slV), "LAST_PRICE", "MARKET"
	}
	raw, err := c.do(http.MethodPost, "/api/v1/futures/trade/place_order", nil, body, true)
	if err != nil {
		return nil, nil, err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		return nil, nil, fmt.Errorf("place_order code=%s msg=%s", r.Code, r.Msg)
	}
	res = &OpenResult{Symbol: symbol, Dir: dir, Qty: body["qty"].(string),
		Margin: margin, Notional: notional, Price: mark, Lev: lev, Raw: raw}

	// reduce-only TP1 limit for tp1Pct of the position (opposite side).
	tp1Qty := floorTo(qty*tp1Pct, pair.BasePrecision)
	if tp1V > 0 && tp1Qty >= atof(pair.MinTradeVolume) {
		closeSide := "SELL"
		if dir == "short" {
			closeSide = "BUY"
		}
		tb := map[string]any{
			"symbol": symbol, "side": closeSide, "orderType": "LIMIT",
			"price": pxfmt(tp1V), "qty": strconv.FormatFloat(tp1Qty, 'f', pair.BasePrecision, 64),
			"effect": "GTC", "reduceOnly": true,
		}
		if posMode == "HEDGE" {
			tb["tradeSide"] = "CLOSE"
		}
		traw, terr := c.do(http.MethodPost, "/api/v1/futures/trade/place_order", nil, tb, true)
		if terr != nil {
			tp1Fail = terr
		} else {
			var tr2 codeMsg
			json.Unmarshal(traw, &tr2)
			if !tr2.ok() {
				tp1Fail = fmt.Errorf("TP1 code=%s msg=%s", tr2.Code, tr2.Msg)
			}
		}
	}
	return res, tp1Fail, nil
}

// ── 完全跟隨(follow)模式的實盤操作原語 ─────────────────────────────────────
// 開倉只掛初始 SL 當安全網;分批止盈 + 沿路套保由上層依紙上策略事件驅動,呼叫下列方法。

// Position 是一筆持倉(get_pending_positions 的一列,只留需要的欄位)。
type Position struct {
	PositionID string      `json:"positionId"`
	Symbol     string      `json:"symbol"`
	Side       string      `json:"side"` // LONG | SHORT
	Qty        string      `json:"qty"`
	Ctime      json.Number `json:"ctime"` // 建倉時戳(ms);分倉/避險下用來認「剛開的那一筆」
}

// PositionID 回傳某合約 + 方向、且「最新建立」那一筆持倉的 id。開倉後立刻呼叫存起來,
// 之後分批平倉 / 移動止損 / 整倉平 都鎖這個 id —— 即使帳號是分倉/避險(同幣同向可能有多
// 筆獨立倉),也不會誤動到別筆。
func (c *Client) PositionID(venueSymbol, dir string) (string, error) {
	want := "LONG"
	if dir == "short" {
		want = "SHORT"
	}
	var lastErr error
	var lastRaw []byte
	// 市價成交後持倉不會「同一瞬間」就出現在查詢裡 → 重試幾次(~3s)。帶 symbol 查詢,並自己
	// 再比對一次 symbol + 方向,取最新建立那一筆(= 我剛開的)。
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		raw, err := c.do(http.MethodGet, "/api/v1/futures/position/get_pending_positions",
			map[string]string{"symbol": venueSymbol}, nil, true)
		if err != nil {
			lastErr = err
			continue
		}
		lastRaw = raw
		var r struct {
			codeMsg
			Data []Position `json:"data"`
		}
		json.Unmarshal(raw, &r)
		if !r.ok() {
			lastErr = fmt.Errorf("get_pending_positions code=%s msg=%s", r.Code, r.Msg)
			continue
		}
		best, bestC := "", int64(-1)
		for _, p := range r.Data {
			if !strings.EqualFold(p.Symbol, venueSymbol) || !strings.EqualFold(p.Side, want) {
				continue
			}
			ct, _ := p.Ctime.Int64()
			if ct >= bestC {
				best, bestC = p.PositionID, ct
			}
		}
		if best != "" {
			return best, nil
		}
		lastErr = fmt.Errorf("找不到 %s %s 的持倉(第 %d 次)", venueSymbol, dir, attempt+1)
	}
	// 診斷:把最後一次的原始回應(截斷)帶進錯誤,方便查為何回空
	snippet := string(lastRaw)
	if len(snippet) > 400 {
		snippet = snippet[:400]
	}
	return "", fmt.Errorf("%v | raw=%s", lastErr, snippet)
}

// CloseQtyMarket 送一張 reduce-only 市價單平掉某「持倉」的 qty(dir 是持倉方向,平倉單走
// 反向)。用於分批止盈的每一段。帶 positionId → 只動這一倉(分倉/避險安全);避險模式再帶
// tradeSide=CLOSE。qty 已是交易所 base 單位。
func (c *Client) CloseQtyMarket(venueSymbol, dir string, qty float64, basePrec int, hedge bool, positionId string) error {
	if qty <= 0 {
		return nil
	}
	side := "SELL"
	if dir == "short" {
		side = "BUY"
	}
	body := map[string]any{
		"symbol": venueSymbol, "side": side, "orderType": "MARKET",
		"qty":        strconv.FormatFloat(qty, 'f', basePrec, 64),
		"reduceOnly": true,
	}
	if positionId != "" {
		body["positionId"] = positionId
	}
	if hedge {
		body["tradeSide"] = "CLOSE" // 避險模式必填;此時 positionId 亦為必填(上面已帶)
	}
	raw, err := c.do(http.MethodPost, "/api/v1/futures/trade/place_order", nil, body, true)
	if err != nil {
		return err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		bj, _ := json.Marshal(body) // 診斷:印出送出的參數,看 10002 是哪個欄位
		return fmt.Errorf("close code=%s msg=%s | body=%s", r.Code, r.Msg, string(bj))
	}
	return nil
}

// FlashClose 依 positionId 市價整倉平掉(最終平倉用)。只動這一倉,分倉/避險安全。
func (c *Client) FlashClose(positionId string) error {
	if positionId == "" {
		return fmt.Errorf("flash close: 缺 positionId")
	}
	raw, err := c.do(http.MethodPost, "/api/v1/futures/trade/flash_close_position", nil,
		map[string]any{"positionId": positionId}, true)
	if err != nil {
		return err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		return fmt.Errorf("flash_close code=%s msg=%s (posId=%s)", r.Code, r.Msg, positionId)
	}
	return nil
}

// SetPositionSL 設定/取代持倉的止損(每倉僅一張 Position TP/SL Order,再打即取代 → 沿路套保
// 靠它上移)。slVenue 已是交易所價。
func (c *Client) SetPositionSL(venueSymbol, positionId string, slVenue float64, quotePrec int) error {
	if slVenue <= 0 || positionId == "" {
		return nil
	}
	body := map[string]any{
		"symbol":     venueSymbol,
		"positionId": positionId,
		"slPrice":    strconv.FormatFloat(slVenue, 'f', quotePrec, 64),
		"slStopType": "LAST_PRICE",
	}
	raw, err := c.do(http.MethodPost, "/api/v1/futures/tpsl/position/place_order", nil, body, true)
	if err != nil {
		return err
	}
	var r codeMsg
	json.Unmarshal(raw, &r)
	if !r.ok() {
		return fmt.Errorf("set SL code=%s msg=%s", r.Code, r.Msg)
	}
	return nil
}
