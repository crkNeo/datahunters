package collector

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// The replay must render every column and mark the anchor, including the rows
// where depth was not sampled — a blank there is expected (depth runs on a
// slower cadence) and must not be printed as a zero, which would read as "the
// book was empty" rather than "not measured".
func TestEventTableRendersAndMarksAnchor(t *testing.T) {
	anchor := time.Date(2026, 8, 18, 5, 23, 0, 0, time.UTC).UnixMilli()
	e := episode{symbol: "ACEUSDT", ts: anchor, mfe5: 0.124, visMFE: 0.071}
	rows := []detailRow{
		{ts: anchor - 2*60_000, close_: 0.1, ret1m: -0.3, volQuote: 12000, volZ: -0.4,
			takerPct: 41, oiUSD: 5e6, dOI5m: -0.9, fundBps: 0.5, basisBps: -3.2, mktRet: -0.01},
		{ts: anchor, close_: 0.102, ret1m: 2.0, volQuote: 90000, volZ: 6.2,
			takerPct: 78, oiUSD: 5.4e6, dOI5m: 3.1, fundBps: 0.5, basisBps: 12.0,
			spotRatio: 0.42, askUSD1: 250000, bidUSD1: 180000, hasDepth: true, mktRet: 0.02},
	}
	var buf bytes.Buffer
	newReport(&buf).eventTable(e, rows, 30)
	out := buf.String()

	for _, want := range []string{"ACEUSDT", "08-18 05:23", "T0", "volZ", "賣深(K)"} {
		if !strings.Contains(out, want) {
			t.Errorf("replay missing %q\n---\n%s", want, out)
		}
	}
	// the row without depth must show a dash, not a fabricated 0
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var minus2 string
	for _, l := range lines {
		if strings.Contains(l, "-2 ") || strings.HasPrefix(strings.TrimSpace(l), "-2") {
			minus2 = l
		}
	}
	if minus2 == "" {
		t.Fatalf("could not find the T-2 row\n%s", out)
	}
	if !strings.Contains(minus2, "-") {
		t.Errorf("unsampled depth should render as '-', got: %s", minus2)
	}
}
