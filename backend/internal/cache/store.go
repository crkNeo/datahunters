package cache

import (
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// Snapshot is the per-coin aggregated row for the board. Its Score/Bias come
// from the SAME detail scorer used by the per-coin card, so the board, the
// recommendations and the detail drawer never disagree.
type Snapshot struct {
	OKXChg   float64 `json:"okx_chg"`
	OIChg1h  float64 `json:"oi_chg_1h"`
	CVDRatio float64 `json:"cvd_ratio"`
	Funding  float64 `json:"funding_rate"`
	Score    int     `json:"score"`
	Bias     string  `json:"bias"`
	Quality  string  `json:"quality"`
}

// Store holds the latest snapshot and full detail for all tracked coins.
type Store struct {
	mu      sync.RWMutex
	data    map[string]Snapshot
	details map[string]CoinDetail
	updated time.Time
	ex      *exchange.Client
	coins   []string

	altMu        sync.Mutex // guards altcoin-season day tracking
	altDate      string     // UTC date of altToday
	altToday     int
	altYesterday int
}

func NewStore(coins []string) *Store {
	return &Store{
		data:    map[string]Snapshot{},
		details: map[string]CoinDetail{},
		ex:      exchange.NewClient(),
		coins:   coins,
	}
}

func (s *Store) All() (map[string]Snapshot, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Snapshot, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out, s.updated
}

// Refresh pulls fresh data for every coin and scores it with the detail scorer.
// One all-tickers call supplies 24h change (and BTC's, for relative strength)
// for every coin, so per-coin work stays the same as before.
func (s *Store) Refresh() {
	tickers, _ := s.ex.BinanceAllTickers()
	tmap := make(map[string]exchange.MarketTicker, len(tickers))
	for _, t := range tickers {
		tmap[t.Symbol] = t
	}
	btcChg := tmap["BTCUSDT"].ChgPct

	nextSnaps := make(map[string]Snapshot, len(s.coins))
	nextDetails := make(map[string]CoinDetail, len(s.coins))
	for _, coin := range s.coins {
		detail, snap := s.computeDetailCore(coin, tmap[coin+"USDT"].ChgPct, btcChg)
		nextSnaps[coin] = snap
		nextDetails[coin] = detail
		time.Sleep(120 * time.Millisecond) // be polite to public endpoints
	}

	// fill related peers now that every coin is scored
	for coin, d := range nextDetails {
		d.Related = relatedFrom(coin, nextSnaps)
		nextDetails[coin] = d
	}

	s.mu.Lock()
	s.data = nextSnaps
	s.details = nextDetails
	s.updated = time.Now()
	s.mu.Unlock()
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}
