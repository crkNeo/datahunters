package cache

import (
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// drBars 造 n 根 1h K(量 100、價 10 附近),最後一根可另外指定。
func drBars(start int64, n int) []exchange.Candle {
	cs := make([]exchange.Candle, n)
	for i := range cs {
		cs[i] = exchange.Candle{Ts: start + int64(i)*3600_000, Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 100}
	}
	return cs
}

func TestDumpRevHit(t *testing.T) {
	cs := drBars(0, 60)
	last := &cs[len(cs)-1]
	*last = exchange.Candle{Ts: last.Ts, Open: 10, High: 10, Low: 8.5, Close: 9.8, Volume: 1000} // 10 倍量、跌 2%
	if dir, entry, sl, tp, ok := dumpRevSignal(cs); !ok || dir != "long" || entry != 9.8 || sl != 0 || tp != 0 {
		t.Fatalf("should trigger long at close with no SL/TP: %v %v %v %v %v", dir, entry, sl, tp, ok)
	}
	last.Volume = 999 // 不到 10 倍
	if dumpRevHit(cs) {
		t.Fatal("volume < 10x should not trigger")
	}
	last.Volume, last.Close = 1000, 9.95 // 跌 0.5%
	if dumpRevHit(cs) {
		t.Fatal("drop < 1% should not trigger")
	}
	last.Close = 10.2 // 陽線
	if dumpRevHit(cs) {
		t.Fatal("green bar should not trigger")
	}
}

func TestDumpRevBookLifecycle(t *testing.T) {
	s := &Store{}
	b := s.newDumpRevBook()
	base := (time.Now().Unix()/3600 - 200) * 3600_000
	cs := drBars(base, 60)
	cs[59] = exchange.Candle{Ts: cs[59].Ts, Open: 10, High: 10, Low: 8, Close: 9, Volume: 2000}
	now := time.UnixMilli(cs[59].Ts + 3600_000)
	if r := s.microRun(b, "AAA", cs, now); r != "" {
		t.Fatalf("should open, got %q", r)
	}
	tr := b.trades[0]
	if tr.Entry != 9 || tr.SL != 0 || tr.TP != 0 || !tr.OpenTime.Equal(now) {
		t.Fatalf("bad trade: %+v", tr)
	}
	// 之後一路崩到 1 塊:沒有止損,不能出場
	all := cs
	for i := 1; i < drHoldBars; i++ {
		ts := cs[59].Ts + int64(i)*3600_000
		all = append(all, exchange.Candle{Ts: ts, Open: 5, High: 5, Low: 1, Close: 2, Volume: 100})
		s.microRun(b, "AAA", all, time.UnixMilli(ts+3600_000))
		if tr.Status != "open" {
			t.Fatalf("closed early at bar %d (%s)", i, tr.Outcome)
		}
	}
	// 第 48 根收盤 = 進場後 48 小時 → 計時出場
	ts := cs[59].Ts + int64(drHoldBars)*3600_000
	all = append(all, exchange.Candle{Ts: ts, Open: 12, High: 12, Low: 11, Close: 11.7, Volume: 100})
	s.microRun(b, "AAA", all, time.UnixMilli(ts+3600_000))
	if tr.Status != "closed" || tr.Outcome != "expired" || tr.Cur != 11.7 || tr.PnLPct < 29.9 {
		t.Fatalf("should time-exit at +48h close: %+v", tr)
	}
	// 同時持有上限 3
	b2 := s.newDumpRevBook()
	for i, c := range []string{"B1", "B2", "B3", "B4"} {
		r := s.microRun(b2, c, cs, now)
		if (i < 3) != (r == "") {
			t.Fatalf("coin %d: open=%v reason=%q", i, r == "", r)
		}
	}
}

func TestDumpRevUniverse(t *testing.T) {
	s := &Store{coins: []string{"BTC", "ETH", "AAA", "SOL", "BNB", "BBB", "NVDA"},
		symTypes: map[string]string{"NVDA": "EQUITY"}, symTime: time.Now()} // 代幣化股票也排除
	if got := s.dumpRevUniverse(); len(got) != 2 || got[0] != "AAA" || got[1] != "BBB" {
		t.Fatalf("universe = %v", got)
	}
}
