package cache

import (
	"math"
	"sort"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// pathBars 由關鍵點(第幾根, 價格)線性內插出收盤,加一點固定的小波動,做成 1H K。
func pathBars(pts [][2]float64, noise float64) []exchange.Candle {
	last := int(pts[len(pts)-1][0])
	closes := make([]float64, last+1)
	for k := 1; k < len(pts); k++ {
		a, b := pts[k-1], pts[k]
		for i := int(a[0]); i <= int(b[0]); i++ {
			t := (float64(i) - a[0]) / (b[0] - a[0])
			closes[i] = a[1] + t*(b[1]-a[1])
		}
	}
	cs := make([]exchange.Candle, len(closes))
	for i, c := range closes {
		c += noise * (math.Sin(float64(i)*1.7) + 0.5*math.Sin(float64(i)*0.37))
		o := c
		if i > 0 {
			o = cs[i-1].Close
		}
		cs[i] = exchange.Candle{Ts: int64(i) * dcBarMs, Open: o, Close: c, High: math.Max(o, c) + 0.3, Low: math.Min(o, c) - 0.3, Volume: 100}
	}
	return cs
}

// flipPath 把關鍵點上下翻轉(200 − 價格),看漲型態變成對應的看跌型態。
func flipPath(pts [][2]float64) [][2]float64 {
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{p[0], 200 - p[1]}
	}
	return out
}

// 每種看漲型態的理想走勢(最後一點 = 現在)。
var idealPaths = map[string][][2]float64{
	"sym_tri_bull": {{0, 100}, {40, 100}, {70, 130}, {82, 112}, {94, 127}, {106, 116}, {118, 124.5}, {130, 119}, {136, 122}},
	"dbl_bottom":   {{0, 130}, {30, 130}, {60, 100}, {75, 115}, {90, 100.5}, {100, 110}},
	"bull_flag":    {{0, 100}, {60, 100}, {72, 125}, {78, 119}, {84, 122.5}, {90, 116.5}, {96, 120}, {102, 114}, {104, 115.5}}, // 平行往下的通道
	"fall_wedge":   {{0, 140}, {30, 140}, {40, 126}, {50, 136}, {60, 123}, {70, 131}, {80, 120.5}, {90, 126.5}, {100, 118.5}, {104, 121}},
	"tri_bottom":   {{0, 130}, {40, 130}, {60, 100}, {70, 112}, {80, 100.5}, {90, 111.5}, {100, 99.8}, {108, 108}},
	"hs_bottom":    {{0, 130}, {40, 130}, {60, 105}, {70, 115}, {82, 96}, {94, 115.5}, {106, 105.5}, {114, 112}},
	"cup_bull":     {{0, 90}, {40, 90}, {60, 120}, {70, 108}, {80, 101}, {90, 99}, {100, 101}, {110, 108}, {120, 119.5}, {126, 115}, {130, 117}},
}

// 圓弧底用拋物線產生(線性內插做不出圓弧)。
func roundPath() [][2]float64 {
	pts := [][2]float64{{0, 118}}
	for i := 20; i <= 115; i += 5 {
		d := (float64(i) - 70) / 50
		pts = append(pts, [2]float64{float64(i), 100 + 25*d*d})
	}
	return pts
}

var bearOf = map[string]string{"sym_tri_bull": "sym_tri_bear", "dbl_bottom": "dbl_top", "bull_flag": "bear_flag",
	"fall_wedge": "rise_wedge", "tri_bottom": "tri_top", "hs_bottom": "hs_top", "cup_bull": "cup_bear", "round_bottom": "round_top"}

