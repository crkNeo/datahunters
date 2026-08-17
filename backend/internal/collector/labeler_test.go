package collector

import (
	"math"
	"testing"
	"time"
)

// mkBars builds a minute series from (offsetMinutes, high, low, close) tuples.
func mkBars(base int64, spec [][4]float64) []bar {
	out := make([]bar, 0, len(spec))
	for _, s := range spec {
		out = append(out, bar{
			ts:     base + int64(s[0])*60_000,
			high:   s[1],
			low:    s[2],
			close_: s[3],
		})
	}
	return out
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestComputeLabelMFEandMAE(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	// t+0 is the base bar (close 100). Price spikes to 118 at t+2, dips to 94
	// at t+4, and settles at 104 by t+5.
	bars := mkBars(base, [][4]float64{
		{0, 100, 100, 100},
		{1, 105, 99, 103},
		{2, 118, 102, 110},
		{3, 112, 101, 106},
		{4, 108, 94, 96},
		{5, 106, 95, 104},
	})
	got := computeLabel("TESTUSDT", bars, 0, 0.10)

	approx(t, "mfe_5m", got.MFE[5], 0.18)  // 118/100 - 1
	approx(t, "mae_5m", got.MAE[5], -0.06) // 94/100 - 1
	approx(t, "ret_5m", got.Ret[5], 0.04)  // last close in window 104/100 - 1
	if got.Bars5 != 5 {
		t.Errorf("bars_5m = %d, want 5", got.Bars5)
	}
	if !got.IsEvent {
		t.Errorf("is_event = false, want true (mfe_5m 18%% ≥ 10%% threshold)")
	}
	// The peak high inside the 15m window sits at t+2.
	if got.SecsPeak != 120 {
		t.Errorf("secs_to_peak_15m = %d, want 120", got.SecsPeak)
	}
}

// A gap in collection must not stretch a window. This is the failure mode that
// would quietly corrupt exactly the rows next to interesting conditions — a
// rate-limit pause or a restart during a volatile stretch — so the window is
// selected by timestamp, never by index offset.
func TestComputeLabelIgnoresBarsOutsideWindow(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := mkBars(base, [][4]float64{
		{0, 100, 100, 100},
		{1, 101, 99, 100},
		// collection gap — the next sample is 40 minutes later and doubles
		{40, 200, 190, 195},
	})
	got := computeLabel("TESTUSDT", bars, 0, 0.10)

	approx(t, "mfe_5m", got.MFE[5], 0.01) // only the t+1 bar is inside 5m
	if got.Bars5 != 1 {
		t.Errorf("bars_5m = %d, want 1", got.Bars5)
	}
	if got.IsEvent {
		t.Errorf("is_event = true, but the +95%% bar is 40m away — must not count as a 5m event")
	}
	// The far bar does belong to the 60m horizon.
	approx(t, "mfe_60m", got.MFE[60], 1.0)
	if got.Bars60 != 2 {
		t.Errorf("bars_60m = %d, want 2", got.Bars60)
	}
}

// A bar with no forward data yet must produce zeros rather than a fabricated
// excursion — the labeler's lag window is what normally prevents this, and this
// test pins the behaviour if that guard is ever loosened.
func TestComputeLabelNoForwardData(t *testing.T) {
	base := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	bars := mkBars(base, [][4]float64{{0, 100, 100, 100}})
	got := computeLabel("TESTUSDT", bars, 0, 0.10)
	if got.MFE[5] != 0 || got.MAE[5] != 0 || got.Bars5 != 0 || got.IsEvent {
		t.Errorf("expected empty label for a bar with no forward bars, got %+v", got)
	}
}

func TestBuildRegime(t *testing.T) {
	ts := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	snaps := []snapRow{
		{Symbol: "BTCUSDT", Open: 100, Close: 101, OIUSD: 1000},
		{Symbol: "ETHUSDT", Open: 100, Close: 99, OIUSD: 500},
		{Symbol: "AAAUSDT", Open: 100, Close: 102, OIUSD: 250},
		{Symbol: "BBBUSDT", Open: 100, Close: 98, OIUSD: 250},
		{Symbol: "CCCUSDT", Open: 0, Close: 50, OIUSD: 100}, // bad open, skipped from returns
	}
	r := buildRegime(ts, snaps)

	if r.AdvCount != 2 || r.DecCount != 2 {
		t.Errorf("adv/dec = %d/%d, want 2/2", r.AdvCount, r.DecCount)
	}
	approx(t, "btc_px", r.BTCPx, 101)
	approx(t, "eth_px", r.ETHPx, 99)
	approx(t, "total_oi_usd", r.TotalOIUSD, 2100)
	// returns are -0.02, -0.01, 0.01, 0.02 → median of the middle pair is 0
	approx(t, "median_ret", r.MedianRet, 0)
	if r.Universe != 5 {
		t.Errorf("universe = %d, want 5", r.Universe)
	}
	if r.Disp <= 0 {
		t.Errorf("disp = %v, want > 0 for a spread cross-section", r.Disp)
	}
}

// Dispersion is the "is money picking coins, or is the whole board moving"
// term. A cross-section where every coin returns the same must read zero, or
// the regime gate cannot tell beta days from selection days.
func TestBuildRegimeZeroDispersionWhenUniform(t *testing.T) {
	ts := time.Now().UnixMilli()
	snaps := []snapRow{
		{Symbol: "AAAUSDT", Open: 100, Close: 101},
		{Symbol: "BBBUSDT", Open: 200, Close: 202},
		{Symbol: "CCCUSDT", Open: 50, Close: 50.5},
	}
	r := buildRegime(ts, snaps)
	approx(t, "median_ret", r.MedianRet, 0.01)
	if r.Disp > 1e-12 {
		t.Errorf("disp = %v, want ~0 when every coin returns +1%%", r.Disp)
	}
}

func TestNextTickAt(t *testing.T) {
	delay := 10 * time.Second
	cases := []struct{ now, want string }{
		{"2026-08-17T12:00:00Z", "2026-08-17T12:00:10Z"}, // exactly on the boundary
		{"2026-08-17T12:00:05Z", "2026-08-17T12:00:10Z"},
		{"2026-08-17T12:00:10Z", "2026-08-17T12:01:10Z"}, // at the tick — go to the next
		{"2026-08-17T12:00:59Z", "2026-08-17T12:01:10Z"},
	}
	for _, c := range cases {
		now, _ := time.Parse(time.RFC3339, c.now)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := nextTickAt(now, delay); !got.Equal(want) {
			t.Errorf("nextTickAt(%s) = %s, want %s", c.now, got.Format(time.RFC3339), c.want)
		}
	}
}

func TestDayKey(t *testing.T) {
	if got := dayKey(time.Date(2026, 8, 17, 23, 59, 0, 0, time.UTC)); got != 20260817 {
		t.Errorf("dayKey = %d, want 20260817", got)
	}
	// Partition keys are UTC: a local-zone conversion here would put rows in the
	// wrong partition for part of every day.
	loc := time.FixedZone("UTC+8", 8*3600)
	if got := dayKey(time.Date(2026, 8, 18, 3, 0, 0, 0, loc)); got != 20260817 {
		t.Errorf("dayKey(UTC+8 03:00 on the 18th) = %d, want 20260817", got)
	}
	if got := dayKeyMs(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC).UnixMilli()); got != 20260105 {
		t.Errorf("dayKeyMs = %d, want 20260105", got)
	}
}
