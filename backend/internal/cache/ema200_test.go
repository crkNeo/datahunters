package cache

import (
	"testing"

	"datahunter/internal/exchange"
)

// e2Series 造 n 根 4H K:前段收盤 base、最後 tail 根改成 tailPx(用來做出穿越)。
func e2Series(n int, base float64, tail int, tailPx float64) []exchange.Candle {
	cs := make([]exchange.Candle, n)
	for i := range cs {
		c := base
		if i >= n-tail {
			c = tailPx
		}
		cs[i] = exchange.Candle{Ts: int64(i) * e2BarMs, Open: c, High: c, Low: c, Close: c}
	}
	return cs
}

func TestEMA200State(t *testing.T) {
	// 一直在 100,最後一根拉到 110 → 最新這根上穿
	st, ok := ema200State(e2Series(250, 100, 1, 110))
	if !ok || st.crossDir != "up" || st.barsSince != 0 || st.crossBar != 249*e2BarMs || !(st.close > st.ema) {
		t.Fatalf("最新一根應是上穿: %+v ok=%v", st, ok)
	}
	// 緩漲(收盤一直在 EMA 之上),最後一根掉到 90 → 最新這根下穿
	rising := make([]exchange.Candle, 250)
	for i := range rising {
		rising[i] = exchange.Candle{Ts: int64(i) * e2BarMs, Close: 100 + float64(i)*0.05}
	}
	rising[249].Close = 90
	if st, _ := ema200State(rising); st.crossDir != "down" || st.barsSince != 0 {
		t.Errorf("最新一根應是下穿: %+v", st)
	}
	// 最後 3 根都在 110 → 穿越發生在 2 根前,最新這根不是剛穿越
	if st, _ := ema200State(e2Series(250, 100, 3, 110)); st.crossDir != "up" || st.barsSince != 2 {
		t.Errorf("穿越應在 2 根前: %+v", st)
	}
	// 一路緩漲、從沒跌破 → 沒有穿越
	cs := make([]exchange.Candle, 250)
	for i := range cs {
		cs[i] = exchange.Candle{Ts: int64(i) * e2BarMs, Close: 100 + float64(i)}
	}
	if st, _ := ema200State(cs); st.crossDir != "" || st.crossBar != 0 {
		t.Errorf("一路在 EMA 之上不該有穿越: %+v", st)
	}
	// K 線不夠(上市太新)→ 不算
	if _, ok := ema200State(e2Series(e2MinBars-1, 100, 1, 110)); ok {
		t.Error("K 線不足不該算")
	}
}
