package cache

import (
	"testing"

	"datahunter/internal/exchange"
)

// mkCandles 造一串收盤=closes[i] 的 15m K 棒,量能用 vols[i](長度需相同)。
// High/Low/Open 由 close 推出綠棒(open 稍低、上影短),方便構造「會過插針」的情況。
func mkCandles(closes, vols []float64) []exchange.Candle {
	out := make([]exchange.Candle, len(closes))
	for i, c := range closes {
		open := c * 0.995
		out[i] = exchange.Candle{
			Ts: int64(i) * 900000, Open: open, Close: c,
			High: c * 1.001, Low: open * 0.999, Volume: vols[i],
		}
	}
	return out
}

// surgeV3Why 與 surgeV3Signal 必須同步:why=="" 當且僅當 signal ok==true。
// 這個測試擋住「改了 surgeV3Signal 門檻卻忘了同步 surgeV3Why(log 會說謊)」。
func TestSurgeV3WhyMatchesSignal(t *testing.T) {
	// 情境 1:資料不足(<40 根)
	short := mkCandles(make([]float64, 30), make([]float64, 30))
	if _, _, _, _, ok := surgeV3Signal(short); ok {
		t.Fatal("30 根不該成立")
	}
	if why := surgeV3Why(short); why != "資料不足" {
		t.Errorf("why(30根)=%q, want 資料不足", why)
	}

	// 情境 2:下跌盤(動能不足)。48 根遞減收盤、量能平平。
	n := 48
	closes := make([]float64, n)
	vols := make([]float64, n)
	for i := 0; i < n; i++ {
		closes[i] = float64(200 - i) // 持續下跌 → EMA5<EMA20、收黑
		vols[i] = 100
	}
	down := mkCandles(closes, vols)
	_, _, _, _, ok := surgeV3Signal(down)
	why := surgeV3Why(down)
	if ok {
		t.Fatal("下跌盤不該成立")
	}
	if why == "" {
		t.Error("下跌盤 surgeV3Why 不該回空字串(應報子原因)")
	}

	// 核心不變量:why=="" ⇔ ok==true(在上面幾個樣本上)
	for _, cs := range [][]exchange.Candle{short, down} {
		_, _, _, _, ok := surgeV3Signal(cs)
		if (surgeV3Why(cs) == "") != ok {
			t.Errorf("surgeV3Why 與 surgeV3Signal 不一致:ok=%v why=%q", ok, surgeV3Why(cs))
		}
	}
}
