package collector

import (
	"testing"
	"time"
)

// baseSeries builds a quiet series that satisfies nothing, so each test only
// has to introduce the one condition it is about.
func baseSeries(n int) []pbar {
	base := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := make([]pbar, n)
	for i := range bs {
		bs[i] = pbar{
			ts:   base + int64(i)*60_000,
			open: 100, high: 100.2, low: 99.8, close_: 100,
			volQuote: 1000, takerPct: 50,
			oiUSD: 1e6, oiChg5m: 0,
			fundingBps: 1, basisBps: 5, volZ: 0, spotRatio: 0.5,
		}
	}
	return bs
}

// setupA lays down a textbook 型態 A: a run of red bars on collapsing open
// interest, then a green bar with buyers back in control.
func setupA(n int) []pbar {
	bs := baseSeries(n)
	i := n - 1
	px := 100.0
	for j := i - aLookback; j < i; j++ {
		bs[j].open = px
		px *= 0.994 // ~ -0.6% a bar, comfortably past the -3% run threshold
		bs[j].close_ = px
		bs[j].low = px * 0.998
		bs[j].high = bs[j].open
		bs[j].takerPct = 30
		bs[j].basisBps = -12
		bs[j].oiChg5m = -0.06
		bs[j].volZ = 5
	}
	bs[i] = pbar{ts: bs[i].ts, open: px, close_: px * 1.01, high: px * 1.012, low: px,
		volQuote: 5000, takerPct: 70, basisBps: 8, fundingBps: 1,
		oiUSD: 9e5, oiChg5m: -0.05, volZ: 6, spotRatio: 0.5}
	return bs
}

func TestDetectAHappyPath(t *testing.T) {
	bs := setupA(40)
	h, ok := detectA("XPINUSDT", bs, len(bs)-1)
	if !ok {
		t.Fatal("a textbook capitulation reversal was not detected")
	}
	if h.Pattern != "A" || h.Symbol != "XPINUSDT" {
		t.Errorf("hit = %+v, want pattern A on XPINUSDT", h)
	}
	if h.RunBars < aMinRedBars {
		t.Errorf("run_bars = %d, want ≥ %d", h.RunBars, aMinRedBars)
	}
	if h.RunPct >= aMinDrop {
		t.Errorf("run_pct = %v, want ≤ %v", h.RunPct, aMinDrop)
	}
}

// Each condition must be load-bearing on its own. If removing one still fires,
// the detector is looser than the spec says and the recorded hit rate would be
// measuring a different rule than the one written down.
func TestDetectARequiresEveryCondition(t *testing.T) {
	cases := map[string]func(bs []pbar){
		"OI 沒有被清算": func(bs []pbar) { bs[len(bs)-2].oiChg5m = -0.01 },
		"量能沒有放大":   func(bs []pbar) { bs[len(bs)-2].volZ = 1 },
		"賣壓不夠主導":   func(bs []pbar) { bs[len(bs)-2].takerPct = 55 },
		"基差沒有轉負":   func(bs []pbar) { bs[len(bs)-2].basisBps = 3 },
		"觸發根收黑":    func(bs []pbar) { i := len(bs) - 1; bs[i].close_ = bs[i].open * 0.99 },
		"觸發根買盤不足":  func(bs []pbar) { bs[len(bs)-1].takerPct = 50 },
		"觸發根基差仍為負": func(bs []pbar) { bs[len(bs)-1].basisBps = -4 },
		"跌幅不夠": func(bs []pbar) {
			i := len(bs) - 1
			for j := i - aLookback; j < i; j++ {
				bs[j].open, bs[j].close_ = 100, 99.99
			}
		},
		"紅K不夠多": func(bs []pbar) {
			i := len(bs) - 1
			for j := i - aLookback; j < i-1; j++ {
				bs[j].close_ = bs[j].open + 0.01 // turn them green
			}
		},
	}
	for name, mutate := range cases {
		bs := setupA(40)
		mutate(bs)
		if _, ok := detectA("X", bs, len(bs)-1); ok {
			t.Errorf("%s 仍然觸發 — 該條件沒有作用", name)
		}
	}
}

// setupB lays down 型態 B: OI and price rising together, buyers in control,
// basis positive and climbing, then a volume spike.
func setupB(n int) []pbar {
	bs := baseSeries(n)
	i := n - 1
	for k := bBuildBars + 1; k >= 1; k-- {
		b := &bs[i-k]
		b.close_ = 100 + float64(bBuildBars+1-k)*0.3
		b.open = b.close_ - 0.2
		b.takerPct = 65
		b.basisBps = 20 + float64(bBuildBars+1-k)*5
		b.oiChg5m = 0.005 * float64(bBuildBars+2-k) // 增加且遞增
	}
	bs[i] = pbar{ts: bs[i].ts, open: 101, close_: 102.5, high: 103, low: 101,
		volQuote: 50000, takerPct: 60, basisBps: 45, fundingBps: 2,
		oiUSD: 1.1e6, oiChg5m: 0.07, volZ: 25, spotRatio: 0.4}
	return bs
}

func TestDetectBHappyPath(t *testing.T) {
	bs := setupB(40)
	h, ok := detectB("CLOUSDT", bs, len(bs)-1)
	if !ok {
		t.Fatal("a textbook OI-expansion breakout was not detected")
	}
	if h.Pattern != "B" {
		t.Errorf("pattern = %q, want B", h.Pattern)
	}
}

