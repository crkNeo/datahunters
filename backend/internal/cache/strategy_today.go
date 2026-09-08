package cache

import (
	"sort"
	"time"

	"datahunter/internal/auth"
)

// strategy_today.go —— 首頁「今日策略 · 表現前三名」彙總。
//
// 純唯讀:重用 strategyHistFull(以「今日午夜→現在」為時間窗)算出各策略今日的
// 平倉統計,依使用者角色可見範圍(TabRole)過濾,最後由呼叫端取前三。
// 不改任何策略/資料採集邏輯,只是把既有 paper_trades 換個角度聚合呈現。

// stratTabKey 把策略「分頁 key」對應到 strategyHistFull 用的 book「策略 key」。
// 兩者多半同名,例外:星軌分頁是 paper,但 book 名是 main。
var stratTabKey = [][2]string{
	{"paper", "main"},
	{"gamble", "gamble"},
	{"emaonly", "emaonly"},
	{"conv", "conv"},
	{"meanrev", "meanrev"},
	{"bollema", "bollema"},
	{"pulsar", "pulsar"},
	{"pulsarv3", "pulsarv3"},
	{"pulsarv5", "pulsarv5"},
	{"pulsarv6", "pulsarv6"},
	{"orderblock", "orderblock"},
	{"orderblockv2", "orderblockv2"},
}

// StratTodayRow 是排行榜的一列(對應前端 cmprow)。
type StratTodayRow struct {
	Tab      string  `json:"tab"`
	Label    string  `json:"label"`     // 中文名(來自 tabMeta)
	Tier     string  `json:"tier"`      // 該策略目前的最低可見角色
	TotalPnl float64 `json:"total_pnl"` // 今日累計損益 %(平倉)
	WinRate  float64 `json:"win_rate"`  // 今日勝率 0..100
	Closed   int     `json:"closed"`    // 今日平倉筆數
}

// tabLabel 由 tabMeta 查中文名(查不到回 tab 本身)。
func tabLabel(tab string) string {
	for _, t := range tabMeta {
		if t.tab == tab {
			return t.label
		}
	}
	return tab
}

// StrategyToday 回「今日(本機午夜→現在)已平倉」各策略的表現,依 role 可見範圍過濾,
// 依今日累計損益由高到低排序;今日沒有平倉的策略不列入。呼叫端自行取前三。
func (s *Store) StrategyToday(role string) []StratTodayRow {
	if s.db == nil {
		return []StratTodayRow{}
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	winMs := now.UnixMilli() - midnight.UnixMilli()
	if winMs <= 0 {
		return []StratTodayRow{}
	}

	out := []StratTodayRow{}
	for _, tk := range stratTabKey {
		tab, key := tk[0], tk[1]
		tier := s.TabRole(tab)
		if !auth.AtLeast(role, tier) {
			continue // 使用者層級看不到這個策略 → 不列入
		}
		st := s.strategyHistFull(key, winMs).stats
		if st.Closed == 0 {
			continue // 今日沒有平倉
		}
		out = append(out, StratTodayRow{
			Tab:      tab,
			Label:    tabLabel(tab),
			Tier:     tier,
			TotalPnl: st.TotalPnl,
			WinRate:  st.WinRate,
			Closed:   st.Closed,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TotalPnl > out[j].TotalPnl })
	return out
}
