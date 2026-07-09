package cache

import (
	"time"

	"datahunter/internal/unlock"
)

// unlock.go: the public 代幣解鎖 board. Pulls DefiLlama's free emission schedules
// for a curated set of tokens and summarises each one's upcoming unlock (next
// 7d/30d as a share of circulating supply + the biggest single unlock day in the
// next 30d). Schedules are static, so this refreshes on a slow ticker.

// UnlockData is the 代幣解鎖 tab payload.
type UnlockData struct {
	Rows      []unlock.Row `json:"rows"`
	UpdatedAt string       `json:"updated_at"`
}

// UnlockTick refreshes the token-unlock board from DefiLlama + CoinGecko.
func (s *Store) UnlockTick() {
	if s.unlockW == nil {
		return
	}
	rows, ok := s.unlockW.Fetch()
	if !ok {
		s.apiFail("代幣解鎖(DefiLlama)", "全部排程抓取失敗")
		return
	}
	s.apiOK("代幣解鎖(DefiLlama)")
	s.unlockMu.Lock()
	s.unlockBoard = rows
	s.unlockTime = time.Now()
	s.unlockMu.Unlock()
}

// Unlocks returns the cached token-unlock board (biggest upcoming 30d unlock first).
func (s *Store) Unlocks() UnlockData {
	s.unlockMu.RLock()
	defer s.unlockMu.RUnlock()
	rows := make([]unlock.Row, len(s.unlockBoard))
	copy(rows, s.unlockBoard)
	out := UnlockData{Rows: rows}
	if !s.unlockTime.IsZero() {
		out.UpdatedAt = s.unlockTime.Format(time.RFC3339)
	}
	return out
}
