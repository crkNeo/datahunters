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

	radarMu   sync.RWMutex // guards the breakout-radar cache
	radar     RadarData
	radarTime time.Time

	symMu    sync.Mutex // guards the coin-type (crypto vs equity) cache
	symTypes map[string]string
	symTime  time.Time

	paperMu     sync.Mutex // guards both paper-trading books
	paperMain   *paperBook // disciplined: high bar, fresh-cross only
	paperGamble *paperBook // loose: low bar, chases already-elevated coins

	logMu     sync.Mutex // guards the score-cross log
	scoreLog  []ScoreEvent
	prevScore map[string]int
	logSeeded bool

	optMu   sync.Mutex // guards the BTC/ETH options dashboard cache
	optData OptionsData
	optTime time.Time
}

func NewStore(coins []string) *Store {
	return &Store{
		data:        map[string]Snapshot{},
		details:     map[string]CoinDetail{},
		ex:          exchange.NewClient(),
		coins:       coins,
		paperMain:   newBook(55, true, 4*time.Hour),  // disciplined
		paperGamble: newBook(45, false, 1*time.Hour), // gamble: low bar, chases
		prevScore:   map[string]int{},
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

	s.logScoreCrosses(nextSnaps, tmap, time.Now())

	s.mu.Lock()
	s.data = nextSnaps
	s.details = nextDetails
	s.updated = time.Now()
	s.mu.Unlock()
}

// ScoreEvent records the moment a coin's directional score crossed into a
// long/short signal (|score| >= 20), so it can be reviewed on the chart later.
type ScoreEvent struct {
	Coin  string    `json:"coin"`
	Score int       `json:"score"`
	Bias  string    `json:"bias"`
	Price float64   `json:"price"`
	Time  time.Time `json:"time"`
}

// logScoreCrosses appends an event whenever a coin's |score| goes from <20 to
// >=20 (a fresh long/short signal). The first refresh only seeds the baseline.
func (s *Store) logScoreCrosses(snaps map[string]Snapshot, tmap map[string]exchange.MarketTicker, now time.Time) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	for coin, snap := range snaps {
		if s.logSeeded && abs(s.prevScore[coin]) < 20 && abs(snap.Score) >= 20 {
			s.scoreLog = append(s.scoreLog, ScoreEvent{
				Coin: coin, Score: snap.Score, Bias: snap.Bias,
				Price: tmap[coin+"USDT"].Price, Time: now,
			})
		}
		s.prevScore[coin] = snap.Score
	}
	s.logSeeded = true
	if len(s.scoreLog) > 300 {
		s.scoreLog = s.scoreLog[len(s.scoreLog)-300:]
	}
}

// ScoreLog returns the recorded signal-cross events, newest first.
func (s *Store) ScoreLog() []ScoreEvent {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	out := make([]ScoreEvent, len(s.scoreLog))
	for i, e := range s.scoreLog {
		out[len(s.scoreLog)-1-i] = e
	}
	return out
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
