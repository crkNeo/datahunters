package cache

import (
	"sort"
	"time"
)

// funding.go: a standalone public 資金費率 board sourced from OKX (to spread load
// off Binance). This is independent of fundMap (the Binance/WS funding used inside
// the scorer) — it only powers the dedicated funding tab.

// FundingRow is one coin's current funding rate (from OKX).
type FundingRow struct {
	Coin   string  `json:"coin"`
	Rate   float64 `json:"rate"`    // per-interval funding rate (fraction, e.g. 0.0001 = 0.01%)
	APR    float64 `json:"apr"`     // annualised % (OKX funds 3×/day → rate × 3 × 365 × 100)
	NextMs int64   `json:"next_ms"` // next funding time (unix ms)
}

// FundingData is the funding-tab payload.
type FundingData struct {
	Rows      []FundingRow `json:"rows"`
	UpdatedAt string       `json:"updated_at"`
}

// FundingTick pulls each tracked coin's funding rate from OKX and caches a board
// sorted most-positive first. OKX has no bulk funding endpoint, so it's one call
// per coin — paced, and refreshed slowly (funding moves slowly). Call on a ticker.
func (s *Store) FundingTick() {
	rows := make([]FundingRow, 0, len(s.coins))
	for _, coin := range s.coins {
		rate, next, err := s.ex.OKXFundingInfo(coin + "-USDT-SWAP")
		if err != nil {
			continue
		}
		rows = append(rows, FundingRow{Coin: coin, Rate: rate, APR: round2(rate * 3 * 365 * 100), NextMs: next})
		time.Sleep(40 * time.Millisecond) // pace OKX public calls
	}
	if len(rows) == 0 {
		s.apiFail("OKX 資金費率", "全部抓取失敗")
		return
	}
	s.apiOK("OKX 資金費率")
	sort.Slice(rows, func(i, j int) bool { return rows[i].Rate > rows[j].Rate })
	s.fundBoardMu.Lock()
	s.fundBoard = rows
	s.fundBoardTime = time.Now()
	s.fundBoardMu.Unlock()
}

// FundingBoard returns the cached OKX funding board (most-positive first).
func (s *Store) FundingBoard() FundingData {
	s.fundBoardMu.RLock()
	defer s.fundBoardMu.RUnlock()
	rows := make([]FundingRow, len(s.fundBoard))
	copy(rows, s.fundBoard)
	out := FundingData{Rows: rows}
	if !s.fundBoardTime.IsZero() {
		out.UpdatedAt = s.fundBoardTime.Format(time.RFC3339)
	}
	return out
}
