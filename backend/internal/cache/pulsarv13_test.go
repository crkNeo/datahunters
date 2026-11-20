package cache

import (
	"math"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// v13Bars:前面一段 100 附近震盪、中段一根 110 的高點,之後跌到 95 附近,最後兩根爆量長紅反彈。
func v13Bars(peakHigh, lastVolMult float64) []exchange.Candle {
	const n = 200
	t0 := int64(1_800_000_000_000) / 900_000 * 900_000
	cs := make([]exchange.Candle, n)
	px := 100.0
	for i := range cs {
		switch {
		case i < 150:
			px = 100 + 0.3*math.Sin(float64(i))
		case i < 190:
			px = 100 - 5*float64(i-150)/40 + 0.2*math.Sin(float64(i))
		default:
			px = 95 + 0.1*float64(i-190)
		}
		cs[i] = exchange.Candle{Ts: t0 + int64(i)*900_000, Open: px - 0.05, High: px + 0.4, Low: px - 0.4, Close: px + 0.05, Volume: 100}
		if i%2 == 0 { // 紅綠交錯,ATR 穩定
			cs[i].Open, cs[i].Close = px+0.05, px-0.05
		}
	}
	cs[160].High = peakHigh // 24h 內的高點
	// 倒數第二根:爆量
	cs[n-2] = exchange.Candle{Ts: cs[n-2].Ts, Open: 96, High: 96.9, Low: 95.9, Close: 96.8, Volume: 100 * lastVolMult}
	// 訊號根:長紅、收在高點附近、量 ≥ 1.2 倍
	cs[n-1] = exchange.Candle{Ts: cs[n-1].Ts, Open: 96.8, High: 97.75, Low: 96.7, Close: 97.7, Volume: 300}
	return cs
}

func TestSurgeV13Signal(t *testing.T) {
	cs := v13Bars(110, 6)
	if _, _, _, _, ok := surgeV3Signal(cs); !ok {
		t.Fatalf("test setup: v3 should fire, why=%q", surgeV3Why(cs))
	}
	d24, spx, ema, ok := v13Feats(cs)
	if !ok || d24 > -4 || spx < 3.5 || ema < 0.5 {
		t.Fatalf("test setup feats: d24 %.2f spx %.2f ema %.2f", d24, spx, ema)
	}
	dir, entry, sl, tp, ok := surgeV13Signal(cs)
	if !ok || dir != "long" || math.Abs(tp-roundPx(entry+0.75*(entry-sl))) > 1e-9 {
		t.Fatalf("v13 should fire with TP = entry + 0.75R: %v %v %v %v %v", dir, entry, sl, tp, ok)
	}
	// ① 離 24h 高點不到 4% → 不進
	if _, _, _, _, ok := surgeV13Signal(v13Bars(99, 6)); ok {
		t.Fatalf("near the 24h high must not fire")
	}
	if w := surgeV13Why(v13Bars(99, 6)); w != "離24h高點不夠遠" {
		t.Fatalf("why: %q", w)
	}
	// ② 爆量不夠(v3 的 2.5 倍有過,但 < 3.5 倍)→ 不進
	weak := v13Bars(110, 2.8)
	if _, _, _, _, ok := surgeV3Signal(weak); !ok {
		t.Fatalf("test setup: v3 should still fire with 2.8x: %q", surgeV3Why(weak))
	}
	if _, _, _, _, ok := surgeV13Signal(weak); ok {
		t.Fatalf("weak spike must not fire")
	}
}

func TestPulsarV13GateBTC(t *testing.T) {
	cs := v13Bars(110, 6)
	ts := cs[len(cs)-1].Ts
	for _, tc := range []struct {
		btcFrom, btcTo float64
		want           bool
	}{{100, 99, true}, {100, 101, false}} {
		s := &Store{candleCache: newTTLCache(time.Hour)}
		btc := make([]exchange.Candle, 40)
		for i := range btc {
			btc[i] = exchange.Candle{Ts: ts - int64(39-i)*900_000, Close: tc.btcFrom}
		}
		btc[39].Close = tc.btcTo // 訊號那根(16 根前 = btcFrom)
		s.candleCache.get(fmtBTCKey(ts), func() (any, error) { return btc, nil })
		if got := s.pulsarV13Gate("AAA", cs); got != tc.want {
			t.Fatalf("BTC %v→%v: gate %v want %v", tc.btcFrom, tc.btcTo, got, tc.want)
		}
		if r, ok := s.btc4hAt(ts); !ok || math.Abs(r-(tc.btcTo/tc.btcFrom-1)*100) > 1e-9 {
			t.Fatalf("btc4hAt %v %v", r, ok)
		}
	}
	// 抓不到 BTC → 不進
	s := &Store{candleCache: newTTLCache(time.Hour)}
	if s.pulsarV13Gate("AAA", cs) {
		t.Fatalf("missing BTC must veto")
	}
}
