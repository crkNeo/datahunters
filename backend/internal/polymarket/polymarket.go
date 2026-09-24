// Package polymarket screens Polymarket CRYPTO-leaderboard wallets for
// cross-window consistency (PLAN v0.2 功能一). 功能二(closed-position 分析)、Gamma
// 類別判定、短週期市場偵測先不做 —— 都要等真實 API 實測欄位後再加(見 PLAN §11)。
//
// ⚠️ Polymarket API 對美國 IP 封鎖,開發機連不到,所以欄位名/單位皆「照企劃書假設、未實測」。
// JSON 解析刻意寬鬆(容忍多種欄位名),真正跑在能連線的機器上時再依實際回傳微調。
package polymarket

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

const base = "https://data-api.polymarket.com"

var periods = []string{"DAY", "WEEK", "MONTH", "ALL"}

var client = &http.Client{Timeout: 15 * time.Second}

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
}

// row 是排行榜/個人成績共用的寬鬆解析結構(欄位名未實測,盡量多接幾種)。
type row struct {
	Wallet  string  `json:"wallet"`
	Proxy   string  `json:"proxyWallet"`
	User    string  `json:"user"`
	Address string  `json:"address"`
	Name    string  `json:"name"`
	User2   string  `json:"username"`
	Pseudo  string  `json:"pseudonym"`
	Rank    int     `json:"rank"`
	PnL     float64 `json:"pnl"`
	Profit  float64 `json:"profit"`
	Vol     float64 `json:"vol"`
	Volume  float64 `json:"volume"`
}

func (r row) wallet() string { return first(r.Wallet, r.Proxy, r.User, r.Address) }
func (r row) name() string   { return first(r.Name, r.User2, r.Pseudo) }
func (r row) pnl() float64   { return nz(r.PnL, r.Profit) }
func (r row) vol() float64   { return nz(r.Vol, r.Volume) }

func first(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
func nz(vs ...float64) float64 {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

// fetchLeaderboard 打排行榜。回傳寬鬆解析後的 rows。leaderboard 回傳可能是陣列或 {leaderboard:[…]}。
func fetchLeaderboard(q url.Values) ([]row, error) {
	u := base + "/v1/leaderboard?" + q.Encode()
	req, _ := http.NewRequest("GET", u, nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("leaderboard %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var arr []row
	if json.Unmarshal(body, &arr) == nil && len(arr) > 0 {
		return arr, nil
	}
	var wrap struct {
		Leaderboard []row `json:"leaderboard"`
		Data        []row `json:"data"`
	}
	if json.Unmarshal(body, &wrap) == nil {
		if len(wrap.Leaderboard) > 0 {
			return wrap.Leaderboard, nil
		}
		return wrap.Data, nil
	}
	return nil, fmt.Errorf("leaderboard: unexpected shape")
}

// Leaderboard 回加密貨幣類 MONTH 排行榜前 limit 名(候選清單)。
func Leaderboard(limit int) ([]row, error) {
	q := url.Values{"category": {"CRYPTO"}, "timePeriod": {"MONTH"}, "limit": {itoa(limit)}}
	return fetchLeaderboard(q)
}

// Windows 查一個錢包在四個時間區間的成績(每個錢包 4 次請求)。
func Windows(wallet string) map[string]Window {
	out := map[string]Window{}
	for _, p := range periods {
		w := Window{Period: p}
		q := url.Values{"category": {"CRYPTO"}, "timePeriod": {p}, "user": {wallet}}
		rows, err := fetchLeaderboard(q)
		if err == nil && len(rows) > 0 {
			r := rows[0]
			w.Rank, w.PnL, w.Vol, w.HasData = r.Rank, r.pnl(), r.vol(), true
		}
		out[p] = w
		time.Sleep(120 * time.Millisecond) // 禮貌間隔
	}
	return out
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
	if all.PnL != 0 {
		rep.RecentShare = month.PnL / all.PnL
	}

	switch {
	case all.Vol < th.MinAllVol || dataWindows < th.MinWindows:
		rep.Verdict = "資料不足"
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("有資料區間 %d 個、ALL 量 %.0f 過低", dataWindows, all.Vol))
	case all.PnL > 0 && month.PnL < 0:
		rep.Verdict = "近期轉弱"
		rep.Reasons = append(rep.Reasons, "ALL 獲利但 MONTH 轉虧")
	case month.PnL > all.PnL || rep.RecentShare > th.RecentShareCap:
		rep.Verdict = "短期爆發"
		if month.PnL > all.PnL {
			rep.Reasons = append(rep.Reasons, "MONTH > ALL(先前曾虧損)")
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

// Screen 掃描 CRYPTO 排行榜前 limit 名的一致性,結果快取 10 分鐘。
func Screen(limit int) ([]Report, error) {
	mu.Lock()
	defer mu.Unlock()
	if time.Since(cachedAt) < 10*time.Minute && cached != nil {
		return cached, nil
	}
	lb, err := Leaderboard(limit)
	if err != nil {
		return nil, err
	}
	th := Defaults()
	out := make([]Report, 0, len(lb))
	for _, r := range lb {
		wallet := r.wallet()
		if wallet == "" {
			continue
		}
		rep := Assess(wallet, r.name(), Windows(wallet), th)
		rep.Windows = nil // payload 精簡:表格用不到逐區間明細
		out = append(out, rep)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AllPnL > out[j].AllPnL })
	cached, cachedAt = out, time.Now()
	return out, nil
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
