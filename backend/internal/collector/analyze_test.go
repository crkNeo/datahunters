package collector

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

// mkSeries builds n consecutive labelled minute bars with a quiet baseline.
//
// Volume carries a small deterministic wobble on purpose: a perfectly constant
// series has zero variance, and the z-score feature correctly declines to
// divide by it — which would make this helper test nothing.
func mkSeries(base int64, n int, price float64) []abar {
	bs := make([]abar, 0, n)
	for i := 0; i < n; i++ {
		vol := 1000 + float64(i%7)*10
		bs = append(bs, abar{
			ts:   base + int64(i)*60_000,
			open: price, high: price, low: price, close_: price,
			volQuote: vol, takerBuyQuote: vol / 2,
			oiUSD: 1_000_000, funding: 0.0001, mark: price, indexPx: price,
			labelled: true,
		})
	}
	return bs
}

// One spike produces an is_event flag on every bar that still sees it inside
// its own 5-minute window. Counting those as separate opportunities inflates
// the frequency several-fold and drags every per-event statistic toward the
// middle of moves instead of their start.
func TestSplitEpisodesMergesOneBurst(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 120, 100)
	for i := 80; i <= 84; i++ { // five consecutive bars flagged by one spike
		bs[i].mfe5 = 0.18
	}
	cfg := DefaultAnalyzeConfig()
	eps, _ := splitEpisodes(map[string][]abar{"AAAUSDT": bs}, cfg)

	if len(eps) != 1 {
		t.Fatalf("episodes = %d, want 1 merged burst", len(eps))
	}
	if eps[0].i != 80 {
		t.Errorf("anchor index = %d, want 80 (the FIRST bar of the burst)", eps[0].i)
	}
}

// Two bursts far enough apart are two opportunities, not one.
func TestSplitEpisodesSeparatesDistantBursts(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 300, 100)
	bs[80].mfe5 = 0.18
	bs[200].mfe5 = 0.22 // 120 minutes later — well past the 30m merge gap
	eps, _ := splitEpisodes(map[string][]abar{"AAAUSDT": bs}, DefaultAnalyzeConfig())
	if len(eps) != 2 {
		t.Fatalf("episodes = %d, want 2", len(eps))
	}
}

// A bar below the threshold is not an event, and an unlabelled bar (still
// inside its forward window) must never be counted either way.
func TestSplitEpisodesIgnoresSubThresholdAndUnlabelled(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 120, 100)
	bs[80].mfe5 = 0.04 // below 10%
	bs[90].mfe5 = 0.50 // huge, but…
	bs[90].labelled = false
	eps, _ := splitEpisodes(map[string][]abar{"AAAUSDT": bs}, DefaultAnalyzeConfig())
	if len(eps) != 0 {
		t.Fatalf("episodes = %d, want 0", len(eps))
	}
}

// The feature window is index-based for speed, so it must verify that the
// indices really span the intended minutes. A collection outage would
// otherwise silently stretch a 60-minute lookback across many hours.
func TestFeaturesAtRejectsTimeGap(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 120, 100)
	if _, ok := featuresAt(bs, 80); !ok {
		t.Fatal("contiguous series should produce features")
	}
	// punch a two-hour hole inside the lookback window
	for i := 30; i < len(bs); i++ {
		bs[i].ts += 2 * 60 * 60_000
	}
	if _, ok := featuresAt(bs, 80); ok {
		t.Error("features computed across a collection gap — the window is not really 60 minutes")
	}
}

func TestFeaturesAtValues(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 120, 100)
	i := 80
	bs[i].oiUSD = 1_200_000              // +20% vs the flat 1,000,000 baseline
	bs[i].close_ = 110                   // +10% vs 15m ago
	bs[i].volQuote = 9000                // a clear volume spike
	bs[i].takerBuyQuote = 7200           // 80% taker buy
	bs[i].mark, bs[i].indexPx = 101, 100 // +100 bps basis

	f, ok := featuresAt(bs, i)
	if !ok {
		t.Fatal("features not computed")
	}
	near := func(name string, got, want, tol float64) {
		t.Helper()
		if got < want-tol || got > want+tol {
			t.Errorf("%s = %v, want ~%v", name, got, want)
		}
	}
	near("OI 15m 變化 %", f["OI 15m 變化 %"], 20, 0.001)
	near("價格 15m 報酬 %", f["價格 15m 報酬 %"], 10, 0.001)
	near("主動買佔比", f["主動買佔比"], 0.8, 0.001)
	near("基差 basis(bps)", f["基差 basis(bps)"], 100, 0.001)
	if f["成交量 z-score(60m)"] <= 3 {
		t.Errorf("volume z = %v, want a large positive spike", f["成交量 z-score(60m)"])
	}
}

