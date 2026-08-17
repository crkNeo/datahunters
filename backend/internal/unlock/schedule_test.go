package unlock

import (
	"testing"
	"time"
)

// mkDataset builds an emissions dataset from per-label cumulative series.
func mkDataset(cats map[string][]string, series map[string][][2]float64) *dataset {
	d := &dataset{Categories: cats}
	for label, pts := range series {
		var c struct {
			Label string `json:"label"`
			Data  []struct {
				T int64   `json:"timestamp"`
				U float64 `json:"unlocked"`
			} `json:"data"`
		}
		c.Label = label
		for _, p := range pts {
			c.Data = append(c.Data, struct {
				T int64   `json:"timestamp"`
				U float64 `json:"unlocked"`
			}{T: int64(p[0]), U: p[1]})
		}
		d.Doc.Data = append(d.Doc.Data, c)
	}
	return d
}

func TestExtractEventsCumulativeToDeltas(t *testing.T) {
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	base := now.Unix()
	d := mkDataset(
		map[string][]string{"investors": {"seed"}},
		map[string][][2]float64{
			// cumulative unlocked: 100 → 100 → 150 → 400
			"seed": {
				{float64(base - 86400), 100},
				{float64(base + 86400), 100}, // no step, no event
				{float64(base + 2*86400), 150},
				{float64(base + 3*86400), 400},
			},
		},
	)
	ev := extractEvents(d, now, 30*24*time.Hour)
	if len(ev) != 2 {
		t.Fatalf("events = %d (%+v), want 2", len(ev), ev)
	}
	if ev[0].Ts != base+2*86400 || ev[0].Amount != 50 {
		t.Errorf("first event = %+v, want ts=+2d amount=50", ev[0])
	}
	if ev[1].Ts != base+3*86400 || ev[1].Amount != 250 {
		t.Errorf("second event = %+v, want ts=+3d amount=250", ev[1])
	}
}

// Staking is continuous emission, not a scheduled supply event. Including it
// would drown the real cliffs the feature exists to find.
func TestExtractEventsSkipsStaking(t *testing.T) {
	now := time.Now()
	base := now.Unix()
	d := mkDataset(
		map[string][]string{"staking": {"rewards"}, "investors": {"seed"}},
		map[string][][2]float64{
			"rewards": {{float64(base), 0}, {float64(base + 3600), 1_000_000}},
			"seed":    {{float64(base), 0}, {float64(base + 7200), 10}},
		},
	)
	ev := extractEvents(d, now, 30*24*time.Hour)
	if len(ev) != 1 {
		t.Fatalf("events = %d (%+v), want only the non-staking one", len(ev), ev)
	}
	if ev[0].Amount != 10 {
		t.Errorf("amount = %v, want 10", ev[0].Amount)
	}
}

func TestExtractEventsHonoursHorizonAndPast(t *testing.T) {
	now := time.Now()
	base := now.Unix()
	d := mkDataset(
		map[string][]string{"investors": {"seed"}},
		map[string][][2]float64{
			"seed": {
				{float64(base - 20*86400), 0},
				{float64(base - 10*86400), 10}, // past — excluded
				{float64(base + 5*86400), 20},  // inside horizon
				{float64(base + 90*86400), 40}, // beyond a 30d horizon
			},
		},
	)
	ev := extractEvents(d, now, 30*24*time.Hour)
	if len(ev) != 1 {
		t.Fatalf("events = %d (%+v), want 1", len(ev), ev)
	}
	if ev[0].Amount != 10 || ev[0].Ts != base+5*86400 {
		t.Errorf("event = %+v, want ts=+5d amount=10", ev[0])
	}
}

// Two labels in the same KNOWN group releasing at one timestamp are one cliff:
// groupZh folds them to a single bucket, so the amounts merge.
func TestExtractEventsMergesLabelsWithinKnownGroup(t *testing.T) {
	now := time.Now()
	base := now.Unix()
	d := mkDataset(
		map[string][]string{"insiders": {"team", "advisors"}},
		map[string][][2]float64{
			"team":     {{float64(base), 0}, {float64(base + 86400), 30}},
			"advisors": {{float64(base), 0}, {float64(base + 86400), 70}},
		},
	)
	ev := extractEvents(d, now, 30*24*time.Hour)
	if len(ev) != 1 {
		t.Fatalf("events = %d (%+v), want 1 merged event", len(ev), ev)
	}
	if ev[0].Amount != 100 {
		t.Errorf("amount = %v, want 100 (30+70 merged)", ev[0].Amount)
	}
	if ev[0].Category != "內部人" {
		t.Errorf("category = %q, want 內部人", ev[0].Category)
	}
}

// An UNKNOWN group falls back to the raw label, so its labels stay separate.
// That is the documented behaviour and it matters downstream: category is part
// of the unlock_events primary key, so a silent merge here would drop rows.
func TestExtractEventsKeepsRawLabelForUnknownGroup(t *testing.T) {
	now := time.Now()
	base := now.Unix()
	d := mkDataset(
		map[string][]string{"investors": {"seed", "strategic"}}, // not a groupZh key
		map[string][][2]float64{
			"seed":      {{float64(base), 0}, {float64(base + 86400), 30}},
			"strategic": {{float64(base), 0}, {float64(base + 86400), 70}},
		},
	)
	ev := extractEvents(d, now, 30*24*time.Hour)
	if len(ev) != 2 {
		t.Fatalf("events = %d (%+v), want 2 (raw labels stay distinct)", len(ev), ev)
	}
	if ev[0].Category != "seed" || ev[1].Category != "strategic" {
		t.Errorf("categories = %q/%q, want seed/strategic", ev[0].Category, ev[1].Category)
	}
}
