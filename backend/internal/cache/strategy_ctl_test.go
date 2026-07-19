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
		name  string
		maxSL float64
		mode  string
		a, b  float64 // split 位置
		w1    float64 // TP1 平倉比例
	}{
		{"gamble", 12, "split", 40, 70, 40},   // FILTER@12% + tpMomentum
		{"main", 0, "split", 40, 70, 40},      // tpMomentum
		{"conv", 0, "split", 40, 70, 40},      // tpMomentum(原本寫死在 convergence.go)
		{"meanrev", 10, "split", 45, 60, 60},  // tpMeanRevFront(K棒重播調校)
		{"bollfade", 10, "split", 45, 60, 60}, // tpMeanRevFront
		{"bgv2", 0, "single", 0, 0, 0},        // 單段止盈、無濾網
		{"bollema", 0, "single", 0, 0, 0},     // 單段 + 保本位提示
	} {
		got := s.StratConfigOf(tc.name)
		if got.MaxSLPct != tc.maxSL {
			t.Errorf("%s MaxSLPct = %v, want %v", tc.name, got.MaxSLPct, tc.maxSL)
		}
		if got.ExitMode != tc.mode {
			t.Errorf("%s ExitMode = %q, want %q", tc.name, got.ExitMode, tc.mode)
		}
		if tc.mode == "split" && (got.SplitA != tc.a || got.SplitB != tc.b || got.SplitW1 != tc.w1) {
			t.Errorf("%s split = %v/%v w1=%v, want %v/%v w1=%v",
				tc.name, got.SplitA, got.SplitB, got.SplitW1, tc.a, tc.b, tc.w1)
		}
	}
}

// 預設值必須真的還原出與 multitp.go 三組 preset 相同的計畫,否則「沒動過的策略
// 行為不變」這個保證就是空的。
func TestDefaultsReproducePresetPlans(t *testing.T) {
	s := newCfgStore()
	for _, tc := range []struct {
		name string
		want *tpPlan
	}{
		{"gamble", tpMomentum},
		{"meanrev", tpMeanRevFront},
	} {
		got, be := s.tpFor(tc.name, tc.want)
		if !be {
			t.Errorf("%s: breakeven-on-TP1 should be enabled in split mode", tc.name)
		}
		if got == nil {
			t.Fatalf("%s: got nil plan", tc.name)
		}
		near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
		if !near(got.a, tc.want.a) || !near(got.b, tc.want.b) ||
			!near(got.w1, tc.want.w1) || !near(got.w2, tc.want.w2) || !near(got.w3, tc.want.w3) ||
			!near(got.beBuf, tc.want.beBuf) {
			t.Errorf("%s: rebuilt plan %+v != preset %+v", tc.name, *got, *tc.want)
		}
	}
}

// 分批比例沒加到 100 時要正規化,不能讓倉位算超過或不足。
func TestSplitWeightsNormalised(t *testing.T) {
	s := newCfgStore()
	s.SetStrategyConfig("meanrev", StratCfg{ExitMode: "split", SplitA: 40, SplitB: 70, SplitW1: 50, SplitW2: 50, SplitW3: 50})
	p, _ := s.tpFor("meanrev", nil)
	if sum := p.w1 + p.w2 + p.w3; sum < 0.999 || sum > 1.001 {
		t.Errorf("weights sum to %v, want 1", sum)
	}
}

// 出場模式互斥:非 split 模式不得回傳分段計畫。
func TestExitModesAreExclusive(t *testing.T) {
	s := newCfgStore()
	s.SetStrategyConfig("meanrev", StratCfg{ExitMode: "breakeven", BeAtPct: 50, BeBufPct: 0.05})
	if p, be := s.tpFor("meanrev", tpMeanRevFront); p != nil || be {
		t.Errorf("breakeven 模式仍回傳分段計畫: plan=%v be=%v", p, be)
	}
	if at, buf := s.beFor("meanrev"); at != 0.5 || buf != 0.0005 {
		t.Errorf("beFor = %v/%v, want 0.5/0.0005", at, buf)
	}
	// split 模式不應該有獨立保本觸發
	s.SetStrategyConfig("meanrev", StratCfg{ExitMode: "split", SplitA: 45, SplitB: 60, SplitW1: 60, SplitW2: 25, SplitW3: 15, BeAtPct: 50})
	if at, _ := s.beFor("meanrev"); at != 0 {
		t.Errorf("split 模式不該有獨立保本觸發,得到 %v", at)
	}
}

