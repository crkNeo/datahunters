package cache

import (
	"math"
	"testing"

	"datahunter/internal/exchange"
)

func TestComputeEntryTags(t *testing.T) {
	cs := make([]exchange.Candle, 120)
	for i := range cs {
		cs[i] = exchange.Candle{Ts: int64(i) * 900000, Close: 100, Volume: 10, TakerBuy: 5, QuoteVol: 1000}
	}
	cs[len(cs)-97].Close = 80                                                         // 24h 前收 80 → 現在 100 = +25%
	cs[len(cs)-1].TakerBuy = 10                                                       // 最後一根全是主動買
	oi := []exchange.OIPoint{{SumOIValue: 100}, {SumOIValue: 110}, {SumOIValue: 121}} // 只有 3 點也要能算
	tg := ComputeEntryTags(cs, oi, 0.0005, 0.62, -1.5, 3.2)

	if math.Abs(tg.Chg24-25) > 1e-9 {
		t.Errorf("Chg24=%v want 25", tg.Chg24)
	}
	if math.Abs(tg.OI12h-21) > 1e-9 || math.Abs(tg.OI1h-10) > 1e-9 {
		t.Errorf("OI12h=%v OI1h=%v want 21 / 10", tg.OI12h, tg.OI1h)
	}
	if math.Abs(tg.Vol24-96000) > 1e-9 {
		t.Errorf("Vol24=%v want 96000", tg.Vol24)
	}
	if !(tg.CVD1h > tg.CVD6h && tg.CVD6h > 0) { // 最後一根全買 → 1h 窗權重較大
		t.Errorf("CVD1h=%v CVD6h=%v", tg.CVD1h, tg.CVD6h)
	}
	if tg.Funding != 0.0005 || tg.LongAcct != 0.62 || tg.BTC1h != -1.5 || tg.SurgeX != 3.2 {
		t.Errorf("直通欄位錯: %+v", tg)
	}

	var tr PaperTrade
	tg.apply(&tr)
	if tr.OI != tg.OI12h || tr.CVD != tg.CVD6h || tr.Funding != tg.Funding || tr.Chg24 != tg.Chg24 || tr.SurgeX != tg.SurgeX {
		t.Errorf("apply 沒寫對: %+v", tr)
	}
}

// 爆量倍數看的是「24h 滾動成交額」的變化(新進來的量 − 24h 前滾掉的量),所以用「量緩慢穩定增加」
// 當常態:每 30 分鐘的變化量固定 → 倍數 ≈ 1;最後 30 分鐘量 ×5 → 倍數明顯 > 3。資料不足回 0。
func TestSurgeXFromMinuteKlines(t *testing.T) {
	mk := func(n int, burst float64) []exchange.Candle {
		ks := make([]exchange.Candle, n)
		for i := range ks {
			qv := 1000.0 + 0.5*float64(i)
			if i >= n-30 {
				qv *= burst
			}
			ks[i] = exchange.Candle{Ts: int64(i) * 60000, Close: 1, QuoteVol: qv}
		}
		return ks
	}
	if x := SurgeXFromMinuteKlines(mk(2000, 1)); math.Abs(x-1) > 0.05 {
		t.Errorf("常態量 x=%v want ≈1", x)
	}
	if x := SurgeXFromMinuteKlines(mk(2000, 5)); x < 3 {
		t.Errorf("爆量 x=%v want > 3", x)
	}
	if x := SurgeXFromMinuteKlines(mk(1000, 5)); x != 0 {
		t.Errorf("不足 24h 資料應回 0, got %v", x)
	}
}
