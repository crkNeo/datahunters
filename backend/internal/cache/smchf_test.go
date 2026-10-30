package cache

import (
	"math"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// shfLongBars:擺動高 110(第 45 根)→ 盤整 → 第 57 根陰線(OB,中點 103)→ 第 59 根收 111 突破(BOS),
// 且第 59 根低點 105 > 第 57 根高點 104.5 → 與 OB 重疊的多方 FVG。
func shfLongBars() []exchange.Candle {
	cs := make([]exchange.Candle, 60)
	for i := range cs {
		cs[i] = exchange.Candle{Ts: int64(i) * shfBarMs, Open: 100, Close: 100, High: 101, Low: 99}
	}
	cs[45] = exchange.Candle{Ts: 45 * shfBarMs, Open: 100, Close: 105, High: 110, Low: 100}
	for i := 46; i <= 56; i++ {
		cs[i] = exchange.Candle{Ts: int64(i) * shfBarMs, Open: 102, Close: 102, High: 104, Low: 100}
	}
	cs[57] = exchange.Candle{Ts: 57 * shfBarMs, Open: 104, Close: 102, High: 104.5, Low: 101.5}
	cs[58] = exchange.Candle{Ts: 58 * shfBarMs, Open: 102, Close: 103, High: 104, Low: 101.8}
	cs[59] = exchange.Candle{Ts: 59 * shfBarMs, Open: 103, Close: 111, High: 112, Low: 105}
	return cs
}

func TestSMCHFBuild(t *testing.T) {
	cs := shfLongBars()
	p, ok := smcHFBuild(cs, nil)
	if !ok || p.Dir != "long" || p.Entry != 103 || p.ZoneTop != 104.5 || p.ZoneBt != 101.5 {
		t.Fatalf("long plan wrong: %+v %v", p, ok)
	}
	atr := atrSeries(cs, 14)[59]
	if math.Abs(p.SL-(101.5-0.15*atr)) > 1e-9 {
		t.Fatalf("SL = %v, want OB low − 0.15 ATR", p.SL)
	}
	// 鏡像成空單:價格 200 − p,高低互換
	m := make([]exchange.Candle, len(cs))
	for i, c := range cs {
		m[i] = exchange.Candle{Ts: c.Ts, Open: 200 - c.Open, Close: 200 - c.Close, High: 200 - c.Low, Low: 200 - c.High}
	}
	ps, ok := smcHFBuild(m, nil)
	if !ok || ps.Dir != "short" || ps.Entry != 97 || ps.SL <= ps.ZoneTop {
		t.Fatalf("short plan wrong: %+v %v", ps, ok)
	}
	// 前一根就已經在擺動高之上(不是「剛突破」)→ 不算 BOS
	cs[58].Close = 110.5
	if _, ok := smcHFBuild(cs, nil); ok {
		t.Fatal("not a fresh break, should not plan")
	}
}

func TestSMCHFProcess(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	plan := func(coin string, age time.Duration) *PaperTrade {
		return &PaperTrade{ID: coin, Coin: coin, Dir: "long", Entry: 100, SL: 98, Status: "pending", OpenTime: now.Add(-age)}
	}
	b := newSMCHFBook()
	b.trades = []*PaperTrade{plan("A", time.Hour), plan("B", time.Hour), plan("C", 11*time.Hour), plan("D", time.Hour)}
	px := map[string]float64{"A": 99.95, "B": 99.5, "C": 99.95, "D": 97.9}
	opened, _, _ := smcHFProcess(b, px, now)
	if len(opened) != 1 || opened[0].Coin != "A" {
		t.Fatalf("only A should fill (B too far past mid, C expired, D hit SL first): %+v", opened)
	}
	if len(b.trades) != 2 { // A(持倉)+ B(還在等)
		t.Fatalf("C / D should be cancelled, left %d", len(b.trades))
	}
	a := opened[0]
	if a.Entry != 99.95 || math.Abs(a.TP1-(99.95+1.95)) > 0.01 || math.Abs(a.TP-(99.95+1.5*1.95)) > 0.01 || a.SL != 98 {
		t.Fatalf("R must be recomputed from the fill: %+v", a)
	}
	// TP1 → 平一半,止損不動;接著打到原止損 → 剩下一半虧 1R,總共約 0(不是 +1R)
	_, tp1, _ := smcHFProcess(b, map[string]float64{"A": 102}, now)
	if len(tp1) != 1 || a.Legs != 1 || a.SL != 98 {
		t.Fatalf("TP1 should fill without moving SL: %+v", a)
	}
	_, _, closed := smcHFProcess(b, map[string]float64{"A": 97.5}, now)
	if len(closed) != 1 || a.Outcome != "tp1stop" || math.Abs(a.PnLPct) > 0.01 {
		t.Fatalf("TP1 then stop should net ≈ 0: %+v", a)
	}
	// 一路到 TP2:0.5 × 1R + 0.5 × 1.5R = 1.25R
	c := &PaperTrade{Coin: "E", Dir: "long", Status: "pending", Entry: 100, SL: 98, OpenTime: now}
	b2 := newSMCHFBook()
	b2.trades = []*PaperTrade{c}
	smcHFProcess(b2, map[string]float64{"E": 100}, now)
	smcHFProcess(b2, map[string]float64{"E": 103.5}, now)
	if c.Status != "closed" || c.Outcome != "tp2" || math.Abs(c.PnLPct-2.5) > 0.01 {
		t.Fatalf("TP2 should net 1.25R (2.5%%): %+v", c)
	}
}

func TestSMCHFMaxOpen(t *testing.T) {
	now := time.Now().UTC()
	b := newSMCHFBook()
	px := map[string]float64{}
	for i := 0; i < 10; i++ {
		coin := string(rune('A' + i))
		b.trades = append(b.trades, &PaperTrade{ID: coin, Coin: coin, Dir: "long", Entry: 100, SL: 98, Status: "pending", OpenTime: now})
		px[coin] = 100
	}
	opened, _, _ := smcHFProcess(b, px, now)
	if len(opened) != shfMaxOpen {
		t.Fatalf("at most %d positions, got %d", shfMaxOpen, len(opened))
	}
}