func TestDetectBRequiresEveryCondition(t *testing.T) {
	cases := map[string]func(bs []pbar){
		"沒有爆量":      func(bs []pbar) { bs[len(bs)-1].volZ = 3 },
		"OI 沒有跳增":   func(bs []pbar) { bs[len(bs)-1].oiChg5m = 0.01 },
		"堆積期 OI 轉負": func(bs []pbar) { bs[len(bs)-2].oiChg5m = -0.01 },
		"堆積期買盤不足": func(bs []pbar) {
			for k := 1; k <= bBuildBars; k++ {
				bs[len(bs)-1-k].takerPct = 40
			}
		},
		"基差為負(型態C)":  func(bs []pbar) { bs[len(bs)-1].basisBps = -5 },
		"基差沒有上升":     func(bs []pbar) { bs[len(bs)-1].basisBps = 1 },
		"現貨比過低(型態C)": func(bs []pbar) { bs[len(bs)-1].spotRatio = 0.01 },
	}
	for name, mutate := range cases {
		bs := setupB(40)
		mutate(bs)
		if _, ok := detectB("X", bs, len(bs)-1); ok {
			t.Errorf("%s 仍然觸發 — 該條件沒有作用", name)
		}
	}
}

// The OI build-up must be ACCELERATING. A decelerating one is a move running
// out of fuel, not one being fed — and an off-by-one in the comparison window
// is exactly how that distinction gets lost.
func TestDetectBRejectsDeceleratingBuild(t *testing.T) {
	bs := setupB(40)
	i := len(bs) - 1
	// still positive throughout, but shrinking as it approaches the trigger
	bs[i-3].oiChg5m = 0.030
	bs[i-2].oiChg5m = 0.020
	bs[i-1].oiChg5m = 0.010
	if _, ok := detectB("X", bs, i); ok {
		t.Error("decelerating OI build still triggered — the growth check is not comparing adjacent bars inside the window")
	}
}

// A coin with no spot pair at all reports spotRatio 0, which must not be read
// as "spot volume is negligible" — four of the five biggest movers observed had
// no spot market, so treating 0 as a rejection would filter out the population
// of interest.
func TestCFilterDistinguishesNoSpotFromThinSpot(t *testing.T) {
	noSpot := pbar{basisBps: 10, fundingBps: 1, spotRatio: 0}
	thinSpot := pbar{basisBps: 10, fundingBps: 1, spotRatio: 0.01}
	if !passesCFilter(noSpot) {
		t.Error("a perp-only coin was rejected; spotRatio 0 means no spot pair, not thin spot")
	}
	if passesCFilter(thinSpot) {
		t.Error("a coin with real but negligible spot volume should be rejected")
	}
}

// Funding sign must NOT reject on its own.
//
// The first version treated negative funding as a 型態 C tell because GPS had
// it. WLD then ran negative funding for the entire build-up and still returned
// 9.5% — the two differ on BASIS at the trigger (GPS -10.9 bp, WLD +26.0 bp).
// Rejecting on funding would have discarded the better of the two trades on a
// coincidence, so the discriminator is basis alone.
func TestDetectBAcceptsNegativeFundingWithPositiveBasis(t *testing.T) {
	bs := setupB(40)
	for i := range bs {
		bs[i].fundingBps = -2.17 // WLD's actual funding through the whole move
	}
	if _, ok := detectB("WLDUSDT", bs, len(bs)-1); !ok {
		t.Error("negative funding alone rejected the setup — basis is the discriminator, not funding")
	}
	// but negative BASIS still rejects, which is the GPS case
	bs[len(bs)-1].basisBps = -10.9
	if _, ok := detectB("GPSUSDT", bs, len(bs)-1); ok {
		t.Error("negative basis at the trigger must still reject — that is the 型態 C failure")
	}
}

// The trigger threshold has to admit the moves that actually happened. Read off
// the single biggest case it sat at 5%, which threw away three of the four real
// setups observed while the one true failure was nowhere near it.
func TestDetectBTriggerThresholdAdmitsObservedMoves(t *testing.T) {
	observed := map[string]struct {
		oiChg float64
		want  bool
	}{
		"BTW +6.21%":      {0.0621, true},
		"WLD +3.51%":      {0.0351, true},
		"STAR +2.95%":     {0.0295, true},
		"PUMP +2.67%":     {0.0267, true},
		"GPS -0.37%(型態C)": {-0.0037, false},
	}
	for name, c := range observed {
		bs := setupB(40)
		bs[len(bs)-1].oiChg5m = c.oiChg
		_, ok := detectB("X", bs, len(bs)-1)
		if ok != c.want {
			t.Errorf("%s: 觸發=%v, want %v(門檻 %.2f%%)", name, ok, c.want, bTrigOIChg*100)
		}
	}
}

// The market's own move that minute is recorded, never filtered on. Two of the
// observed pumps fired in the same minute the whole market jumped 1.36%: those
// are beta rather than selection, and several at once are one bet, not several.
// The right threshold is unknown, so it has to be measured before it can reject.
func TestMarketReturnIsRecordedNotFiltered(t *testing.T) {
	bs := setupB(40)
	for i := range bs {
		bs[i].mktRet = 1.36 // the whole board moving
	}
	h, ok := detectB("X", bs, len(bs)-1)
	if !ok {
		t.Fatal("a market-wide minute must still be recorded, not silently dropped")
	}
	if h.MktRet != 1.36 {
		t.Errorf("mkt_ret = %v, want 1.36 — it has to reach the row to be measurable later", h.MktRet)
	}
}
