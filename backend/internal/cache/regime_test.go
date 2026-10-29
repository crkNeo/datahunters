package cache

import (
	"fmt"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

func TestEMA200Dist(t *testing.T) {
	cs := make([]exchange.Candle, e2MinBars)
	for i := range cs {
		cs[i] = exchange.Candle{Close: 100}
	}
	if d, ok := EMA200Dist(cs, 110); !ok || d != 10 {
		t.Fatalf("dist = %v %v, want 10", d, ok)
	}
	if _, ok := EMA200Dist(cs[:100], 110); ok {
		t.Fatal("too few bars should fail")
	}
}

func TestTagRegime(t *testing.T) {
	lastBar := (time.Now().UnixMilli()/e2BarMs - 1) * e2BarMs
	e := newEMA200Engine()
	for i := 0; i < 60; i++ { // 60 個幣:45 個在 EMA200 之上 → 75%
		c := 90.0
		if i < 45 {
			c = 110
		}
		e.coins[fmt.Sprintf("C%d", i)] = e2Coin{bar: lastBar, close: c, ema: 100}
	}
	e.coins["BTC"] = e2Coin{bar: lastBar, close: 103, ema: 100}
	e.coins["AAA"] = e2Coin{bar: lastBar, close: 50, ema: 40}
	s := &Store{ema200: e}
	if p, ok := s.ema200Breadth(); !ok || p != 75.81 { // 62 個裡 47 個在上方(45 + BTC + AAA)
		t.Fatalf("breadth = %v %v", p, ok)
	}
	b := &microBook{name: "pulsarv16"}
	tr := &PaperTrade{Coin: "AAA", Entry: 44}
	s.tagRegime(b, tr)
	if tr.Ema200Dist != 10 || tr.BtcEma200 != 3 || tr.Breadth != 75.81 {
		t.Fatalf("regime tags wrong: %+v", tr)
	}
}