// TP2 必須在 TP1 之後,否則分段會退化成單段。
func TestSplitOrderingEnforced(t *testing.T) {
	s := newCfgStore()
	s.SetStrategyConfig("meanrev", StratCfg{ExitMode: "split", SplitA: 60, SplitB: 30, SplitW1: 50, SplitW2: 30, SplitW3: 20})
	if got := s.StratConfigOf("meanrev"); got.SplitB <= got.SplitA {
		t.Errorf("SplitB=%v 未被修正到 SplitA=%v 之後", got.SplitB, got.SplitA)
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
	s.SetStrategyConfig("meanrev", StratCfg{MaxSLPct: 6, ExitMode: "split", SplitA: 45, SplitB: 60, SplitW1: 60, SplitW2: 25, SplitW3: 15})
	if got := s.stratMaxSL("meanrev", 10); got != 6 {
		t.Fatalf("after override: got %v, want 6", got)
	}
	// 0 means "no cap" and must override a non-zero book default, not fall back.
	s.SetStrategyConfig("meanrev", StratCfg{MaxSLPct: 0, ExitMode: "split", SplitA: 45, SplitB: 60, SplitW1: 60, SplitW2: 25, SplitW3: 15})
	if got := s.stratMaxSL("meanrev", 10); got != 0 {
		t.Fatalf("explicit 0: got %v, want 0 (no cap)", got)
	}
}

// 通知開關要逐項獨立生效。
func TestNotifyToggles(t *testing.T) {
	s := newCfgStore()
	if !s.notifyOn("gamble", "open") || !s.notifyOn("gamble", "tp") {
		t.Error("gamble 預設應開啟開倉/止盈通知")
	}
	if s.notifyOn("bollfade", "open") {
		t.Error("bollfade 預設不該發開倉通知(管理員觀察書)")
	}
	s.SetStrategyConfig("gamble", StratCfg{ExitMode: "single", NotifyClose: true})
	if s.notifyOn("gamble", "open") || !s.notifyOn("gamble", "close") {
		t.Error("通知開關未逐項生效")
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

// 獨立保本模式:走到設定的百分比才移止損,且只移一次。
func TestApplyBreakeven(t *testing.T) {
	mk := func() *PaperTrade { return &PaperTrade{Dir: "long", Entry: 100, SL: 90, TP: 120} }
	tr := mk()
	if applyBreakeven(tr, 105, 0.5, 0.0005) { // 走到 25%,還沒到 50%
		t.Error("未達觸發點就移了止損")
	}
	if tr.SL != 90 {
		t.Errorf("SL 不該變動,得到 %v", tr.SL)
	}
	if !applyBreakeven(tr, 110, 0.5, 0.0005) { // 剛好 50%
		t.Fatal("達到觸發點卻沒移止損")
	}
	if tr.SL <= 100 {
		t.Errorf("SL = %v,應移到進場價之上", tr.SL)
	}
	if applyBreakeven(tr, 115, 0.5, 0.0005) {
		t.Error("保本應只觸發一次")
	}
	// 空單鏡像:止盈在下方,價格跌到一半時觸發
	sh := &PaperTrade{Dir: "short", Entry: 100, SL: 110, TP: 80}
	if !applyBreakeven(sh, 90, 0.5, 0.0005) {
		t.Fatal("空單未觸發保本")
	}
	if sh.SL >= 100 {
		t.Errorf("空單 SL = %v,應移到進場價之下", sh.SL)
	}
}

// 切換到保本/單段模式時,分段設定必須保留 —— 否則切回分批會拿到壞掉的 1/1。
func TestSwitchingModeKeepsSplitSettings(t *testing.T) {
	s := newCfgStore()
	base := s.StratConfigOf("meanrev") // 45/60、60/25/15
	// 模擬前端切到保本模式:只送保本相關欄位,不送分段參數
	s.SetStrategyConfig("meanrev", StratCfg{ExitMode: "breakeven", BeAtPct: 50, BeBufPct: 0.05, MaxSLPct: 10})
	got := s.StratConfigOf("meanrev")
	if got.SplitA != base.SplitA || got.SplitB != base.SplitB || got.SplitW1 != base.SplitW1 {
		t.Errorf("切到保本後分段設定被清掉: %v/%v w1=%v,原本 %v/%v w1=%v",
			got.SplitA, got.SplitB, got.SplitW1, base.SplitA, base.SplitB, base.SplitW1)
	}
	// 切回分批應該還是原本那組
	s.SetStrategyConfig("meanrev", StratCfg{ExitMode: "split", MaxSLPct: 10})
	if p, _ := s.tpFor("meanrev", nil); p == nil || p.a != base.SplitA/100 || p.b != base.SplitB/100 {
		t.Errorf("切回分批後計畫不正確: %+v", p)
	}
}

// 手動出場必須發通知,而且要繞過「平倉通知」開關 —— 管理員專屬觀察書預設是關的,
// 若不繞過,手動平倉會像沒發生過一樣安靜(這正是原本的災情)。
func TestManualExitAlwaysNotifies(t *testing.T) {
	s := &Store{stratCfg: map[string]StratCfg{}, tabPerms: map[string]string{}, notifier: nil}
	tr := &PaperTrade{Coin: "BTC", Dir: "long", Entry: 100, Cur: 101, PnLPct: 1, Outcome: "momdead", OpenTime: time.Now()}

	// bollfade 預設 NotifyClose=false → 自動平倉不通知
	if s.notifyOn("bollfade", "close") {
		t.Fatal("前提錯了:bollfade 預設應該是不發平倉通知")
	}
	if s.notifyCloseBook("bollfade", tr, time.Now(), false) {
		t.Error("自動平倉不該通知(開關是關的)")
	}
	// 手動出場 force=true → 一律通知
	if !s.notifyCloseBook("bollfade", tr, time.Now(), true) {
		t.Error("手動出場沒有發通知")
	}
	// 家族分腿(bgv2dev)要能解析回家族 key,不能因為查不到設定而漏發
	if !s.notifyCloseBook("bgv2dev", tr, time.Now(), true) {
		t.Error("布乖v2 分腿的手動出場沒有發通知")
	}
}
