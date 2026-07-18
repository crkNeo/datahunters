package cache

import (
	"testing"
	"time"
)

// newCfgStore builds the minimal Store the strategy-config helpers need (no DB,
// no feed): they only touch stratCfg + stratMu.
func newCfgStore() *Store { return &Store{stratCfg: map[string]StratCfg{}} }

// An untouched strategy must behave exactly as the hard-coded book config, so
// adding the admin panel can't silently change live behaviour.
func TestStratDefaultsMirrorCode(t *testing.T) {
	s := newCfgStore()
	for _, tc := range []struct {
		name    string
		maxSL   float64
		multiTP bool
	}{
		{"gamble", 12, true},   // FILTER@12%
		{"meanrev", 10, true},  // maxSL 10 + tpMeanRevFront
		{"bollfade", 10, true}, //
		{"main", 0, true},      // no SL filter, tpMomentum
		{"bgv2", 0, false},     // 單段止盈、無濾網
	} {
		got := s.StratConfigOf(tc.name)
		if got.MaxSLPct != tc.maxSL {
			t.Errorf("%s MaxSLPct = %v, want %v", tc.name, got.MaxSLPct, tc.maxSL)
		}
		if got.MultiTP != tc.multiTP {
			t.Errorf("%s MultiTP = %v, want %v", tc.name, got.MultiTP, tc.multiTP)
		}
	}
}

// Every strategy in the admin list needs its own defaults AND its own bookLabel
// case — a missing label silently falls through to "星軌", which is how bgv2 and
// bollema ended up mislabelled in the panel.
func TestEveryStrategyIsFullyRegistered(t *testing.T) {
	for _, n := range allStrategies {
		if _, ok := stratDefaults[n]; !ok {
			t.Errorf("%s: missing from stratDefaults", n)
		}
		if lbl := bookLabel(n); lbl == "星軌" && n != "main" {
			t.Errorf("%s: bookLabel fell through to 星軌 (no case for it)", n)
		}
	}
}

// stratMaxSL falls back to the book's own value until an admin override exists.
func TestStratMaxSLOverride(t *testing.T) {
	s := newCfgStore()
	if got := s.stratMaxSL("meanrev", 10); got != 10 {
		t.Fatalf("no override: got %v, want book default 10", got)
	}
	s.SetStrategyConfig("meanrev", StratCfg{MaxSLPct: 6, MultiTP: true, Breakeven: true})
	if got := s.stratMaxSL("meanrev", 10); got != 6 {
		t.Fatalf("after override: got %v, want 6", got)
	}
	// 0 means "no cap" and must override a non-zero book default, not fall back.
	s.SetStrategyConfig("meanrev", StratCfg{MaxSLPct: 0, MultiTP: true})
	if got := s.stratMaxSL("meanrev", 10); got != 0 {
		t.Fatalf("explicit 0: got %v, want 0 (no cap)", got)
	}
}

// tpFor resolves the runtime 分批止盈 / 保本 setup.
func TestTpFor(t *testing.T) {
	s := newCfgStore()
	// default meanrev: split on, breakeven on → keeps the book's own plan
	if plan, be := s.tpFor("meanrev", tpMeanRevFront); plan != tpMeanRevFront || !be {
		t.Errorf("default: plan=%v be=%v, want book plan + be", plan, be)
	}
	// 分批止盈 off → single TP regardless of the book plan
	s.SetStrategyConfig("meanrev", StratCfg{MultiTP: false, Breakeven: true})
	if plan, _ := s.tpFor("meanrev", tpMeanRevFront); plan != nil {
		t.Errorf("multiTP off: plan = %v, want nil", plan)
	}
	// 保本 off → plan kept, breakeven flag false
	s.SetStrategyConfig("meanrev", StratCfg{MultiTP: true, Breakeven: false})
	if plan, be := s.tpFor("meanrev", tpMeanRevFront); plan == nil || be {
		t.Errorf("be off: plan=%v be=%v, want plan + false", plan, be)
	}
	// a single-TP book switched ON falls back to tpMomentum (it has no plan)
	s.SetStrategyConfig("bgv2", StratCfg{MultiTP: true})
	if plan, _ := s.tpFor("bgv2", nil); plan != tpMomentum {
		t.Errorf("bgv2 multiTP on: plan = %v, want tpMomentum", plan)
	}
}

// Tags are filtered to the known vocabulary; MaxSLPct is clamped; unknown
// strategies are rejected outright.
func TestSetStrategyConfigValidation(t *testing.T) {
	s := newCfgStore()
	if s.SetStrategyConfig("nope", StratCfg{}) {
		t.Error("unknown strategy accepted")
	}
	s.SetStrategyConfig("gamble", StratCfg{Tags: []string{"激進", "亂打的", "短線"}, MaxSLPct: 999})
	got := s.StratConfigOf("gamble")
	if len(got.Tags) != 2 || got.Tags[0] != "激進" || got.Tags[1] != "短線" {
		t.Errorf("tags = %v, want [激進 短線]", got.Tags)
	}
	if got.MaxSLPct != 100 {
		t.Errorf("MaxSLPct = %v, want clamped to 100", got.MaxSLPct)
	}
}

// stepTP must not move the stop to break-even when 保本 is off.
func TestStepTPBreakevenGate(t *testing.T) {
	mk := func() *PaperTrade {
		return &PaperTrade{Dir: "long", Entry: 100, SL: 90, TP: 110, TP1: 104.5, TP2: 106}
	}
	p := tpMeanRevFront
	on := mk()
	stepTP(on, 105, p, true, time.Now()) // TP1 filled, 保本 on
	if on.SL <= 100 {
		t.Errorf("be on: SL = %v, want moved above entry", on.SL)
	}
	off := mk()
	stepTP(off, 105, p, false, time.Now()) // TP1 filled, 保本 off
	if off.SL != 90 {
		t.Errorf("be off: SL = %v, want original 90", off.SL)
	}
	if off.Legs != 1 {
		t.Errorf("be off: Legs = %d, want 1 (leg still books)", off.Legs)
	}
}
