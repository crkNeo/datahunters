package cache

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"datahunter/internal/auth"
	"datahunter/internal/exchange"
	"datahunter/internal/notify"
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

	radarMu      sync.RWMutex // guards the breakout-radar cache
	radarCompute sync.Mutex   // singleflight: only one computeRadar at a time
	radar        RadarData
	radarTime    time.Time

	symMu    sync.Mutex // guards the coin-type (crypto vs equity) cache
	symTypes map[string]string
	symTime  time.Time

	paperMu     sync.Mutex // guards the paper-trading books
	paperMain    *paperBook // disciplined: high bar, fresh-cross only
	paperGamble  *paperBook // loose: low bar, chases already-elevated coins
	paperPremium *paperBook // control group: gamble bar + OI/CVD aligned + funding-fuel

	logMu     sync.Mutex // guards the score-cross log
	scoreLog  []ScoreEvent
	prevScore map[string]int
	logSeeded bool

	riskMu      sync.Mutex // guards the US/macro risk-backdrop cache
	riskData    RiskData
	riskTime    time.Time
	calRaw      []MacroEvent    // cached high-impact US calendar (refetched ~30 min)
	calTime     time.Time
	lastPushKey string          // dedupe Telegram push-warning alerts
	sentEvents  map[string]bool // dedupe high-impact event "30 min before" alerts

	obMu   sync.Mutex // guards the order-book wall/imbalance cache
	obData OrderBookData
	obTime time.Time

	liqMu     sync.Mutex // guards the liquidation feed
	liqFeed   []LiqRow
	liqSeen   map[string]bool
	liqTime   time.Time
	ctVal     map[string]float64 // OKX contract values (coin -> base units/contract)
	ctValTime time.Time

	fundMu  sync.RWMutex       // guards the all-coins funding-rate cache
	fundMap map[string]float64 // coin -> latest funding rate

	homeCache   *ttlCache // shared cache for per-request endpoints (public scale)
	detailCache *ttlCache
	klineCache  *ttlCache

	db           *DB              // optional SQLite persistence (nil = disabled)
	notifier     *notify.Telegram // outbound alerts (no-op unless configured)
	alertSignals bool             // push ±20 signal-cross alerts (ALERT_SIGNAL_CROSS=1)
}

func NewStore(coins []string) *Store {
	s := &Store{
		data:        map[string]Snapshot{},
		details:     map[string]CoinDetail{},
		ex:          exchange.NewClient(),
		coins:       coins,
		paperMain:    newBook("main", 55, true, 4*time.Hour, 0),     // disciplined, fixed TP/SL
		paperGamble:  newBook("gamble", 45, false, 1*time.Hour, 0),  // gamble, fixed TP/SL
		paperPremium: newBook("premium", 45, false, 1*time.Hour, 0), // control: aligned + fuel
		prevScore:    map[string]int{},
		sentEvents:   map[string]bool{},
		liqSeen:      map[string]bool{},
		notifier:     notify.NewTelegram(),
		alertSignals: os.Getenv("ALERT_SIGNAL_CROSS") == "1", // default off
		homeCache:    newTTLCache(15 * time.Second),
		detailCache:  newTTLCache(30 * time.Second),
		klineCache:   newTTLCache(30 * time.Second),
	}
	// NY session (12-18 UTC) now allowed for all books (user observed losses
	// weren't NY-concentrated; skipNY left at its default false).
	s.paperPremium.requireAlign = true // control group: OI/CVD aligned …
	s.paperPremium.requireFuel = true  // … AND funding-fuel (contrarian)
	if s.notifier.Enabled() {
		log.Printf("telegram alerts: enabled")
		go s.notifier.Send("✅ <b>datahunter 已啟動</b> · Telegram 通知已連線")
	}
	if db, err := openDB(dbPath()); err != nil {
		log.Printf("sqlite persistence disabled: %v", err)
	} else {
		s.db = db
		s.scoreLog = db.loadScoreEvents(500)
		s.paperMain.trades = db.loadTrades("main")
		s.paperGamble.trades = db.loadTrades("gamble")
		s.paperPremium.trades = db.loadTrades("premium")
		log.Printf("sqlite loaded: %d score events, main=%d gamble=%d premium=%d trades",
			len(s.scoreLog), len(s.paperMain.trades), len(s.paperGamble.trades), len(s.paperPremium.trades))
	}
	return s
}

func dbPath() string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	return "datahunter.db"
}

// notifySignalCross pushes a Telegram alert when a coin crosses into a ±20
// signal, tagged with quality (OI contraction + BTC-trend alignment).
func (s *Store) notifySignalCross(coin string, snap Snapshot, price, btcChg float64) {
	if !s.alertSignals || !s.notifier.Enabled() {
		return
	}
	dir := "做多"
	aligned := btcChg >= 0
	if snap.Bias == "short" {
		dir, aligned = "做空", btcChg <= 0
	}
	tags := "OI擴張⚠"
	if snap.OIChg1h < 0 {
		tags = "OI收縮✓"
	}
	if aligned {
		tags += " 順勢✓"
	} else {
		tags += " 逆勢⚠"
	}
	go s.notifier.Send(fmt.Sprintf("📊 <b>訊號穿越</b> %s %s 評分 %+d\n現價 $%.4g · %s",
		coin, dir, snap.Score, price, tags))
}

