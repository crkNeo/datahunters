package cache

import (
	"testing"

	"datahunter/internal/exchange"
)

// 錘頭線 / 射擊星:形狀(小實體、主影線遠大於實體且絕對主導反向影線)+ 位置脈絡
// (錘頭需當根 low < 前 10 根最低;射擊星需當根 high > 前 10 根最高)。反向可帶一點點影線。
func TestPinKind(t *testing.T) {
	// 10 根脈絡棒:low 都 ≥100、high 都 ≤105。錘頭要破 100、射擊星要破 105 才算局部低/高點。
	ctx := func() []exchange.Candle {
		out := make([]exchange.Candle, 0, pinCtxBars)
		for i := 0; i < pinCtxBars; i++ {
			out = append(out, exchange.Candle{Open: 102, High: 105, Low: 100, Close: 103})
		}
		return out
	}
	cases := []struct {
		name       string
		o, h, l, c float64
		want       string
	}{
		// 錘頭:小實體(100→100.5)、長下影(→95)、帶一點上影(→100.8)、low 95 < 前5最低100
		{"錘頭·帶一點上影+局部低點", 100, 100.8, 95, 100.5, "hammer"},
		// 射擊星鏡像:high 110 > 前5最高105
		{"射擊星·帶一點下影+局部高點", 105, 110, 104.2, 104.5, "star"},
		// 錘頭形狀(小實體+長下影)但 low 100.5 沒破前10最低 100 → 不算
		{"錘頭形狀但非局部低點", 104, 104.4, 100.5, 104.2, ""},
		// 射擊星形狀(小實體+長上影)但 high 104.5 沒破前10最高 105 → 不算
		{"射擊星形狀但非局部高點", 101, 104.5, 100.3, 100.5, ""},
		// 大實體 → 不算
		{"大實體不算", 100, 105, 95, 104, ""},
		// 雙長影無主導 → 不算
		{"雙長影不算", 100, 106, 94, 100, ""},
		// 蜻蜓十字在局部低點 → 算錘頭
		{"蜻蜓十字+局部低點算錘頭", 100, 100.1, 95, 100, "hammer"},
	}
	for _, tc := range cases {
		cs := append(ctx(), exchange.Candle{Open: tc.o, High: tc.h, Low: tc.l, Close: tc.c})
		if got := pinKind(cs); got != tc.want {
			t.Errorf("%s: pinKind = %q, want %q", tc.name, got, tc.want)
		}
	}
	// 脈絡不足(< pinCtxBars+1 根)→ 不算
	if got := pinKind([]exchange.Candle{{Open: 100, High: 100.2, Low: 95, Close: 100}}); got != "" {
		t.Errorf("脈絡不足應回空,得 %q", got)
	}
}

// to4h:4 根 1h 湊成 1 根 4h;不足 4 根的進行中窗口丟掉。
func TestTo4h(t *testing.T) {
	const h = int64(3600000)
	cs := []exchange.Candle{
		{Ts: 0, Open: 10, High: 12, Low: 9, Close: 11},
		{Ts: h, Open: 11, High: 15, Low: 10, Close: 14},
		{Ts: 2 * h, Open: 14, High: 14, Low: 8, Close: 9},
		{Ts: 3 * h, Open: 9, High: 11, Low: 7, Close: 10},
		{Ts: 4 * h, Open: 10, High: 20, Low: 10, Close: 18}, // 下一窗口只有 1 根 → 丟
	}
	agg := to4h(cs)
	if len(agg) != 1 {
		t.Fatalf("應只有 1 根完整 4h,得 %d", len(agg))
	}
	if g := agg[0]; g.Open != 10 || g.Close != 10 || g.High != 15 || g.Low != 7 {
		t.Errorf("4h OHLC 錯誤: %+v (want O10 H15 L7 C10)", g)
	}
}
