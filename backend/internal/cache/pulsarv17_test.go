package cache

import (
	"testing"
	"time"
)

func v17Good() *PaperTrade {
	return &PaperTrade{ID: "pulsarv12|AAA|1", Coin: "AAA", Dir: "long", Entry: 10, SL: 9.5, TP: 12.5, Status: "open",
		OpenTime: time.Now().UTC(), CVD1h: 8, LongAcct: 0.65, Chg24: 5, Ema200Dist: 4, BTC15m: 0.02, BTC1h: 0.1}
}

func TestV17Block(t *testing.T) {
	if why := v17Block(v17Good()); len(why) != 0 {
		t.Fatalf("all filters should pass: %v", why)
	}
	cases := []struct {
		name string
		mod  func(*PaperTrade)
	}{
		{"A", func(t *PaperTrade) { t.CVD1h = 13.5 }},
		{"B", func(t *PaperTrade) { t.LongAcct = 0.6 }},
		{"B 沒資料", func(t *PaperTrade) { t.LongAcct = 0 }},
		{"C", func(t *PaperTrade) { t.Chg24 = 2.9 }},
		{"D", func(t *PaperTrade) { t.Ema200Dist = -0.5 }},
		{"F", func(t *PaperTrade) { t.BTC15m = 0.08 }},
		{"G", func(t *PaperTrade) { t.BTC1h = 0.25 }},
	}
	for _, c := range cases {
		tr := v17Good()
		c.mod(tr)
		if why := v17Block(tr); len(why) != 1 {
			t.Fatalf("%s should block exactly once: %v", c.name, why)
		}
	}
	// 邊界:剛好等於門檻 → 通過(A ≤13、B ≥0.61、C ≥3、D ≥0、F ≤0.07、G ≤0.2)
	tr := v17Good()
	tr.CVD1h, tr.LongAcct, tr.Chg24, tr.Ema200Dist, tr.BTC15m, tr.BTC1h = 13, 0.61, 3, 0, 0.07, 0.2
	if why := v17Block(tr); len(why) != 0 {
		t.Fatalf("thresholds are inclusive: %v", why)
	}
}

func TestV17Follow(t *testing.T) {
	s := &Store{}
	s.pulsarV12Book = &microBook{name: "pulsarv12"}
	s.pulsarV17Book = s.newPulsarV17Book()
	src := v17Good()
	s.pulsarV17Follow(src)
	b := s.pulsarV17Book
	if len(b.trades) != 1 {
		t.Fatalf("should follow, got %d", len(b.trades))
	}
	tr := b.trades[0]
	if tr.Coin != "AAA" || tr.Entry != 10 || tr.SL != 9.5 || !tr.OpenTime.Equal(src.OpenTime) || tr.LongAcct != 0.65 || tr.ID == src.ID {
		t.Fatalf("twin should copy entry / tags with its own id: %+v", tr)
	}
	s.pulsarV17Follow(src) // 同幣已持有 → 不重複
	bad := v17Good()
	bad.Coin, bad.Chg24 = "BBB", 1
	s.pulsarV17Follow(bad) // 濾網沒過 → 不跟
	if len(b.trades) != 1 {
		t.Fatalf("no duplicate / filtered trades, got %d", len(b.trades))
	}
	// 自己不找進場:收盤時宇宙是空的、訊號永遠不成立
	if u := b.universe(); len(u) != 0 {
		t.Fatal("v17 must not scan its own universe")
	}
	if _, _, _, _, ok := b.signal(nil); ok {
		t.Fatal("v17 must never signal on its own")
	}
}
