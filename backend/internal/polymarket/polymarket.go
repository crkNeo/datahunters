// Package polymarket screens Polymarket CRYPTO-leaderboard wallets for
// cross-window consistency (PLAN v0.2 功能一) 並附上各錢包目前的預測市場持倉。
// 功能二(closed-position 分析)、Gamma 類別判定、短週期市場偵測先不做(見 PLAN §11)。
//
// 欄位已對真實 API 實測(2026-09):/v1/leaderboard 回物件陣列、數字常為字串;
// /positions 回持倉陣列。台灣 ISP 依法院命令封鎖此網域,client 自帶 1.1.1.1 解析繞過(見下)。
package polymarket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

const base = "https://data-api.polymarket.com"

var periods = []string{"DAY", "WEEK", "MONTH", "ALL"}

// 台灣部分 ISP 依苗栗地院命令把 data-api.polymarket.com 的 DNS 投毒成封鎖頁
// (182.173.0.181),但真實 IP(Cloudflare)其實可達。所以這裡自帶解析器走公共 DNS
// (1.1.1.1),繞過被投毒的 ISP resolver,不必要求使用者去改作業系統 DNS。
// ponytail: 硬指定 1.1.1.1:53;哪天連 port 53 都被攔再改走 DoH。
var client = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 8 * time.Second,
			Resolver: &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "1.1.1.1:53")
				},
			},
		}).DialContext,
	},
}

// Thresholds 集中所有判定門檻(PLAN §3.3),不寫死在邏輯裡。
type Thresholds struct {
	RecentShareCap float64 // 近期佔比上限,超過視為獲利集中在最近(預設 0.8)
	MinWindows     int     // 至少要有資料的區間數(預設 2)
	MinAllVol      float64 // ALL 成交量下限,過低視為資料不足(預設 1000)
}

func Defaults() Thresholds { return Thresholds{RecentShareCap: 0.8, MinWindows: 2, MinAllVol: 1000} }

// Window 是單一時間區間的個人成績。
type Window struct {
	Period  string  `json:"period"`
	Rank    int     `json:"rank"`
	PnL     float64 `json:"pnl"`
	Vol     float64 `json:"vol"`
	HasData bool    `json:"has_data"`
}

// Report 是一個錢包的一致性判定結果。
type Report struct {
	Wallet          string            `json:"wallet"`
	Name            string            `json:"name"`
	Windows         map[string]Window `json:"windows"`
	AllPnL          float64           `json:"all_pnl"`
	MonthPnL        float64           `json:"month_pnl"`
	ProfitableCount int               `json:"profitable_count"` // 四區間中 pnl>0 的數量
	RankedCount     int               `json:"ranked_count"`     // 四區間中有排名的數量
	RecentShare     float64           `json:"recent_share"`     // MONTH pnl / ALL pnl
	Verdict         string            `json:"verdict"`          // 長期穩定 | 短期爆發 | 近期轉弱 | 觀察中 | 資料不足
	Reasons         []string          `json:"reasons"`
	Positions       []Position        `json:"positions"` // 目前持倉(給名人動向式的點名看倉)
}

// Position 是一個錢包在單一預測市場的目前持倉(Polymarket /positions)。
type Position struct {
	Title      string  `json:"title"`       // 市場名稱
	Outcome    string  `json:"outcome"`     // 押的方向:Yes/No/Up/Down
	Size       float64 `json:"size"`        // 股數
	AvgPrice   float64 `json:"avg_price"`   // 進場均價(0~1 機率)
	CurPrice   float64 `json:"cur_price"`   // 現價
	CurValue   float64 `json:"cur_value"`   // 目前市值(USD)
	CashPnl    float64 `json:"cash_pnl"`    // 未實現損益(USD)
	PercentPnl float64 `json:"percent_pnl"` // 損益 %
	Icon       string  `json:"icon"`        // 市場圖示
	EndDate    string  `json:"end_date"`    // 到期日
}

// row 是排行榜的一列(已抽取)。實測欄位:proxyWallet / userName / rank / pnl / vol。
type row struct {
	Wallet string
	Name   string
	Rank   int
	PnL    float64
	Vol    float64
}

