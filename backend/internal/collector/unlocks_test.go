package collector

import (
	"testing"
	"time"

	"datahunter/internal/unlock"
)

// Binance lists cheap tokens under a multiplier contract. A naive suffix trim
// would look up "1000PEPE" in the unlock source, find nothing, and record the
// coin as uncovered — and those low-float meme names are exactly the population
// this project is chasing, so the bias would point straight at the target.
func TestCoinFromSymbol(t *testing.T) {
	cases := map[string]string{
		"BTCUSDT":        "BTC",
		"1000PEPEUSDT":   "PEPE",
		"1000000MOGUSDT": "MOG",
		"10000SATSUSDT":  "SATS",
		"1000BONKUSDT":   "BONK",
		"ACEUSDT":        "ACE",
		// A ticker that merely starts with the digits must survive intact.
		"1000CATUSDT": "CAT",
		// Nothing to trim — the multiplier IS the whole name, so leave it be.
		"1000USDT": "1000",
	}
	for in, want := range cases {
		if got := coinFromSymbol(in); got != want {
			t.Errorf("coinFromSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildUnlockRowsNextUnlockAndTotals(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC).Unix()
	day := 20260817
	at := func(days int) int64 { return now + int64(days)*86400 }

	scheds := []unlock.Schedule{{
		Coin: "AAA", Name: "Alpha", Price: 2, Circ: 1_000_000, MaxSupply: 5_000_000,
		Events: []unlock.Event{
			// Two buckets releasing at the SAME moment — one cliff, and the
			// tradable size is the sum, not either slice.
			{Ts: at(10), Category: "投資人", Amount: 300},
			{Ts: at(10), Category: "內部人", Amount: 200},
			{Ts: at(30), Category: "生態", Amount: 1000},
			// Already in the past — must be excluded from the forward horizon.
			{Ts: now - 86400, Category: "投資人", Amount: 9999},
		},
	}}
	events, snaps := buildUnlockRows(scheds, map[string]bool{"AAA": true}, day, now)

	if len(snaps) != 1 {
		t.Fatalf("snaps = %d, want 1", len(snaps))
	}
	s := snaps[0]
	if !s.Covered || !s.HasUpcoming || !s.InUniverse {
		t.Errorf("flags covered/upcoming/universe = %v/%v/%v, want all true", s.Covered, s.HasUpcoming, s.InUniverse)
	}
	if s.NextUnlockTs != at(10) {
		t.Errorf("next_unlock_ts = %d, want %d", s.NextUnlockTs, at(10))
	}
	if s.NextUnlockAmt != 500 {
		t.Errorf("next_unlock_amt = %v, want 500 (both buckets on the same date)", s.NextUnlockAmt)
	}
	if s.HorizonAmt != 1500 {
		t.Errorf("horizon_amt = %v, want 1500 (past event excluded)", s.HorizonAmt)
	}
	if s.EventsN != 3 {
		t.Errorf("events_n = %d, want 3", s.EventsN)
	}
	if len(events) != 3 {
		t.Errorf("event rows = %d, want 3 (the past one must not be written)", len(events))
	}
	// as-of values are stored raw so magnitude stays derivable offline
	if s.Price != 2 || s.Circ != 1_000_000 {
		t.Errorf("price/circ = %v/%v, want 2/1000000", s.Price, s.Circ)
	}
}

// The three coverage states must stay distinguishable. Collapsing "not tracked"
// into "no unlock" would put coins that may well have had an unlock into the
// control group, which is the quiet way to manufacture a result.
func TestBuildUnlockRowsCoverageStates(t *testing.T) {
	now := time.Now().Unix()
	scheds := []unlock.Schedule{
		{Coin: "AAA", Events: []unlock.Event{{Ts: now + 86400, Category: "投資人", Amount: 10}}},
		{Coin: "BBB"}, // tracked by the source, but nothing scheduled
	}
	inUniverse := map[string]bool{"AAA": true, "BBB": true, "CCC": true}

	_, snaps := buildUnlockRows(scheds, inUniverse, 20260817, now)
	got := map[string]unlockSnapRow{}
	for _, s := range snaps {
		got[s.Coin] = s
	}
	if len(got) != 3 {
		t.Fatalf("snap coins = %d, want 3", len(got))
	}
	if !got["AAA"].Covered || !got["AAA"].HasUpcoming {
		t.Errorf("AAA should be covered with an upcoming unlock, got %+v", got["AAA"])
	}
	if !got["BBB"].Covered || got["BBB"].HasUpcoming {
		t.Errorf("BBB should be covered with NO upcoming unlock, got %+v", got["BBB"])
	}
	if got["CCC"].Covered || got["CCC"].HasUpcoming {
		t.Errorf("CCC is not tracked by the source — covered must stay 0, got %+v", got["CCC"])
	}
	if !got["CCC"].InUniverse {
		t.Errorf("CCC is tracked by the collector, so in_universe must be 1")
	}
}

// A coin absent from the tracked universe still gets recorded when the unlock
// source knows it — cheap, and it keeps history if the universe later rotates.
func TestBuildUnlockRowsKeepsOffUniverseSchedules(t *testing.T) {
	now := time.Now().Unix()
	scheds := []unlock.Schedule{{Coin: "ZZZ", Events: []unlock.Event{{Ts: now + 3600, Amount: 1}}}}
	_, snaps := buildUnlockRows(scheds, map[string]bool{}, 20260817, now)
	if len(snaps) != 1 || snaps[0].Coin != "ZZZ" {
		t.Fatalf("snaps = %+v, want one ZZZ row", snaps)
	}
	if snaps[0].InUniverse {
		t.Errorf("in_universe = true, want false")
	}
}
