package cache

import (
	"encoding/json"
	"log"
	"math"
	"strings"
	"time"
)

// strategy_ctl.go: admin controls shared by all strategies —
//   1. per-strategy on/off switch (disabled = won't OPEN new trades; open ones keep
//      running until they hit TP/SL). Persisted in site_config ("strat_disabled").
//   2. per-strategy tuning (類型 tags / 風控警語 / 最大止損% / 保本 / 分批止盈),
//      persisted as one JSON blob ("strat_cfg"); defaults mirror the code config.
//   3. manual exit of an open trade at market, recorded as 動能衰弱 (momdead).

// allStrategies is the canonical strategy set for the admin 開關 UI.
var allStrategies = []string{"main", "gamble", "emaonly", "conv", "bollfade", "meanrev", "bgv2", "bollema"}

// StratCfg is the admin-editable per-strategy tuning, persisted as one JSON blob
// in site_config ("strat_cfg"). Every field is seeded from stratDefaults — which
// mirror the hard-coded book config — so an untouched strategy behaves exactly as
// before. The on/off switch stays in its own key (strat_disabled) and is separate.
type StratCfg struct {
	Tags     []string `json:"tags"`       // 類型(可多選):激進/保守/高頻/低頻/長線/短線
	ShowRisk bool     `json:"show_risk"`  // 策略頁顯示「風險較大,請謹慎操作」警語
	MaxSLPct float64  `json:"max_sl_pct"` // >0: 止損距離超過此 % 就不開新單(0 = 不限制)

	// 出場模式,三選一。分批止盈與保本互斥:保本是靠 TP1 觸發的,
	// 兩者混用只會得到「開了保本卻永遠不觸發」的假設定。
	//   split      = 分批止盈(TP1/TP2/TP3 三段,TP1 後移保本、TP2 後鎖 TP1)
	//   breakeven  = 單段止盈 + 走到 BeAtPct 時把止損移到保本
	//   single     = 單段止盈,止損不動
	ExitMode string `json:"exit_mode"`

	// ── split 模式參數(百分比,0–100)──
	SplitA  float64 `json:"split_a"`  // TP1 位置:進場→最終止盈的百分之幾
	SplitB  float64 `json:"split_b"`  // TP2 位置
	SplitW1 float64 `json:"split_w1"` // TP1 平掉的倉位%
	SplitW2 float64 `json:"split_w2"` // TP2 平掉的倉位%
	SplitW3 float64 `json:"split_w3"` // TP3 平掉的倉位%

	// ── 保本參數(百分比)──
	BeAtPct  float64 `json:"be_at_pct"`  // breakeven 模式:走到止盈的百分之幾時移止損
	BeBufPct float64 `json:"be_buf_pct"` // 保本價緩衝:進場價 ±此%(避免剛好掃在進場價)

	// ── 保本位提示(純通知,永不改動止盈止損)──
	// 與上面的 breakeven 是兩回事:這個只發「🛡 已達保本位」通知,倉位照原本 TP/SL 跑。
	BeCuePct float64 `json:"be_cue_pct"` // >0 啟用,走到止盈的百分之幾時提示

	// ── 通知開關 ──
	NotifyOpen  bool `json:"notify_open"`  // 開倉
	NotifyClose bool `json:"notify_close"` // 平倉
	NotifyTP    bool `json:"notify_tp"`    // 分段止盈達成(TP1/TP2/TP3)
	NotifyBE    bool `json:"notify_be"`    // 已達保本位提示
}

// stratTags is the allowed 類型 vocabulary (three mutually-exclusive axes, but a
// strategy normally carries one from each, e.g. 超新星 = 激進+高頻+短線).
var stratTags = []string{"激進", "保守", "高頻", "低頻", "長線", "短線"}