// 值可能是數字或字串("123.45"),都要吃。
func gf(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v
		case string:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f
			}
		}
	}
	return 0
}
func gs(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func mapsToRows(ms []map[string]any) []row {
	out := make([]row, 0, len(ms))
	for _, m := range ms {
		out = append(out, row{
			Wallet: gs(m, "proxyWallet", "wallet", "user", "address"),
			Name:   gs(m, "userName", "name", "username", "pseudonym"),
			Rank:   int(gf(m, "rank")),
			PnL:    gf(m, "pnl", "profit", "amount"),
			Vol:    gf(m, "vol", "volume"),
		})
	}
	return out
}

// fetchLeaderboard 打排行榜。實測 /v1/leaderboard 回傳是物件陣列;仍寬鬆接受
// {leaderboard|data|results:[…]} 包裝,且數字可為字串。真的認不得就把原始前段吐進錯誤。
func fetchLeaderboard(q url.Values) ([]row, error) {
	u := base + "/v1/leaderboard?" + q.Encode()
	req, _ := http.NewRequest("GET", u, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("leaderboard HTTP %d: %s", resp.StatusCode, snip(body))
	}
	var arr []map[string]any
	if json.Unmarshal(body, &arr) == nil {
		return mapsToRows(arr), nil // 空陣列也回(user 查詢無排名時就是空的)
	}
	var wrap map[string]json.RawMessage
	if json.Unmarshal(body, &wrap) == nil {
		for _, k := range []string{"leaderboard", "data", "results", "traders"} {
			if raw, ok := wrap[k]; ok {
				var a []map[string]any
				if json.Unmarshal(raw, &a) == nil {
					return mapsToRows(a), nil
				}
			}
		}
	}
	return nil, fmt.Errorf("leaderboard: unexpected shape: %s", snip(body))
}

func snip(b []byte) string {
	if len(b) > 220 {
		return string(b[:220]) + "…"
	}
	return string(b)
}

// Assess 是純函式:輸入四區間成績 → 一致性判定(PLAN §3.2/3.3)。不發 HTTP,方便測試。
func Assess(wallet, name string, w map[string]Window, th Thresholds) Report {
	all, month := w["ALL"], w["MONTH"]
	rep := Report{Wallet: wallet, Name: name, Windows: w, AllPnL: all.PnL, MonthPnL: month.PnL}
	dataWindows := 0
	for _, p := range periods {
		x := w[p]
		if x.HasData {
			dataWindows++
			if x.PnL > 0 {
				rep.ProfitableCount++
			}
			if x.Rank > 0 {
				rep.RankedCount++
			}
		}
	}
	if all.PnL > 0 {
		// 只有 ALL 為正才有「近期佔比」意義;ALL≤0 時 month/all 會爆成天文數字或負值。
		rep.RecentShare = month.PnL / all.PnL
	}

	switch {
	case dataWindows < th.MinWindows || (all.Vol > 0 && all.Vol < th.MinAllVol):
		// vol 常回 0(未提供),只有真的回報了低量才當資料不足,否則靠區間數判斷。
		rep.Verdict = "資料不足"
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("有資料區間 %d 個、ALL 量 %.0f", dataWindows, all.Vol))
	case all.PnL > 0 && month.PnL < 0:
		rep.Verdict = "近期轉弱"
		rep.Reasons = append(rep.Reasons, "ALL 獲利但 MONTH 轉虧")
	case month.PnL > all.PnL || rep.RecentShare > th.RecentShareCap:
		rep.Verdict = "短期爆發"
		if month.PnL > all.PnL {
			rep.Reasons = append(rep.Reasons, "MONTH 損益超過 ALL(獲利集中在近期)")
		} else {
			rep.Reasons = append(rep.Reasons, fmt.Sprintf("近期佔比 %.0f%% 偏高", rep.RecentShare*100))
		}
	case all.PnL > 0 && month.PnL > 0 && rep.RecentShare <= th.RecentShareCap:
		rep.Verdict = "長期穩定"
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("ALL/MONTH 皆獲利、近期佔比 %.0f%%、獲利區間 %d/4", rep.RecentShare*100, rep.ProfitableCount))
	default:
		rep.Verdict = "觀察中"
	}
	return rep
}