// refreshFunding pulls the all-coins funding map (one premiumIndex call).
func (s *Store) refreshFunding() {
	m, err := s.ex.BinanceAllFunding()
	if err != nil || len(m) == 0 {
		return
	}
	s.fundMu.Lock()
	s.fundMap = m
	s.fundMu.Unlock()
}

// Funding returns the latest funding rate for a coin (0 if unknown).
func (s *Store) Funding(coin string) float64 {
	s.fundMu.RLock()
	defer s.fundMu.RUnlock()
	return s.fundMap[coin]
}

// ---- accounts (public web build) ----

// SeedAdmin creates the super-admin if it doesn't exist yet (idempotent).
func (s *Store) SeedAdmin(username, password string) {
	if s.db == nil || username == "" || password == "" || s.db.userExists(username) {
		return
	}
	h, err := auth.HashPassword(password)
	if err != nil {
		return
	}
	s.db.upsertUser(username, h, auth.RoleAdmin, "active")
	log.Printf("seeded admin user: %s", username)
}

// Authenticate verifies credentials; returns the role if valid and not banned.
func (s *Store) Authenticate(username, password string) (string, bool) {
	if s.db == nil {
		return "", false
	}
	h, role, status, ok := s.db.userAuth(username)
	if !ok || status == "banned" || !auth.CheckPassword(h, password) {
		return "", false
	}
	return role, true
}

func (s *Store) Users() []User {
	if s.db == nil {
		return nil
	}
	return s.db.listUsers()
}

func (s *Store) CreateUser(username, password, role, status string) error {
	if s.db == nil {
		return errors.New("persistence disabled")
	}
	if username == "" || password == "" {
		return errors.New("username and password required")
	}
	if s.db.userExists(username) {
		return errors.New("user already exists")
	}
	h, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	s.db.upsertUser(username, h, role, status)
	return nil
}

func (s *Store) SetUserRole(username, role, status string) {
	if s.db != nil {
		s.db.setUserRole(username, role, status)
	}
}

// RankRow is one coin on the public Top-10 board (scores only, NO entry/TP/SL).
type RankRow struct {
	Coin     string  `json:"coin"`
	Score    int     `json:"score"`
	Bias     string  `json:"bias"`
	OIChg1h  float64 `json:"oi_chg_1h"`
	CVDRatio float64 `json:"cvd_ratio"`
	Funding  float64 `json:"funding_rate"`
}

// RankingData is the public long/short Top-10 leaderboard.
type RankingData struct {
	Long      []RankRow `json:"long"`
	Short     []RankRow `json:"short"`
	UpdatedAt string    `json:"updated_at"`
}

// Ranking returns the Top-10 longs and shorts by score (no levels — public-safe).
func (s *Store) Ranking() RankingData {
	data, updated := s.All()
	rows := make([]RankRow, 0, len(data))
	for coin, snap := range data {
		rows = append(rows, RankRow{coin, snap.Score, snap.Bias, snap.OIChg1h, snap.CVDRatio, snap.Funding})
	}
	longs := append([]RankRow{}, rows...)
	sort.Slice(longs, func(i, j int) bool { return longs[i].Score > longs[j].Score })
	shorts := append([]RankRow{}, rows...)
	sort.Slice(shorts, func(i, j int) bool { return shorts[i].Score < shorts[j].Score })
	top := func(r []RankRow) []RankRow {
		out := []RankRow{}
		for i := 0; i < len(r) && i < 10; i++ {
			out = append(out, r[i])
		}
		return out
	}
	return RankingData{Long: top(longs), Short: top(shorts), UpdatedAt: updated.Format(time.RFC3339)}
}

// KlinePoint is a slim OHLC bar for the detail-drawer candlestick chart.
type KlinePoint struct {
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
}

// Klines fetches recent OHLC candles for a coin (fresh from Binance fapi).
func (s *Store) Klines(coin, interval string, limit int) ([]KlinePoint, error) {
	key := coin + "|" + interval + "|" + strconv.Itoa(limit)
	v, err := s.klineCache.get(key, func() (any, error) {
		kl, err := s.ex.BinanceKlines(coin+"USDT", interval, limit)
		if err != nil {
			return nil, err
		}
		out := make([]KlinePoint, len(kl))
		for i, c := range kl {
			out[i] = KlinePoint{T: c.Ts, O: c.Open, H: c.High, L: c.Low, C: c.Close}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]KlinePoint), nil
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
			ev := ScoreEvent{
				Coin: coin, Score: snap.Score, Bias: snap.Bias,
				Price: tmap[coin+"USDT"].Price, Time: now,
			}
			s.scoreLog = append(s.scoreLog, ev)
			if s.db != nil {
				s.db.insertScoreEvent(ev)
			}
			s.notifySignalCross(coin, snap, ev.Price, tmap["BTCUSDT"].ChgPct)
		}
		s.prevScore[coin] = snap.Score
	}
	s.logSeeded = true
	if len(s.scoreLog) > 500 {
		s.scoreLog = s.scoreLog[len(s.scoreLog)-500:]
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
