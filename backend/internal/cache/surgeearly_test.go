package cache

import (
	"testing"

	"datahunter/internal/exchange"
)

// bar 造一根 15m K 棒(序號 i 決定時戳)。
func bar(i int, o, h, l, c, v float64) exchange.Candle {
	return exchange.Candle{Ts: int64(i) * 900000, Open: o, High: h, Low: l, Close: c, Volume: v}
}

// quietBars 造 n 根盤整 K(在 px 附近 ±0.2 小幅震盪、量 100)。
func quietBars(n int, px float64) []exchange.Candle {
	out := make([]exchange.Candle, 0, n)
	for i := 0; i < n; i++ {
		c := px + 0.1
		if i%2 == 1 {
			c = px - 0.1
		}
		o := px - (c - px)
		out = append(out, bar(i, o, px+0.2, px-0.2, c, 100))
	}
	return out
}

// pullbackSetup 造「盤整 → 緩漲 → 爆量根 → 量縮回踩」,最後一根(由 last 決定)落在 EMA20 附近。
func pullbackSetup(pbVol float64, last func(ema float64, i int) exchange.Candle) []exchange.Candle {
	cs := quietBars(40, 100)
	px := 100.0
	for j := 0; j < 6; j++ { // 緩漲,讓 EMA20 上彎
		i := len(cs)
		cs = append(cs, bar(i, px, px+0.7, px-0.1, px+0.6, 150))
		px += 0.6
	}
	i := len(cs) // 爆量根:收陽、量 6×
	cs = append(cs, bar(i, px, px+3.2, px-0.1, px+3.0, 600))
	px += 3.0
	for j := 0; j < 3; j++ { // 量縮回踩
		i := len(cs)
		cs = append(cs, bar(i, px, px+0.1, px-1.0, px-0.9, pbVol))
		px -= 0.9
	}
	ema := emaSeries(cs, 20)[len(cs)-1]
	return append(cs, last(ema, len(cs)))
}

// 碰到 EMA20 守住、收陽的錘子 → 應進場;止損在回踩低點下方、R 遠小於爆量前的 swing low。
func hammerAt(ema float64, i int) exchange.Candle {
	return bar(i, ema+0.3, ema+0.7, ema-0.1, ema+0.6, 120)
}

func TestSurgeV10FirstPullback(t *testing.T) {
	cs := pullbackSetup(150, hammerAt)
	dir, e, sl, tp, ok := surgeV10Signal(cs)
	if !ok || dir != "long" {
		t.Fatalf("回踩 EMA20 守住應進場, got ok=%v dir=%q", ok, dir)
	}
	if !(sl < e && tp > e) {
		t.Fatalf("價位錯: entry=%v sl=%v tp=%v", e, sl, tp)
	}
	if r := (e - sl) / e * 100; r > 2 {
		t.Errorf("回踩止損應該很近, R=%.2f%%", r)
	}
}

func TestSurgeV10Rejects(t *testing.T) {
	// 回踩沒量縮(回踩量 ≥ 爆量根 0.6×)→ 不進
	if _, _, _, _, ok := surgeV10Signal(pullbackSetup(500, hammerAt)); ok {
		t.Error("回踩量太大不該進")
	}
	// 收盤跌破 EMA20 → 不進
	breakdown := func(ema float64, i int) exchange.Candle { return bar(i, ema+0.2, ema+0.3, ema-0.8, ema-0.5, 120) }
	if _, _, _, _, ok := surgeV10Signal(pullbackSetup(150, breakdown)); ok {
		t.Error("收在 EMA20 下方不該進")
	}
	// 離 EMA20 很遠(沒回踩到)→ 不進
	far := func(ema float64, i int) exchange.Candle { return bar(i, ema+2.0, ema+2.6, ema+1.8, ema+2.5, 120) }
	if _, _, _, _, ok := surgeV10Signal(pullbackSetup(150, far)); ok {
		t.Error("沒碰到 EMA20 不該進")
	}
	// 沒有爆量根(純盤整)→ 不進
	if _, _, _, _, ok := surgeV10Signal(quietBars(60, 100)); ok {
		t.Error("沒爆量不該進")
	}
}

// breakoutSetup 造「盤整 → 3 根放量突破」:還在不追高範圍內(距20根低 ≤ 8×ATR),但止損
// (近10根低)相對盤整 ATR 很寬 → v3 被「止損過寬」擋下;v11 用突破當下 ATR 應放行。
func breakoutSetup(vol float64) []exchange.Candle {
	cs := quietBars(40, 100)
	px := 100.0
	for j := 0; j < 3; j++ {
		i := len(cs)
		cs = append(cs, bar(i, px, px+1.15, px-0.05, px+1.0, vol))
		px += 1.0
	}
	return cs
}

func TestSurgeV11EarlierThanV3(t *testing.T) {
	cs := breakoutSetup(600)
	if _, _, _, _, ok := surgeV3Signal(cs); ok {
		t.Fatal("此情境 v3 應被擋(止損過寬),測試前提不成立")
	}
	if why := surgeV3Why(cs); why != "止損過寬(>4×ATR)" {
		t.Fatalf("v3 應因止損過寬被擋, got %q", why)
	}
	d, e, sl, _, ok := surgeV11Signal(cs)
	if !ok || d != "long" || sl >= e {
		t.Fatalf("v11 應在突破段進場, got ok=%v d=%q e=%v sl=%v", ok, d, e, sl)
	}
	// 寬止損(R≥3%)但爆量不到 4× → 依 v9 規則擋掉
	if _, _, _, _, ok := surgeV11Signal(breakoutSetup(300)); ok {
		t.Error("寬止損沒有真爆量,v11 不該進")
	}
}

// v3 的行為不能因為抽出 surgeV3Core 而改變。
func TestSurgeV3CoreDefaultUnchanged(t *testing.T) {
	for _, cs := range [][]exchange.Candle{breakoutSetup(600), pullbackSetup(150, hammerAt), quietBars(60, 100)} {
		_, e1, s1, t1, ok1 := surgeV3Signal(cs)
		_, e2, s2, t2, ok2 := surgeV3Core(cs, false)
		if ok1 != ok2 || e1 != e2 || s1 != s2 || t1 != t2 {
			t.Error("surgeV3Signal 與 surgeV3Core(false) 不一致")
		}
		if (surgeV3Why(cs) == "") != ok1 {
			t.Errorf("surgeV3Why 與 surgeV3Signal 不一致: ok=%v why=%q", ok1, surgeV3Why(cs))
		}
	}
}
