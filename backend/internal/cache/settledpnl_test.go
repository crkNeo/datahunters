package cache

import (
	"math"
	"testing"
)

// 驗證『鎖住的獲利階梯』顯示口徑(settledPnL):entry=100、TP1=105(5%)/TP2=110(10%)/
// TP3=115(15%)/最終=120(20%)、原始止損 98(-2%)。
func TestSettledPnLLadder(t *testing.T) {
	mk := func(legs int, tp3 float64) *PaperTrade {
		return &PaperTrade{Dir: "long", Entry: 100, TP1: 105, TP2: 110, TP3: tp3, TP: 120, Legs: legs}
	}
	near := func(got, want float64) bool { return math.Abs(got-want) < 0.01 }

	cases := []struct {
		name  string
		tr    *PaperTrade
		price float64
		want  float64
	}{
		// 已達到的那一階本身(不降一階)
		{"TP1後保本被打→顯示TP1", mk(1, 115), 100.05, 5},   // 保本≈0% → 顯示 TP1 5%
		{"TP2後回踩TP1出場→顯示TP2", mk(2, 115), 105, 10},  // 回踩 TP1 但已達 TP2 → 10%
		{"TP3後回踩TP2出場→顯示TP3", mk(3, 115), 110, 15},  // 回踩 TP2 但已達 TP3 → 15%
		// 邊界
		{"吃到最終TP→顯示最終階", mk(4, 115), 120, 20},        // 一路到 fib2.0 那階 → 20%
		{"純止損未達任何TP→原始虧損", mk(0, 0), 98, -2},        // Legs=0 → -2%
		{"持倉中往上跑(Legs2, 現價112)→顯示實際到價", mk(2, 115), 112, 12}, // base 12% > 已達 TP2 10%
		{"三段書最終(Legs3, TP3=0)→回退最終階", mk(3, 0), 120, 20},    // TP3 未設 → tpAtLeg 回退 tr.TP
	}
	for _, c := range cases {
		got := settledPnL(c.tr, c.price)
		if !near(got, c.want) {
			t.Errorf("%s: got %.2f%% want %.2f%%", c.name, got, c.want)
		}
	}
}

// 做空對稱:entry=100、TP1=95(5%)/TP2=90(10%)、最終=80。
func TestSettledPnLShort(t *testing.T) {
	tr := &PaperTrade{Dir: "short", Entry: 100, TP1: 95, TP2: 90, TP3: 85, TP: 80, Legs: 1}
	if got := settledPnL(tr, 100.05); math.Abs(got-5) > 0.1 { // 保本被打 → 顯示 TP1 5%
		t.Errorf("空單保本被打應顯示 TP1 5%%,got %.2f", got)
	}
	tr.Legs = 3
	if got := settledPnL(tr, 90); math.Abs(got-15) > 0.01 { // 已達 TP3(85=15%),回踩 TP2 出場 → 顯示 TP3 15%
		t.Errorf("空單已達 TP3 應顯示 15%%,got %.2f", got)
	}
}
