package cache

import (
	"fmt"
	"sort"
	"time"
)

// robinhood.go: the public Robinhood 上架 board. Polls Robinhood's crypto
// currency-pair list, alerts (TG + Web Push) when a coin becomes newly tradable,
// and keeps the full tradable list for the /api/robinhood tab.

const rhNewWindowMs = 14 * 24 * 3600 * 1000 // keep a "new" badge for 14 days

// RHCoin is one tradable Robinhood crypto for the board.
type RHCoin struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
	New    bool   `json:"new"`             // detected as newly listed (within the window)
	Since  int64  `json:"since,omitempty"` // first-seen unix ms (new coins only)
}

// RHData is the Robinhood-tab payload (newly-listed coins first).
type RHData struct {
	Coins     []RHCoin `json:"coins"`
	UpdatedAt string   `json:"updated_at"`
}

// RobinhoodTick polls Robinhood's tradable list, pushes any newly-tradable coins,
// and rebuilds the board. The first tick only seeds the baseline (no history push).
func (s *Store) RobinhoodTick() {
	if s.rhW == nil {
		return
	}
	fresh, all, err := s.rhW.Poll()
	if err != nil {
		s.apiFail("Robinhood 上架", err.Error())
		return
	}
	s.apiOK("Robinhood 上架")

	now := time.Now()
	s.rhMu.Lock()
	for _, c := range fresh {
		s.rhNew[c.Code] = now.UnixMilli()
	}
	cutoff := now.UnixMilli() - rhNewWindowMs
	for k, t := range s.rhNew { // prune stale "new" marks
		if t < cutoff {
			delete(s.rhNew, k)
		}
	}
	board := make([]RHCoin, 0, len(all))
	for _, c := range all {
		rc := RHCoin{Code: c.Code, Name: c.Name, Symbol: c.Symbol}
		if t, ok := s.rhNew[c.Code]; ok {
			rc.New, rc.Since = true, t
		}
		board = append(board, rc)
	}
	sort.SliceStable(board, func(i, j int) bool { // new first (newest-added first), else by code
		if board[i].New != board[j].New {
			return board[i].New
		}
		if board[i].New && board[j].New {
			return board[i].Since > board[j].Since
		}
		return board[i].Code < board[j].Code
	})
	s.rhBoard = board
	s.rhTime = now
	s.rhMu.Unlock()

	for _, c := range fresh { // alert each newly-tradable coin
		s.PushSend("🤖 Robinhood 上架", c.Code+" 已可交易", "/?tab=robinhood")
		if s.notifier.Enabled() {
			go s.notifier.Send(fmt.Sprintf("🤖 <b>[Robinhood 上架]</b> %s(%s)\n已可在 Robinhood 交易 · %s", c.Code, c.Name, c.Symbol))
		}
	}
}

// RobinhoodBoard returns the current Robinhood tradable-coin board.
func (s *Store) RobinhoodBoard() RHData {
	s.rhMu.RLock()
	defer s.rhMu.RUnlock()
	coins := make([]RHCoin, len(s.rhBoard))
	copy(coins, s.rhBoard)
	out := RHData{Coins: coins}
	if !s.rhTime.IsZero() {
		out.UpdatedAt = s.rhTime.Format(time.RFC3339)
	}
	return out
}