// stratDefaults mirrors the code config of each book, so the admin panel opens
// showing what the strategy actually does today. Keep in sync with NewStore.
// 每一項都必須重現該策略「現在」的行為 —— split 的位置/比例直接抄自
// multitp.go 的三組預設,改 NewStore 或 tpPlan 時要同步這裡。
var stratDefaults = map[string]StratCfg{
	// 順勢組:tpMomentum(位置 40/70、分批 40/30/30)
	"main":    {Tags: []string{"保守", "低頻"}, ExitMode: "split", SplitA: 40, SplitB: 70, SplitW1: 40, SplitW2: 30, SplitW3: 30, BeBufPct: 0.05, NotifyOpen: true, NotifyClose: true, NotifyTP: true},
	"gamble":  {Tags: []string{"激進", "高頻", "短線"}, MaxSLPct: 12, ExitMode: "split", SplitA: 40, SplitB: 70, SplitW1: 40, SplitW2: 30, SplitW3: 30, BeBufPct: 0.05, NotifyOpen: true, NotifyClose: true, NotifyTP: true},
	"emaonly": {Tags: []string{"高頻", "短線"}, ExitMode: "split", SplitA: 40, SplitB: 70, SplitW1: 40, SplitW2: 30, SplitW3: 30, BeBufPct: 0.05, NotifyOpen: true, NotifyClose: true, NotifyTP: true},
	"conv":    {Tags: []string{"保守", "低頻", "長線"}, ExitMode: "split", SplitA: 40, SplitB: 70, SplitW1: 40, SplitW2: 30, SplitW3: 30, BeBufPct: 0.05, NotifyOpen: true, NotifyClose: true, NotifyTP: true},
	// bollfade/meanrev 用 tpMeanRevFront(45/60、60/25/15)—— K棒重播調校後的值
	"bollfade": {Tags: []string{"高頻", "短線"}, MaxSLPct: 10, ExitMode: "split", SplitA: 45, SplitB: 60, SplitW1: 60, SplitW2: 25, SplitW3: 15, BeBufPct: 0.05, NotifyTP: true},
	"meanrev":  {Tags: []string{"高頻", "短線"}, MaxSLPct: 10, ExitMode: "split", SplitA: 45, SplitB: 60, SplitW1: 60, SplitW2: 25, SplitW3: 15, BeBufPct: 0.05, NotifyTP: true},
	// 單段止盈組:照回測規格原樣上線,不分批
	"bgv2": {Tags: []string{"保守", "低頻", "長線"}, ExitMode: "single"},
	// 布林EMA:單段 1:3 RR,並有「走到 30% 發保本位提示」的純通知機制(不動止損)
	"bollema": {Tags: []string{"保守", "低頻", "長線"}, ExitMode: "single", BeCuePct: 30, NotifyBE: true},
}

// StrategyState is one strategy's row for the admin UI: on/off + editable config.
type StrategyState struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	StratCfg
}

// loadStratOff restores the disabled-strategy set from site_config at startup.
func (s *Store) loadStratOff() {
	if s.db == nil {
		return
	}
	off := map[string]bool{}
	for _, n := range strings.Split(s.db.getConfig("strat_disabled"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			off[n] = true
		}
	}
	s.stratMu.Lock()
	s.stratOff = off
	s.stratMu.Unlock()
}

// StrategyEnabled reports whether a strategy may open new trades.
func (s *Store) StrategyEnabled(name string) bool {
	s.stratMu.RLock()
	defer s.stratMu.RUnlock()
	return !s.stratOff[name]
}

// SetStrategyEnabled toggles a strategy's entry switch (admin) and persists it.
func (s *Store) SetStrategyEnabled(name string, on bool) {
	s.stratMu.Lock()
	if on {
		delete(s.stratOff, name)
	} else {
		s.stratOff[name] = true
	}
	var off []string
	for n := range s.stratOff {
		off = append(off, n)
	}
	s.stratMu.Unlock()
	if s.db != nil {
		s.db.setConfig("strat_disabled", strings.Join(off, ","))
	}
}

// loadStratCfg restores the admin per-strategy config blob at startup.
func (s *Store) loadStratCfg() {
	cfg := map[string]StratCfg{}
	if s.db != nil {
		if raw := s.db.getConfig("strat_cfg"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				log.Printf("strat_cfg: bad json, falling back to defaults: %v", err)
				cfg = map[string]StratCfg{}
			}
		}
	}
	s.stratMu.Lock()
	s.stratCfg = cfg
	s.stratMu.Unlock()
}

// StratConfigOf returns a strategy's effective config: the code default, with any
// admin override applied. Unknown names get the zero config (no filter, no split).
func (s *Store) StratConfigOf(name string) StratCfg {
	s.stratMu.RLock()
	defer s.stratMu.RUnlock()
	return s.stratCfgLocked(name)
}

// stratCfgLocked resolves default+override. Caller holds stratMu (R or W).
func (s *Store) stratCfgLocked(name string) StratCfg {
	if c, ok := s.stratCfg[name]; ok {
		return c
	}
	return stratDefaults[name]
}

