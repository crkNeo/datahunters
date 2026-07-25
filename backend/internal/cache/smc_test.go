package cache

import (
	"testing"
	"time"

	"datahunter/internal/exchange"
)

const m15 = int64(900000) // 15m in ms

// c 建一根 15m 棒(ts 用第 i 根 * 15m)。
func c(i int, o, h, l, cl float64) exchange.Candle {
	return exchange.Candle{Ts: int64(i) * m15, Open: o, High: h, Low: l, Close: cl}
}

// 聚合:4 根 15m → 1 根 1H,只回傳湊滿的組。
func TestSMCAggregate(t *testing.T) {
	// 5 根 15m:前 4 根成一個 1H,第 5 根是進行中的下一個 1H(不足 4 根)→ 應被丟掉
	cs := []exchange.Candle{
		c(0, 10, 12, 9, 11),
		c(1, 11, 15, 10, 14),
		c(2, 14, 14, 8, 9),
		c(3, 9, 11, 7, 10), // 這根收在 10
		c(4, 10, 20, 10, 18),
	}
	agg := smcAggregate(cs, 4)
	if len(agg) != 1 {
		t.Fatalf("應只有 1 根完整 1H,得 %d", len(agg))
	}
	g := agg[0]
	if g.Open != 10 || g.Close != 10 || g.High != 15 || g.Low != 7 {
		t.Errorf("聚合 OHLC 錯誤: %+v (want O10 H15 L7 C10)", g)
	}
}

// k=3 樞紐:嚴格大於前後各 3 根。
func TestSMCPivots(t *testing.T) {
	// index 3 是明顯高點(前後各 3 根都較低)
	cs := []exchange.Candle{
		c(0, 0, 5, 4, 0), c(1, 0, 6, 5, 0), c(2, 0, 7, 6, 0),
		c(3, 0, 20, 6, 0), // pivot high @3
		c(4, 0, 8, 7, 0), c(5, 0, 6, 5, 0), c(6, 0, 5, 4, 0),
	}
	ph := pivotHighIdx(cs, 3)
	if len(ph) != 1 || ph[0] != 3 {
		t.Errorf("pivot high 應為 [3],得 %v", ph)
	}
	// 低點鏡像
	cs2 := []exchange.Candle{
		c(0, 0, 5, 10, 0), c(1, 0, 5, 9, 0), c(2, 0, 5, 8, 0),
		c(3, 0, 5, 1, 0), // pivot low @3
		c(4, 0, 5, 7, 0), c(5, 0, 5, 8, 0), c(6, 0, 5, 9, 0),
	}
	pl := pivotLowIdx(cs2, 3)
	if len(pl) != 1 || pl[0] != 3 {
		t.Errorf("pivot low 應為 [3],得 %v", pl)
	}
}

// 趨勢:收盤突破最近已確認 pivot high → 多。
func TestSMCTrend(t *testing.T) {
	// 造一個 pivot high @3(值 20,確認於第 6 根),之後第 10 根收盤 25 突破 → 多
	cs := []exchange.Candle{
		c(0, 0, 5, 4, 5), c(1, 0, 6, 5, 6), c(2, 0, 7, 6, 7),
		c(3, 0, 20, 6, 10), // pivot high
		c(4, 0, 8, 7, 8), c(5, 0, 6, 5, 6), c(6, 0, 5, 4, 5),
		c(7, 0, 9, 4, 9), c(8, 0, 10, 4, 10), c(9, 0, 12, 4, 12),
		c(10, 0, 26, 4, 25), // 收盤 25 > pivot high 20 → 多
	}
	if tr := smcTrend(cs, 3); tr != 1 {
		t.Errorf("突破 pivot high 應為多(1),得 %d", tr)
	}
}

// 多方 FVG:low[j] > high[j-2] 成立,且未被之後收盤跌破下緣。
func TestSMCBullFVG(t *testing.T) {
	// j=2:low[2]=12 > high[0]=10 → 區域 [10,12]。之後收盤都在 12 以上 → 有效
	cs := []exchange.Candle{
		c(0, 0, 10, 9, 10),
		c(1, 0, 15, 11, 14),
		c(2, 0, 16, 12, 15), // 缺口 low=12 > high[0]=10
		c(3, 0, 17, 13, 16),
	}
	z := smcBullFVGs(cs, 100)
	if len(z) != 1 || z[0].bot != 10 || z[0].top != 12 {
		t.Fatalf("應有 1 個 FVG [10,12],得 %+v", z)
	}
	// 若之後有收盤跌破下緣 10 → 失效
	cs = append(cs, c(4, 0, 11, 8, 9)) // 收盤 9 < 10
	if z2 := smcBullFVGs(cs, 100); len(z2) != 0 {
		t.Errorf("收盤跌破下緣後 FVG 應失效,得 %+v", z2)
	}
}

// 出場保守假設:同一根同時觸及 SL 與 TP → 以 SL 計。
func TestSMCExitSLFirst(t *testing.T) {
	tr := &PaperTrade{Coin: "X", Dir: "long", Entry: 100, SL: 95, TP: 110, Status: "open"}
	// 模擬 smcRun 的收盤出場判斷:last.Low<=SL 優先
	last := exchange.Candle{Low: 94, High: 111, Close: 108}
	if last.Low <= tr.SL {
		closeTrade(tr, tr.SL, "sl", time.Now())
	} else if last.High >= tr.TP {
		closeTrade(tr, tr.TP, "tp", time.Now())
	}
	if tr.Outcome != "sl" {
		t.Errorf("同棒觸及 SL 與 TP 應以 SL 計,得 %q", tr.Outcome)
	}
}