// --- 快取:admin 端點按需觸發,整份掃描 ~limit×5 次請求偏慢,結果快取 10 分鐘 ---
// ponytail: 全域單把鎖 + 單一快取,夠一個 admin 按需用;要多人/多參數再拆。
var (
	mu       sync.Mutex
	cached   []Report
	cachedAt time.Time
)

// fetchWindows 查單一錢包在四個區間的成績。必須用 user= 逐區間查:MONTH 榜首常在
// ALL 榜排到幾萬名(近期爆發、歷史平庸),整頁抓不到他,只有 user= 查得到真實跨區間值。
func fetchWindows(wallet string) map[string]Window {
	out := make(map[string]Window, 4)
	for _, p := range periods {
		w := Window{Period: p}
		rows, err := fetchLeaderboard(url.Values{"category": {"CRYPTO"}, "timePeriod": {p}, "user": {wallet}})
		if err == nil && len(rows) > 0 {
			r := rows[0]
			w.Rank, w.PnL, w.Vol, w.HasData = r.Rank, r.PnL, r.Vol, true
		}
		out[p] = w
	}
	return out
}

// fetchPositions 抓一個錢包目前的預測市場持倉(依市值大→小,只留還有市值的活倉,
// 過濾已結算/歸零的死倉)。認不得就回 nil,不讓單一錢包拖垮整批。
func fetchPositions(wallet string) []Position {
	q := url.Values{"user": {wallet}, "sortBy": {"CURRENT"}, "sortDirection": {"DESC"}, "limit": {"20"}}
	req, _ := http.NewRequest("GET", base+"/positions?"+q.Encode(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var arr []map[string]any
	if json.Unmarshal(body, &arr) != nil {
		return nil
	}
	out := make([]Position, 0, len(arr))
	for _, m := range arr {
		cv := gf(m, "currentValue")
		if cv <= 0 {
			continue // 只留還有市值的活倉
		}
		out = append(out, Position{
			Title: gs(m, "title"), Outcome: gs(m, "outcome"),
			Size: gf(m, "size"), AvgPrice: gf(m, "avgPrice"), CurPrice: gf(m, "curPrice"),
			CurValue: cv, CashPnl: gf(m, "cashPnl"), PercentPnl: gf(m, "percentPnl"),
			Icon: gs(m, "icon"), EndDate: gs(m, "endDate"),
		})
	}
	return out
}

// Screen 掃描 CRYPTO MONTH 排行榜前 limit 名的跨區間一致性,結果快取 10 分鐘。
// 每個候選要 4 次 user= 查詢才有正確跨區間值,序列跑會撞前端 20s 逾時,所以用
// bounded pool 併發(限 8 條,對對方 API 友善)。ponytail: 併發數寫死 8,要更快再調。
func Screen(limit int) ([]Report, error) {
	mu.Lock()
	defer mu.Unlock()
	if time.Since(cachedAt) < 10*time.Minute && cached != nil {
		return cached, nil
	}
	cands, err := fetchLeaderboard(url.Values{"category": {"CRYPTO"}, "timePeriod": {"MONTH"}, "limit": {itoa(limit)}})
	if err != nil {
		return nil, err
	}
	th := Defaults()
	out := make([]Report, len(cands)) // 各 goroutine 只寫自己那格,無資料競爭
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, c := range cands {
		if c.Wallet == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c row) {
			defer wg.Done()
			defer func() { <-sem }()
			rep := Assess(c.Wallet, c.Name, fetchWindows(c.Wallet), th)
			rep.Windows = nil // payload 精簡:表格用不到逐區間明細
			rep.Positions = fetchPositions(c.Wallet)
			out[i] = rep
		}(i, c)
	}
	wg.Wait()
	res := make([]Report, 0, len(out))
	for _, r := range out {
		if r.Wallet != "" { // 略過空錢包留下的空洞
			res = append(res, r)
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].AllPnL > res[j].AllPnL })
	cached, cachedAt = res, time.Now()
	return res, nil
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
