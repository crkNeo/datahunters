package polymarket

import "testing"

func win(p string, rank int, pnl, vol float64) Window {
	return Window{Period: p, Rank: rank, PnL: pnl, Vol: vol, HasData: true}
}

func TestAssess(t *testing.T) {
	th := Defaults()
	cases := []struct {
		name string
		w    map[string]Window
		want string
	}{
		{"長期穩定", map[string]Window{
			"DAY": win("DAY", 5, 100, 5000), "WEEK": win("WEEK", 5, 500, 20000),
			"MONTH": win("MONTH", 5, 2000, 80000), "ALL": win("ALL", 5, 10000, 500000),
		}, "長期穩定"}, // recentShare 0.2、皆獲利
		{"短期爆發", map[string]Window{
			"DAY": win("DAY", 3, 200, 5000), "WEEK": win("WEEK", 3, 800, 20000),
			"MONTH": win("MONTH", 3, 5000, 80000), "ALL": win("ALL", 3, 3000, 500000),
		}, "短期爆發"}, // MONTH(5000) > ALL(3000) → 之前曾虧
		{"近期轉弱", map[string]Window{
			"DAY": win("DAY", 9, -50, 5000), "WEEK": win("WEEK", 9, -200, 20000),
			"MONTH": win("MONTH", 9, -1000, 80000), "ALL": win("ALL", 9, 8000, 500000),
		}, "近期轉弱"}, // ALL 賺 MONTH 虧
		{"資料不足", map[string]Window{
			"ALL": win("ALL", 0, 50, 100), // 量低 + 只有一個區間
		}, "資料不足"},
	}
	for _, c := range cases {
		got := Assess("0xabc", c.name, c.w, th).Verdict
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