// Baseline must exclude the run-up and the aftermath: folding the move itself
// into "normal conditions" blunts exactly the contrast being measured.
func TestBaselineExcludesEventNeighbourhood(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 400, 100)
	bs[200].mfe5 = 0.30
	_, baseFeat := splitEpisodes(map[string][]abar{"AAAUSDT": bs}, DefaultAnalyzeConfig())

	// bars 140..260 are excluded; of the 400 bars only 60.. are eligible at all
	got := len(baseFeat["主動買佔比"])
	if got == 0 {
		t.Fatal("no baseline samples at all")
	}
	eligible := 400 - winLong // bars with enough history
	excluded := 2*winLong + 1 // the event neighbourhood
	if want := eligible - excluded; got != want {
		t.Errorf("baseline samples = %d, want %d (eligible %d minus excluded %d)", got, want, eligible, excluded)
	}
}

func TestPctQuantile(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := pct(v, 0.5); got != 5 {
		t.Errorf("p50 = %v, want 5", got)
	}
	if got := pct(v, 0); got != 1 {
		t.Errorf("p0 = %v, want 1", got)
	}
	if got := pct(v, 1.0); got != 10 {
		t.Errorf("p100 = %v, want 10", got)
	}
	if got := pct(nil, 0.5); got != 0 {
		t.Errorf("p50(empty) = %v, want 0", got)
	}
}

// The report must render without panicking on an empty dataset — that is the
// state it will first be run in.
func TestReportRendersWithNoEvents(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	series := map[string][]abar{"AAAUSDT": mkSeries(base, 120, 100)}
	var buf bytes.Buffer
	rep := newReport(&buf)
	cfg := DefaultAnalyzeConfig()
	rep.health(series)
	eps, bf := splitEpisodes(series, cfg)
	rep.q1(eps, series, cfg)
	rep.q4(eps, cfg, autoVisible(cfg.EventPct))
	rep.q3(eps, bf)
	rep.footer(eps, cfg)

	out := buf.String()
	for _, want := range []string{"資料健康", "沒有任何事件", "-event-pct"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

func TestReportRendersWithEvents(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 400, 100)
	for _, i := range []int{100, 200, 300} {
		bs[i].mfe5, bs[i].mae5, bs[i].mfe15, bs[i].secsPeak = 0.22, -0.03, 0.25, 90
	}
	series := map[string][]abar{"AAAUSDT": bs}
	var buf bytes.Buffer
	rep := newReport(&buf)
	cfg := DefaultAnalyzeConfig()
	eps, bf := splitEpisodes(series, cfg)
	if len(eps) != 3 {
		t.Fatalf("episodes = %d, want 3", len(eps))
	}
	rep.q1(eps, series, cfg)
	rep.q4(eps, cfg, autoVisible(cfg.EventPct))
	rep.q3(eps, bf)

	out := buf.String()
	for _, want := range []string{"最常爆的標的", "MFE 5m", "觸及 +20%", "事件中位數", "可交易性"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

// The anchor sits up to 5 minutes before the peak by construction, so anything
// measured from it overstates the opportunity. addTradableView re-measures from
// the first bar a live system could have reacted to; this pins that arithmetic.
func TestAddTradableView(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 200, 100)
	set := func(i int, high, low, close_ float64) {
		bs[i].high, bs[i].low, bs[i].close_ = high, low, close_
	}
	set(101, 100.6, 100.0, 100.5) // +0.5% — below the visibility threshold
	set(102, 103.2, 100.4, 103.0) // +3.0% — the move becomes visible here
	set(103, 110.0, 102.0, 109.0) // peak
	set(104, 109.0, 106.0, 107.0)
	set(105, 108.0, 105.0, 106.0)
	set(106, 107.0, 104.0, 105.0)
	set(107, 106.0, 103.0, 104.0)

	var e episode
	addTradableView(&e, bs, 100, 0.02)
	if !e.hasVis {
		t.Fatal("move should have been visible")
	}
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-6 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	near("visSecs", e.visSecs, 120)         // two bars after the anchor
	near("visMFE", e.visMFE, 110.0/103.0-1) // from the ENTRY price, not the anchor
	near("visMAE", e.visMAE, 102.0/103.0-1)
	near("visToPeakSec", e.visToPeakSec, 60) // one bar from visible to peak

	// The anchor-based view of the same move looks far better — that gap is the
	// cost of detection, and the report must never present only the first one.
	if anchorMFE := 110.0/100.0 - 1; anchorMFE <= e.visMFE {
		t.Errorf("anchor MFE %v should exceed the tradable MFE %v", anchorMFE, e.visMFE)
	}
}

// A spike that only ever shows up as a wick — never in a close — is not
// something a close-based screener can react to, and must not be counted as
// tradable rather than silently inheriting the anchor's numbers.
func TestAddTradableViewWickOnly(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 200, 100)
	bs[103].high = 130 // a huge wick, but every close stays at 100
	var e episode
	addTradableView(&e, bs, 100, 0.02)
	if e.hasVis {
		t.Errorf("wick-only move must not be reported as tradable, got %+v", e)
	}
}

func TestSimulate(t *testing.T) {
	const cost = 0.003
	e := func(path ...pathBar) episode { return episode{entry: 100, path: path} }

	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	// take-profit reached
	near("TP", e(pathBar{106, 99, 105}).simulate(0.05, 0.02, cost), 0.05-cost)
	// stop-loss reached
	near("SL", e(pathBar{101, 97, 98}).simulate(0.05, 0.02, cost), -0.02-cost)

	// A bar spanning BOTH levels must resolve as the stop. Minute bars cannot
	// say which came first, and picking the favourable reading is how a
	// backtest quietly converts losses into wins.
	near("both-in-one-bar", e(pathBar{106, 97, 105}).simulate(0.05, 0.02, cost), -0.02-cost)

	// neither level touched → time stop at the last close
	near("time stop", e(pathBar{103, 99, 102}, pathBar{104, 101, 103}).simulate(0.10, 0.05, cost), 0.03-cost)

	// earlier bar wins over a later one
	near("first bar decides", e(pathBar{101, 97, 98}, pathBar{120, 119, 120}).simulate(0.05, 0.02, cost), -0.02-cost)

	near("empty path", e().simulate(0.05, 0.02, cost), 0)
}

// The simulation must be reachable from a real episode built by addTradableView,
// not only from hand-made structs — otherwise a change to the capture window
// could leave the path empty and every simulated trade would silently read as a
// flat time-stop.
func TestAddTradableViewCapturesPath(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 200, 100)
	bs[102].high, bs[102].low, bs[102].close_ = 103.2, 100.4, 103.0
	bs[103].high, bs[103].low, bs[103].close_ = 110.0, 102.0, 109.0

	var e episode
	addTradableView(&e, bs, 100, 0.02)
	if !e.hasVis {
		t.Fatal("expected a visible move")
	}
	if len(e.path) == 0 {
		t.Fatal("path is empty — the simulation would see every trade as a flat time stop")
	}
	if e.entry != 103.0 {
		t.Errorf("entry = %v, want the visible bar's close 103.0", e.entry)
	}
	// +5% from 103 is 108.15, reached by bar 103's high of 110
	if got := e.simulate(0.05, 0.02, 0); got != 0.05 {
		t.Errorf("simulate = %v, want the +5%% take-profit", got)
	}
}

