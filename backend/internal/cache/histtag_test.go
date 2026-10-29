package cache

import (
	"testing"

	"datahunter/internal/exchange"
)

func TestPartialBar15m(t *testing.T) {
	const start = int64(1_700_000_100_000) - int64(1_700_000_100_000)%(15*60_000)
	ms := []exchange.Candle{
		{Ts: start - 60_000, Open: 1, High: 9, Low: 1, Close: 1, Volume: 100}, // 上一根,不算
		{Ts: start, Open: 10, High: 12, Low: 9, Close: 11, Volume: 5},
		{Ts: start + 60_000, Open: 11, High: 15, Low: 10, Close: 14, Volume: 7},
	}
	p, ok := PartialBar15m(ms, start)
	if !ok || p.Open != 10 || p.High != 15 || p.Low != 9 || p.Close != 14 || p.Volume != 12 {
		t.Fatalf("partial bar wrong: %+v", p)
	}
	if _, ok := PartialBar15m(ms[:1], start); ok {
		t.Fatal("no minute inside the bar should fail")
	}
}

func TestHistTagSyncMem(t *testing.T) {
	b := &microBook{name: "pulsarv12"}
	tr := &PaperTrade{ID: "x", Coin: "AAA"}
	b.trades = []*PaperTrade{tr}
	s := &Store{pulsarV12Book: b}
	s.histTagSyncMem(&histTagRow{id: "x", book: "pulsarv12", cvd1h: 12, chg24: 1, longAcct: 0.5, ema200Dist: -3, btcEma200: 2})
	if tr.CVD1h != 12 || tr.LongAcct != 0.5 || tr.Ema200Dist != -3 || tr.BtcEma200 != 2 {
		t.Fatalf("memory not synced: %+v", tr)
	}
}
