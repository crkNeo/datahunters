package cache

import (
	"testing"

	"datahunter/internal/exchange"
)

// 5 根平淡的脈絡棒(low 都 ≥100、high 都 ≤105),供插針棒接在後面。
func pinCtx() []exchange.Candle {
	return []exchange.Candle{
		{Open: 102, High: 104, Low: 100, Close: 103},
		{Open: 103, High: 105, Low: 101, Close: 102},
		{Open: 102, High: 104, Low: 100, Close: 103},
		{Open: 103, High: 105, Low: 101, Close: 104},
		{Open: 104, High: 105, Low: 102, Close: 103},
	}
}

func TestPinKindHammer(t *testing.T) {
	// 錘子:下影 ≥ 2×實體、上影 ≤ 0.5×實體、當根 low < 前5根最低(100)
	// body=0.5, lower=4.5(≥1.0), upper=0.2(≤0.25), low=95<100 ✓
	cs := append(pinCtx(), exchange.Candle{Open: 99.5, High: 100.2, Low: 95, Close: 100})
	if k := pinKind(cs); k != "hammer" {
		t.Errorf("應判為 hammer,得 %q", k)
	}
}

func TestPinKindStar(t *testing.T) {
	// 流星:上影 ≥ 2×實體、下影 ≤ 0.5×實體、當根 high > 前5根最高(105)
	// body=0.5, upper=4.5(≥1.0), lower=0.2(≤0.25), high=110>105 ✓
	cs := append(pinCtx(), exchange.Candle{Open: 105.5, High: 110, Low: 104.8, Close: 105})
	if k := pinKind(cs); k != "star" {
		t.Errorf("應判為 star,得 %q", k)
	}
}

func TestPinKindRejects(t *testing.T) {
	// 一般棒(無長影)→ 不命中
	cs := append(pinCtx(), exchange.Candle{Open: 103, High: 104, Low: 102, Close: 103.5})
	if k := pinKind(cs); k != "" {
		t.Errorf("一般棒不該命中,得 %q", k)
	}
	// 錘子形狀但沒在局部低點(low 沒破前5最低)→ 不命中
	cs2 := append(pinCtx(), exchange.Candle{Open: 102.5, High: 103.2, Low: 100.5, Close: 103})
	if k := pinKind(cs2); k != "" {
		t.Errorf("非局部低點不該判 hammer,得 %q", k)
	}
	// 純十字星(實體=0)→ 跳過
	cs3 := append(pinCtx(), exchange.Candle{Open: 100, High: 100.1, Low: 95, Close: 100})
	if k := pinKind(cs3); k != "" {
		t.Errorf("十字星應跳過,得 %q", k)
	}
	// 脈絡不足(< 6 根)→ 不命中
	if k := pinKind([]exchange.Candle{{Open: 99.5, High: 100, Low: 95, Close: 100}}); k != "" {
		t.Errorf("脈絡不足不該命中,得 %q", k)
	}
}

// to4h:4 根 1h 湊成 1 根 4h;不足 4 根的進行中窗口丟掉。
func TestTo4h(t *testing.T) {
	const h = int64(3600000)
	cs := []exchange.Candle{
		{Ts: 0, Open: 10, High: 12, Low: 9, Close: 11},
		{Ts: h, Open: 11, High: 15, Low: 10, Close: 14},
		{Ts: 2 * h, Open: 14, High: 14, Low: 8, Close: 9},
		{Ts: 3 * h, Open: 9, High: 11, Low: 7, Close: 10}, // 收 10 → 完成第一根 4h
		{Ts: 4 * h, Open: 10, High: 20, Low: 10, Close: 18}, // 下一個 4h 窗口,只有 1 根 → 丟
	}
	agg := to4h(cs)
	if len(agg) != 1 {
		t.Fatalf("應只有 1 根完整 4h,得 %d", len(agg))
	}
	g := agg[0]
	if g.Open != 10 || g.Close != 10 || g.High != 15 || g.Low != 7 {
		t.Errorf("4h OHLC 錯誤: %+v (want O10 H15 L7 C10)", g)
	}
}