// SetStrategyConfig stores an admin edit and persists the whole blob. Tags are
// filtered to the known vocabulary and MaxSLPct is clamped to a sane range.
func (s *Store) SetStrategyConfig(name string, c StratCfg) bool {
	if _, ok := stratDefaults[name]; !ok {
		return false
	}
	var tags []string
	for _, t := range c.Tags {
		for _, ok := range stratTags {
			if t == ok {
				tags = append(tags, t)
				break
			}
		}
	}
	c.Tags = tags
	clamp := func(v float64, lo, hi float64) float64 {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	// 切到「保本/單段」時,表單不會送分段參數 —— 若直接夾到下限,使用者切回分批
	// 就會拿到壞掉的 1/1。沒送到的分段參數一律沿用原本的值。
	prev := s.StratConfigOf(name)
	if c.SplitA == 0 {
		c.SplitA = prev.SplitA
	}
	if c.SplitB == 0 {
		c.SplitB = prev.SplitB
	}
	if c.SplitW1 == 0 && c.SplitW2 == 0 && c.SplitW3 == 0 {
		c.SplitW1, c.SplitW2, c.SplitW3 = prev.SplitW1, prev.SplitW2, prev.SplitW3
	}
	c.MaxSLPct = clamp(c.MaxSLPct, 0, 100)
	c.SplitA = clamp(c.SplitA, 1, 99)
	c.SplitB = clamp(c.SplitB, 1, 99)
	c.SplitW1, c.SplitW2, c.SplitW3 = clamp(c.SplitW1, 0, 100), clamp(c.SplitW2, 0, 100), clamp(c.SplitW3, 0, 100)
	c.BeAtPct = clamp(c.BeAtPct, 0, 99)
	c.BeBufPct = clamp(c.BeBufPct, 0, 5)
	c.BeCuePct = clamp(c.BeCuePct, 0, 99)
	if c.ExitMode != "split" && c.ExitMode != "breakeven" && c.ExitMode != "single" {
		c.ExitMode = "single"
	}
	if c.ExitMode == "split" && c.SplitB <= c.SplitA {
		c.SplitB = math.Min(99, c.SplitA+10) // TP2 必須在 TP1 之後,否則分段會退化
	}
	s.stratMu.Lock()
	s.stratCfg[name] = c
	blob, err := json.Marshal(s.stratCfg)
	s.stratMu.Unlock()
	if err == nil && s.db != nil {
		s.db.setConfig("strat_cfg", string(blob))
	}
	return true
}

// ResetStrategyConfig drops a strategy's override so it falls back to the code
// default. Without this a bad edit (or a config written by an older build) can
// only be undone by hand-editing the DB.
func (s *Store) ResetStrategyConfig(name string) bool {
	if _, ok := stratDefaults[name]; !ok {
		return false
	}
	s.stratMu.Lock()
	delete(s.stratCfg, name)
	blob, err := json.Marshal(s.stratCfg)
	s.stratMu.Unlock()
	if err == nil && s.db != nil {
		s.db.setConfig("strat_cfg", string(blob))
	}
	return true
}

// stratMaxSL is the entry-time SL-distance cap for a strategy: the admin override
// when set, else the book's own hard-coded value.
func (s *Store) stratMaxSL(name string, bookDefault float64) float64 {
	s.stratMu.RLock()
	defer s.stratMu.RUnlock()
	if _, ok := s.stratCfg[name]; ok {
		return s.stratCfgLocked(name).MaxSLPct
	}
	return bookDefault
}

// tpFor resolves a strategy's runtime 分批止盈 setup from its admin config.
// Returns the plan to use (nil = 單段止盈) and whether the TP1→保本 stop move is on.
// The plan is built from the configured percentages, so an admin edit takes effect
// on the next entry without touching code. `base` only supplies minSplitPct.
func (s *Store) tpFor(name string, base *tpPlan) (*tpPlan, bool) {
	c := s.StratConfigOf(name)
	if c.ExitMode != "split" {
		return nil, false // 保本/單段模式沒有分段,TP1→保本 自然也不適用
	}
	minSplit := 0.008
	if base != nil && base.minSplitPct > 0 {
		minSplit = base.minSplitPct
	}
	w1, w2, w3 := c.SplitW1/100, c.SplitW2/100, c.SplitW3/100
	if sum := w1 + w2 + w3; sum > 0 && (sum < 0.999 || sum > 1.001) {
		w1, w2, w3 = w1/sum, w2/sum, w3/sum // 比例沒加到 100% 就正規化,免得倉位算錯
	}
	return &tpPlan{
		a: c.SplitA / 100, b: c.SplitB / 100,
		w1: w1, w2: w2, w3: w3,
		beBuf: c.BeBufPct / 100, minSplitPct: minSplit,
	}, true
}

// beFor returns the 保本 (stop-moving) trigger fraction for a strategy, or 0 when
// that mode is off. Distinct from beCueFor — this one MOVES the stop.
func (s *Store) beFor(name string) (float64, float64) {
	c := s.StratConfigOf(name)
	if c.ExitMode != "breakeven" || c.BeAtPct <= 0 {
		return 0, 0
	}
	return c.BeAtPct / 100, c.BeBufPct / 100
}

// beCueFor returns the 保本位提示 fraction (notify only, never touches TP/SL).
func (s *Store) beCueFor(name string, bookDefault float64) float64 {
	c := s.StratConfigOf(name)
	if _, ok := s.stratCfg[name]; !ok && c.BeCuePct == 0 {
		return bookDefault // 沒有覆寫過就沿用書本身的設定
	}
	return c.BeCuePct / 100
}

// stratKeyOf maps a DB book name to its 開關/設定 key. Family legs (布乖v2 的兩腿)
// share one key, so notifications and config resolve to the same strategy.
func stratKeyOf(book string) string {
	switch book {
	case "bgv2dev", "bgv2boll":
		return "bgv2"
	}
	return book
}

// notifyOn reports whether a strategy should send a given notification kind.
func (s *Store) notifyOn(book, kind string) bool {
	c := s.StratConfigOf(stratKeyOf(book))
	switch kind {
	case "open":
		return c.NotifyOpen
	case "close":
		return c.NotifyClose
	case "tp":
		return c.NotifyTP
	case "be":
		return c.NotifyBE
	}
	return false
}

// StrategyStates returns every strategy's on/off state + config for the admin panel.
func (s *Store) StrategyStates() []StrategyState {
	s.stratMu.RLock()
	defer s.stratMu.RUnlock()
	out := make([]StrategyState, 0, len(allStrategies))
	for _, n := range allStrategies {
		out = append(out, StrategyState{Name: n, Label: bookLabel(n), Enabled: !s.stratOff[n], StratCfg: s.stratCfgLocked(n)})
	}
	return out
}

// StratMeta is the public (non-admin) view of a strategy's config: just what the
// strategy pages render — 類型 tags and the risk-warning flag.
type StratMeta struct {
	Tags     []string `json:"tags"`
	ShowRisk bool     `json:"show_risk"`
}

// StrategyMeta returns name → public meta for every strategy, for the frontend to
// render 策略類型 and the 風控警語 without exposing the admin-only fields.
func (s *Store) StrategyMeta() map[string]StratMeta {
	s.stratMu.RLock()
	defer s.stratMu.RUnlock()
	out := make(map[string]StratMeta, len(allStrategies))
	for _, n := range allStrategies {
		c := s.stratCfgLocked(n)
		out[n] = StratMeta{Tags: c.Tags, ShowRisk: c.ShowRisk}
	}
	return out
}

// ManualExit force-closes an open trade (by id) in any strategy at the live price,
// recorded as 動能衰弱 (momdead). Returns false if no matching open trade. 銀河's own
// 逾時 exit (ManualCloseEMA) is separate; this covers every other book.
func (s *Store) ManualExit(book, id string) bool {
	px := s.livePrices()
	now := time.Now()
	closeIn := func(trades []*PaperTrade) *PaperTrade {
		for _, tr := range trades {
			if tr.ID == id && tr.Status == "open" {
				exit := px[tr.Coin]
				if exit <= 0 {
					exit = tr.Cur
				}
				if exit <= 0 {
					exit = tr.Entry
				}
				closeTrade(tr, exit, "momdead", now) // blends any realized 分批 tranches
				return tr
			}
		}
		return nil
	}
	var done *PaperTrade
	dbBook := book // 家族的 DB book 名 != 開關 key,需記住實際那一腿
	switch book {
	case "main", "gamble":
		s.paperMu.Lock()
		b := s.paperMain
		switch book {
		case "gamble":
			b = s.paperGamble
		}
		done = closeIn(b.trades)
		s.paperMu.Unlock()
	case "bollfade":
		s.bollFadeBook.mu.Lock()
		done = closeIn(s.bollFadeBook.trades)
		s.bollFadeBook.mu.Unlock()
	case "meanrev":
		s.meanRevBook.mu.Lock()
		done = closeIn(s.meanRevBook.trades)
		s.meanRevBook.mu.Unlock()
	case "bollema":
		s.bollEMABook.mu.Lock()
		done = closeIn(s.bollEMABook.trades)
		s.bollEMABook.mu.Unlock()
	case "bgv2": // 家族:兩腿都找,並記下命中的那一腿以便寫回正確的 DB book
		for _, b := range []*microBook{s.bgv2Dev, s.bgv2Boll} {
			b.mu.Lock()
			if tr := closeIn(b.trades); tr != nil {
				done, dbBook = tr, b.name
			}
			b.mu.Unlock()
			if done != nil {
				break
			}
		}
	case "conv":
		s.convMu.Lock()
		done = closeIn(s.convTrades)
		s.convMu.Unlock()
	default:
		return false
	}
	if done == nil {
		return false
	}
	if s.db != nil {
		s.db.upsertTrade(dbBook, done)
	}
	return true
}
