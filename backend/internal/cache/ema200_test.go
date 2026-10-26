package cache

import (
	"testing"
	"time"

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

// 沒有 DB 時,穿越紀錄分頁退回記憶體:總數、頁數、頁碼夾緊、時間範圍都要對。
func TestEMA200HistoryPaging(t *testing.T) {
	s := &Store{ema200: newEMA200Engine()}
	now := time.Now().UTC()
	for i := 0; i < 120; i++ { // 新的在前,每筆差 1 小時
		s.ema200.events = append(s.ema200.events, EMA200Event{Coin: "C", Dir: "up", BarTime: now.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339)})
	}
	r := s.EMA200History(0, 3, 50)
	if r.Total != 120 || r.Pages != 3 || r.Page != 3 || len(r.Rows) != 20 {
		t.Errorf("第 3 頁: total=%d pages=%d page=%d rows=%d", r.Total, r.Pages, r.Page, len(r.Rows))
	}
	if r := s.EMA200History(0, 9, 50); r.Page != 3 {
		t.Errorf("超出頁數應夾到最後一頁, got %d", r.Page)
	}
	if r := s.EMA200History(int64(24*time.Hour/time.Millisecond), 1, 50); r.Total != 24 && r.Total != 25 {
		t.Errorf("最近 24h 應約 24 筆, got %d", r.Total)
	}
	if r := (&Store{}).EMA200History(0, 1, 50); r.Total != 0 || r.Rows == nil {
		t.Error("沒有引擎也要回空陣列")
	}
}