func scoresOf(hits map[string]*PatternHit) string {
	var ks []string
	for k := range hits {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	s := ""
	for _, k := range ks {
		s += k + "=" + itoa(hits[k].Score) + "(" + hits[k].State + ") "
	}
	return s
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestPatternsIdeal(t *testing.T) {
	paths := map[string][][2]float64{"round_bottom": roundPath()}
	for k, v := range idealPaths {
		paths[k] = v
	}
	for key, pts := range paths {
		for _, flip := range []bool{false, true} {
			want, p := key, pts
			if flip {
				want, p = bearOf[key], flipPath(pts)
			}
			hits := scanPatterns("X", pathBars(p, 0.6))
			h := hits[want]
			if h == nil || h.Score < 70 {
				t.Errorf("%s: want ≥70, got %v | all: %s", want, h, scoresOf(hits))
				continue
			}
			if (h.State != "forming" && h.State != "breakout") || h.Bars < ptMinBars || h.Breakout <= 0 || len(h.Chart.Candles) == 0 || len(h.Chart.Pivots) == 0 {
				t.Errorf("%s: bad hit %+v", want, h)
			}
			// 看漲:目標價在突破價之上;看跌:之下
			if (!flip && h.Target <= h.Breakout) || (flip && h.Target >= h.Breakout) {
				t.Errorf("%s: target %v vs breakout %v", want, h.Target, h.Breakout)
			}
			t.Logf("%-13s %3d  %s", want, h.Score, scoresOf(hits))
		}
	}
}

func TestPatternsBreakout(t *testing.T) {
	// 三角最後 3 根往上突破 → 剛突破;突破價在現價之下
	pts := append(append([][2]float64{}, idealPaths["sym_tri_bull"]...), [2]float64{140, 128})
	h := scanPatterns("X", pathBars(pts, 0.4))["sym_tri_bull"]
	if h == nil || h.State != "breakout" || h.DistPct <= 0 {
		t.Fatalf("expected a fresh breakout, got %+v", h)
	}
	// 跌破下緣 → 不算三角
	pts2 := append(append([][2]float64{}, idealPaths["sym_tri_bull"]...), [2]float64{142, 108})
	if h := scanPatterns("X", pathBars(pts2, 0.4))["sym_tri_bull"]; h != nil && h.Score >= 70 {
		t.Fatalf("broke below the triangle, should not be a high-score bull triangle: %+v", h.Parts)
	}
}

func TestPatternsRandomWalk(t *testing.T) {
	// 隨機走勢:大部分型態都不該高分
	r := uint32(12345)
	rnd := func() float64 { r = r*1664525 + 1013904223; return float64(r)/float64(1<<32) - 0.5 }
	high, total := 0, 0
	for s := 0; s < 60; s++ {
		cs := make([]exchange.Candle, 200)
		p := 100.0
		for i := range cs {
			o := p
			p *= 1 + rnd()*0.02
			cs[i] = exchange.Candle{Ts: int64(i) * dcBarMs, Open: o, Close: p, High: math.Max(o, p) * (1 + math.Abs(rnd())*0.006), Low: math.Min(o, p) * (1 - math.Abs(rnd())*0.006), Volume: 100}
		}
		for _, h := range scanPatterns("R", cs) {
			total++
			if h.Score >= 80 {
				high++
			}
		}
	}
	t.Logf("隨機走勢 60 條:列出 %d 個型態(≥50),其中 ≥80 的 %d 個", total, high)
	if high > 30 { // 60 條 × 16 種型態,≥80 超過 30 個(平均每條 0.5 個)就太寬鬆;目前約 21 個
		t.Fatalf("too many high scores on random walks: %d", high)
	}
}

func TestPatternsMirrorPrices(t *testing.T) {
	// 看跌型態的價格要換回原本座標(正的、在現價附近)
	cs := pathBars(flipPath(idealPaths["dbl_bottom"]), 0.6)
	h := scanPatterns("X", cs)["dbl_top"]
	if h == nil {
		t.Fatal("double top not found")
	}
	last := cs[len(cs)-1].Close
	if h.Breakout <= 0 || math.Abs(h.Breakout-last)/last > 0.2 {
		t.Fatalf("breakout %v should be a positive price near %v", h.Breakout, last)
	}
	for _, p := range h.Chart.Pivots {
		if p.P <= 0 {
			t.Fatalf("pivot price must be positive: %+v", p)
		}
	}
}

func TestPatternsAPI(t *testing.T) {
	s := &Store{ptStore: newPatternStore()}
	s.ptStore.frames["1h"].hits = map[string]map[string]*PatternHit{
		"AAA": {"dbl_bottom": {Coin: "AAA", Score: 72, State: "forming"}},
		"BBB": {"dbl_bottom": {Coin: "BBB", Score: 88, State: "breakout"}, "bull_flag": {Coin: "BBB", Score: 61}},
	}
	s.ptStore.frames["4h"].hits = map[string]map[string]*PatternHit{"CCC": {"hs_top": {Coin: "CCC", Score: 77}}}
	r := s.Patterns("dbl_bottom", "")
	if r.TF != "1h" || len(r.Patterns) != 16 || len(r.Rows) != 2 || r.Rows[0].Coin != "BBB" || r.Scanned != 2 {
		t.Fatalf("api: %+v", r)
	}
	if len(r.Summary["dbl_bottom"]) != 2 || len(r.Summary["bull_flag"]) != 1 || len(r.Summary["hs_top"]) != 0 {
		t.Fatalf("summary: %+v", r.Summary)
	}
	// 各週期分開
	r4 := s.Patterns("hs_top", "4h")
	if r4.TF != "4h" || r4.Scanned != 1 || len(r4.Rows) != 1 || len(r4.Summary["dbl_bottom"]) != 0 {
		t.Fatalf("4h: %+v", r4)
	}
	if r := s.Patterns("hs_top", "1d"); r.TF != "1d" || r.Scanned != 0 || len(r.Rows) != 0 {
		t.Fatalf("1d: %+v", r)
	}
	if r := s.Patterns("hs_top", "bogus"); r.TF != "1h" {
		t.Fatalf("unknown tf falls back to 1h: %v", r.TF)
	}
}

func TestPatternFrameDue(t *testing.T) {
	coins := func() []string {
		var out []string
		for i := 0; i < 120; i++ {
			out = append(out, string(rune('A'+i/26))+string(rune('A'+i%26)))
		}
		return out
	}
	f := newPatternStore().frames["4h"]
	bar := int64(1000) * f.barMs
	at := func(ms int64) time.Time { return time.UnixMilli(bar + ms) }
	if got := f.due(at(4*60_000), coins); len(got) != 0 {
		t.Fatalf("4h 收盤後 5 分鐘內不抓: %d", len(got))
	}
	got := f.due(at(5*60_000), coins)
	if len(got) != 50 || len(f.pend) != 120 {
		t.Fatalf("第一批 50 個(共 120 個排隊): %d / %d", len(got), len(f.pend))
	}
	for _, c := range got {
		delete(f.pend, c)
	}
	if got := f.due(at(6*60_000), coins); len(got) != 50 || got[0] == "AA" {
		t.Fatalf("下一分鐘接著下一批: %v", got[:1])
	}
	if got := f.due(at(61*60_000), coins); len(got) != 0 {
		t.Fatalf("過了 60 分鐘放棄這一輪: %d", len(got))
	}
	if got := f.due(time.UnixMilli(bar+f.barMs+5*60_000), coins); len(got) != 50 || len(f.pend) != 120 {
		t.Fatalf("下一根 4H 重新排入: %d", len(got))
	}
	// 1H:不延後、不分批
	h := newPatternStore().frames["1h"]
	if got := h.due(time.UnixMilli(int64(5000)*dcBarMs+30_000), coins); len(got) != 120 {
		t.Fatalf("1h 整點就全部排入: %d", len(got))
	}
}