// Detection eats the first slice of every move, so the threshold that decides
// "visible" has to scale with the move being chased. Pinned at a constant 2% it
// would consume two thirds of a 3% target and make small moves look
// unprofitable for reasons of measurement rather than market.
func TestAutoVisibleScalesWithTarget(t *testing.T) {
	cases := map[float64]float64{
		0.03:  0.01, // a third of the move
		0.05:  0.0166666667,
		0.10:  0.02,  // capped
		0.30:  0.02,  // still capped
		0.006: 0.005, // floored
	}
	for target, want := range cases {
		if got := autoVisible(target); math.Abs(got-want) > 1e-6 {
			t.Errorf("autoVisible(%.3f) = %.6f, want %.6f", target, got, want)
		}
	}
	// monotonic up to the cap, never above it, never below the floor
	prev := 0.0
	for _, tgt := range []float64{0.005, 0.01, 0.02, 0.03, 0.06, 0.10, 0.50} {
		v := autoVisible(tgt)
		if v > 0.02+1e-9 || v < 0.005-1e-9 {
			t.Errorf("autoVisible(%.3f) = %v, outside [0.005, 0.02]", tgt, v)
		}
		if v < prev-1e-9 {
			t.Errorf("autoVisible not monotonic at %.3f: %v < %v", tgt, v, prev)
		}
		prev = v
	}
}

// A lower event threshold must find at least as many episodes as a higher one:
// every +10% move is also a +3% move.
func TestSweepThresholdsAreNested(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bs := mkSeries(base, 400, 100)
	bs[100].mfe5 = 0.12 // clears every threshold
	bs[200].mfe5 = 0.04 // clears 3% only
	series := map[string][]abar{"AAAUSDT": bs}

	cfg := DefaultAnalyzeConfig()
	cfg.EventPct = 0.10
	hi, _ := splitEpisodes(series, cfg)
	cfg.EventPct = 0.03
	lo, _ := splitEpisodes(series, cfg)

	if len(hi) != 1 {
		t.Errorf("at +10%% got %d episodes, want 1", len(hi))
	}
	if len(lo) != 2 {
		t.Errorf("at +3%% got %d episodes, want 2", len(lo))
	}
	if len(lo) < len(hi) {
		t.Error("lowering the threshold must never reduce the episode count")
	}
}
