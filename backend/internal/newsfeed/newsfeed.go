// Package newsfeed 是「消息雷達」的資料來源層:各交易所公告、Telegram 公開頻道、GitHub、
// Snapshot、SEC EDGAR、Polymarket、Upbit 韓元行情、鏈上錢包等免費來源的抓取與解析。
//
// 這一層只負責「抓 + 解析成 Item」,不判斷利多利空、不碰交易;分類、幣種對應、價格反應、
// 模擬開單都在 internal/cache/newsalpha.go。全部只用標準函式庫。
//
// ⚠️ 各來源的接口是依公開文件 / 常見用法實作,開發環境連不到外網無法實測;部署後請看
// 「消息雷達 → 來源狀態」確認每個來源是否正常,格式若有變動只需改對應的解析函式。
package newsfeed

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Item 是一則從來源抓到的消息(還沒分類)。
type Item struct {
	Source    string    // 來源代號,例如 binance / okx / bybit / bithumb / tg:whale_alert_io / github / snapshot …
	ID        string    // 在該來源內唯一(用來去重)
	Title     string    // 原文標題 / 內文(Telegram 取前段)
	URL       string    // 原文連結
	Lang      string    // en | ko | zh
	Published time.Time // 來源標示的發布時間(沒有就是零值)
	Coins     []string  // 來源本身就確定的幣種(例如 Snapshot space、GitHub repo 對應);空 = 交給上層從標題比對
	Category  string    // 來源本身就確定的分類(例如 Robinhood 上架);空 = 交給上層規則判斷
	Value     float64   // 數值型來源的附帶值(鏈上轉帳 USD、溢價 %、機率變化…)
}

// Client 是共用的 HTTP 抓取器(瀏覽器 UA、逾時)。
type Client struct {
	HTTP *http.Client
	UA   string
}

// New 建立預設 Client。
func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 12 * time.Second},
		UA:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36",
	}
}

// get 抓一個 URL 回傳原始內容;非 2xx 視為錯誤。headers 可覆蓋 UA 等。
func (c *Client) get(url string, headers map[string]string) ([]byte, error) {
	return c.do("GET", url, "", headers)
}

func (c *Client) do(method, url, body string, headers map[string]string) ([]byte, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UA)
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}

func (c *Client) getJSON(url string, headers map[string]string, v any) error {
	b, err := c.get(url, headers)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("解析失敗: %v", err)
	}
	return nil
}

// ---- 幣種比對 ----

// 常見會被誤認成代號的大寫英文字(不是幣)。
var tickerStop = map[string]bool{
	"USD": true, "USDT": true, "USDC": true, "KRW": true, "ETF": true, "SEC": true, "CEO": true,
	"NFT": true, "DEX": true, "CEX": true, "API": true, "AI": true, "THE": true, "AND": true, "FOR": true,
	"NEW": true, "WILL": true, "LIST": true, "UTC": true, "KST": true, "UPDATE": true, "NOTICE": true,
	"US": true, "EU": true, "UK": true, "FED": true, "CPI": true, "FOMC": true, "GDP": true, "TVL": true,
	"ATH": true, "ALL": true, "OF": true, "TO": true, "IN": true, "ON": true, "AT": true, "BY": true,
	"VIP": true, "APR": true, "APY": true, "OTC": true, "IPO": true, "DAO": true, "L1": true, "L2": true,
	"MARKET": true, "TRADING": true, "SPOT": true, "MARGIN": true, "FUTURES": true, "PERPETUAL": true,
	"CONTRACT": true, "CONTRACTS": true, "PAIRS": true, "PAIR": true, "TOKEN": true, "TOKENS": true,
}

var (
	parenTicker = regexp.MustCompile(`[(（]([A-Z0-9]{2,12})[)）]`)
	wordTicker  = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,11}\b`)
	pairTicker  = regexp.MustCompile(`\b([A-Z0-9]{2,12})(?:USDT|USDC|USD|KRW|BTC)\b`)
	krwMarket   = regexp.MustCompile(`KRW-([A-Z0-9]{2,12})`)
)

// ExtractTickers 從標題找出幣種代號,只回傳 known 裡有的(例如 Binance 有合約的幣)。
// 優先順序:括號內代號 →「XXXUSDT」交易對 → KRW-XXX → 獨立的大寫字。去重、保持順序。
func ExtractTickers(text string, known func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t == "" || seen[t] || tickerStop[t] || !known(t) {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, m := range parenTicker.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range pairTicker.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range krwMarket.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, w := range wordTicker.FindAllString(text, -1) {
		add(w)
	}
	return out
}

// LooseTickers 只看「括號內代號 / XXXUSDT 交易對 / KRW-XXX」這三種明確格式,不要求幣種已知。
// 給交易所官方公告用:要上的新幣通常還沒有任何行情,ExtractTickers 會比對不到。
func LooseTickers(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t == "" || seen[t] || tickerStop[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, m := range parenTicker.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range pairTicker.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range krwMarket.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	return out
}

// ---- 共用小工具 ----

// walkObjects 遞迴走訪任意 JSON,對每個 object 呼叫 fn。用來容忍各家回應外層結構的差異。
func walkObjects(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, x := range t {
			walkObjects(x, fn)
		}
	case []any:
		for _, x := range t {
			walkObjects(x, fn)
		}
	}
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case json.Number:
		return v.String()
	}
	return ""
}

// msTime 把毫秒(數字或字串)轉成時間;0 / 解析失敗回零值。
func msTime(m map[string]any, k string) time.Time {
	var ms float64
	switch v := m[k].(type) {
	case float64:
		ms = v
	case string:
		fmt.Sscanf(v, "%f", &ms)
	}
	if ms <= 0 {
		return time.Time{}
	}
	if ms < 1e12 { // 秒
		ms *= 1000
	}
	return time.UnixMilli(int64(ms)).UTC()
}

// stripTags 把 HTML 片段轉成純文字(Telegram / Bithumb 頁面用)。
var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = tagRe.ReplaceAllString(s, "")
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	return strings.TrimSpace(r.Replace(s))
}
