package collector

import (
	"bytes"
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
	rep.q4(eps, cfg)
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
	rep.q4(eps, cfg)
	rep.q3(eps, bf)

	out := buf.String()
	for _, want := range []string{"最常爆的標的", "MFE 5m", "觸及 +20%", "事件中位數", "到頂中位數"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}
