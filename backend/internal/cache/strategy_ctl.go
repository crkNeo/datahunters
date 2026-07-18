package cache

import (
	"encoding/json"
	"log"
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
var allStrategies = []string{"main", "gamble", "emaonly", "conv", "rsifade", "bollfade", "meanrev", "bgv2", "bollema"}

// StratCfg is the admin-editable per-strategy tuning, persisted as one JSON blob
// in site_config ("strat_cfg"). Every field is seeded from stratDefaults — which
// mirror the hard-coded book config — so an untouched strategy behaves exactly as
// before. The on/off switch stays in its own key (strat_disabled) and is separate.
type StratCfg struct {
	Tags      []string `json:"tags"`       // 類型(可多選):激進/保守/高頻/低頻/長線/短線
	ShowRisk  bool     `json:"show_risk"`  // 策略頁顯示「風險較大,請謹慎操作」警語
	MaxSLPct  float64  `json:"max_sl_pct"` // >0: 止損距離超過此 % 就不開新單(0 = 不限制)
	Breakeven bool     `json:"breakeven"`  // TP1 觸及後把止損移到保本
	MultiTP   bool     `json:"multi_tp"`   // 分批止盈(關閉 = 單段止盈)
}

// stratTags is the allowed 類型 vocabulary (three mutually-exclusive axes, but a
// strategy normally carries one from each, e.g. 超新星 = 激進+高頻+短線).
var stratTags = []string{"激進", "保守", "高頻", "低頻", "長線", "短線"}

// stratDefaults mirrors the code config of each book, so the admin panel opens
// showing what the strategy actually does today. Keep in sync with NewStore.
var stratDefaults = map[string]StratCfg{
	"main":     {Tags: []string{"保守", "低頻"}, MaxSLPct: 0, Breakeven: true, MultiTP: true},
	"gamble":   {Tags: []string{"激進", "高頻", "短線"}, MaxSLPct: 12, Breakeven: true, MultiTP: true},
	"emaonly":  {Tags: []string{"高頻", "短線"}, MaxSLPct: 0, Breakeven: true, MultiTP: true},
	"conv":     {Tags: []string{"保守", "低頻", "長線"}, MaxSLPct: 0, Breakeven: true, MultiTP: true},
	"rsifade":  {Tags: []string{"激進", "高頻", "短線"}, MaxSLPct: 10, Breakeven: true, MultiTP: true},
	"bollfade": {Tags: []string{"高頻", "短線"}, MaxSLPct: 10, Breakeven: true, MultiTP: true},
	"meanrev":  {Tags: []string{"高頻", "短線"}, MaxSLPct: 10, Breakeven: true, MultiTP: true},
	"bgv2":     {Tags: []string{"保守", "低頻", "長線"}, MaxSLPct: 0, Breakeven: false, MultiTP: false},
	// 布林EMA:單段止盈(1:3 RR)、無濾網、最長 180 根 4H(≈30天)。Breakeven=false 指的是
	// 「TP1→移止損」那種保本(它沒有分批所以沒有 TP1);它自己的 beAt=0.3 保本位是純通知,
	// 不受這個開關影響。
	"bollema": {Tags: []string{"保守", "低頻", "長線"}, MaxSLPct: 0, Breakeven: false, MultiTP: false},
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
	if c.MaxSLPct < 0 {
		c.MaxSLPct = 0
	}
	if c.MaxSLPct > 100 {
		c.MaxSLPct = 100
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

// tpFor resolves a strategy's runtime 分批止盈 setup. Returns the plan to use (nil
// = single TP) and whether the TP1→保本 stop move is enabled. A book with no base
// plan that the admin switches ON falls back to tpMomentum.
func (s *Store) tpFor(name string, base *tpPlan) (*tpPlan, bool) {
	c := s.StratConfigOf(name)
	if !c.MultiTP {
		return nil, c.Breakeven
	}
	if base == nil {
		return tpMomentum, c.Breakeven
	}
	return base, c.Breakeven
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
	case "rsifade":
		s.rsiFadeBook.mu.Lock()
		done = closeIn(s.rsiFadeBook.trades)
		s.rsiFadeBook.mu.Unlock()
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
