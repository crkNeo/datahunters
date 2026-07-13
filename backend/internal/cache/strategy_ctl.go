package cache

import (
	"strings"
	"time"
)

// strategy_ctl.go: admin controls shared by all strategies —
//   1. per-strategy on/off switch (disabled = won't OPEN new trades; open ones keep
//      running until they hit TP/SL). Persisted in site_config.
//   2. manual exit of an open trade at market, recorded as 動能衰弱 (momdead).

// allStrategies is the canonical strategy set for the admin 開關 UI.
var allStrategies = []string{"main", "gamble", "gambleA", "gambleB", "emaonly", "conv", "pool", "rsifade", "bollfade", "meanrev"}

// StrategyState is one strategy's on/off row for the admin UI.
type StrategyState struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
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

// StrategyStates returns the on/off state of every strategy for the admin panel.
func (s *Store) StrategyStates() []StrategyState {
	s.stratMu.RLock()
	defer s.stratMu.RUnlock()
	out := make([]StrategyState, 0, len(allStrategies))
	for _, n := range allStrategies {
		out = append(out, StrategyState{Name: n, Label: bookLabel(n), Enabled: !s.stratOff[n]})
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
	switch book {
	case "main", "gamble", "gambleA", "gambleB":
		s.paperMu.Lock()
		b := s.paperMain
		switch book {
		case "gamble":
			b = s.paperGamble
		case "gambleA":
			b = s.paperGambleA
		case "gambleB":
			b = s.paperGambleB
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
	case "conv":
		s.convMu.Lock()
		done = closeIn(s.convTrades)
		s.convMu.Unlock()
	case "pool":
		s.poolMu.Lock()
		done = closeIn(s.poolTrades)
		s.poolMu.Unlock()
	default:
		return false
	}
	if done == nil {
		return false
	}
	if s.db != nil {
		s.db.upsertTrade(book, done)
	}
	return true
}
