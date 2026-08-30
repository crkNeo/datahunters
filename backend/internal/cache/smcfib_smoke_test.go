package cache

import (
	"math"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// 造一段:盤整 → 起漲(結構破壞,產生 bullish 訂單塊)→ 衝高(fib1)→ 回撤到訂單塊附近 →
// 最後一根收成頭槌且低點落在 0.142-0.382 區間。驗證 smcFibSignal 進場、fib 幾何、四段 TP。
func TestSMCFibLongSetup(t *testing.T) {
	var cs []exchange.Candle
	ts := int64(1_700_000_000_000)
	push := func(o, h, l, c float64) {
		cs = append(cs, exchange.Candle{Ts: ts, Open: o, High: h, Low: l, Close: c, Volume: 1000})
		ts += 900_000
	}
	// 60 根暖機(ATR/swing 基線)
	for i := 0; i < 60; i++ {
		push(100, 100.3, 99.7, 100)
	}
	// 上升到峰值 105 → 建立 swing high pivot
	push(100, 101, 100, 101)
	push(101, 102, 101, 102)
	push(102, 103, 102, 103)
	push(103, 104, 103, 104)
	push(104, 105, 104, 104.8) // 峰值高 105
	// 回撤 5 根,高點都 < 105,第一根是需求訂單塊(大陰、低點 96 = fib0)
	push(104.5, 104.6, 96, 98)
	push(98, 99, 97.5, 98.5)
	push(98.5, 100, 98, 99.5)
	push(99.5, 100.5, 99, 100)
	push(100, 100.8, 99.5, 100.5)
	// 突破:收盤 > 105 → bullish 結構破壞 → 需求訂單塊(下緣≈96)
	push(101, 106.5, 101, 106)
	// 續攻到 120(fib1)
	push(106, 110, 106, 109.5)
	push(109.5, 115, 109, 114.5)
	push(114.5, 120, 114, 119)
	// 高檔盤整 > 10 根,把低價 K 推出頭槌的局部低點視窗(pinCtxBars=10)
	for i := 0; i < 12; i++ {
		push(119, 120, 118, 119)
	}
	// 回撤到訂單塊附近。r=fib1-fib0=120-96=24;進場區 0.142→99.4、0.382→105.2。
	push(118, 118.5, 108, 109)
	// 頭槌:小實體、長下影絕對主導、低點 101 為局部低點且落在 [99.4,105.2] 區間
	push(104, 105, 101.0, 104.5)

	dir, entry, sl, tp, ok := smcFibSignal(cs)
	if !ok {
		t.Fatalf("smcFibSignal 沒進場(預期做多)")
	}
	if dir != "long" {
		t.Fatalf("方向錯:want long got %s", dir)
	}
	// 從 sl/tp 還原斐波格,核對 fib0≈96、fib1≈120
	r := (tp - sl) / (smcFibFinalTP + smcFibSL)
	fib0 := sl + smcFibSL*r
	fib1 := fib0 + r
	t.Logf("進場 dir=%s entry=%.2f sl=%.2f tp=%.2f | fib0=%.2f fib1=%.2f r=%.2f", dir, entry, sl, tp, fib0, fib1, r)
	if math.Abs(fib0-96) > 1.5 {
		t.Errorf("fib0 應≈96(訂單塊下緣),got %.2f", fib0)
	}
	if math.Abs(fib1-120) > 1.5 {
		t.Errorf("fib1 應≈120(回撤前最高),got %.2f", fib1)
	}
	// 四段 TP:tp1=fib1.0、tp2=fib1.382、tp3=fib1.618、tp(final)=fib2.0
	tp1, tp2, tp3 := smcFibTPLevels(entry, sl, tp)
	wantTP1 := fib0 + 1.0*r
	wantTP2 := fib0 + 1.382*r
	wantTP3 := fib0 + 1.618*r
	t.Logf("四段 TP: TP1=%.2f TP2=%.2f TP3=%.2f TP4(final)=%.2f", tp1, tp2, tp3, tp)
	if math.Abs(tp1-wantTP1) > 0.5 || math.Abs(tp2-wantTP2) > 0.5 || math.Abs(tp3-wantTP3) > 0.5 {
		t.Errorf("四段 TP 還原不符: got %.2f/%.2f/%.2f want %.2f/%.2f/%.2f", tp1, tp2, tp3, wantTP1, wantTP2, wantTP3)
	}
	if !(sl < entry && entry < tp1 && tp1 < tp2 && tp2 < tp3 && tp3 < tp) {
		t.Errorf("價位順序不對: sl=%.2f entry=%.2f tp1=%.2f tp2=%.2f tp3=%.2f tp=%.2f", sl, entry, tp1, tp2, tp3, tp)
	}
}

// 驗證四段 stepTP:TP1→保本、TP2→TP1、TP3→TP2、最終段收剩下,Legs 0→4。
func TestSMCFib4LegStepTP(t *testing.T) {
	now := time.Now()
	tr := &PaperTrade{
		Dir: "long", Entry: 100, Status: "open",
		TP1: 110, TP2: 113, TP3: 116, TP: 120, SL: 98,
	}
	p := &tpPlan{a: 0.4, b: 0.7, w1: 0.25, w2: 0.25, w3: 0.25, beBuf: 0.0005}

	stepTP(tr, 110, p, true, now) // TP1
	if tr.Legs != 1 || tr.SL <= 100 {
		t.Fatalf("TP1 後應 Legs=1、SL 移到保本(>100),got Legs=%d SL=%.4f", tr.Legs, tr.SL)
	}
	stepTP(tr, 113, p, true, now) // TP2
	if tr.Legs != 2 || tr.SL != 110 {
		t.Fatalf("TP2 後應 Legs=2、SL=TP1(110),got Legs=%d SL=%.4f", tr.Legs, tr.SL)
	}
	stepTP(tr, 116, p, true, now) // TP3
	if tr.Legs != 3 || tr.SL != 113 {
		t.Fatalf("TP3 後應 Legs=3、SL=TP2(113),got Legs=%d SL=%.4f", tr.Legs, tr.SL)
	}
	if math.Abs(tr.Filled-0.75) > 1e-9 {
		t.Fatalf("三段分批後應平 75%%,got %.4f", tr.Filled)
	}
	stepTP(tr, 120, p, true, now) // 最終段
	if tr.Legs != 4 || tr.Status != "closed" {
		t.Fatalf("最終段後應 Legs=4、closed,got Legs=%d status=%s", tr.Legs, tr.Status)
	}
}
