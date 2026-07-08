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
	"datahunter/internal/gdelt"
	"datahunter/internal/notify"
	"datahunter/internal/push"
	"datahunter/internal/upbit"
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
	feed    *exchange.WSFeed // live WS prices/funding/klines (REST fallback) — avoids 418 bans
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

	paperMu        sync.Mutex // guards the paper-trading books
	paperMain        *paperBook // disciplined: high bar, fresh-cross only
	paperGamble      *paperBook // loose: low bar, chases already-elevated coins
	paperGambleHedge *paperBook // admin-only A/B: gamble + break-even hedge (推播僅管理員)
	paperEMA         *paperBook // standalone: 1h EMA5/20 cross + 15m EMA200 side (long+short)

	emaMu   sync.Mutex          // guards the multi-timeframe EMA cache
	emaMap  map[string]emaState // coin -> latest closed-bar EMA read
	emaPrev map[string]emaReady // coin -> last OBSERVED readiness (for live transition detection)
	emaHour int64               // UTC hour bucket last evaluated (1 eval per hourly close)

	emaUniMu    sync.Mutex // guards the EMA-strategy coin universe
	emaUniverse []string   // top-N-by-volume coins the EMA strategy scans (set in Refresh)

	logMu     sync.Mutex // guards the score-cross log
	scoreLog  []ScoreEvent
	prevScore map[string]int
	logSeeded bool

	riskMu      sync.Mutex // guards the US/macro risk-backdrop cache
	riskData    RiskData
	riskTime    time.Time
	calRaw      []MacroEvent // cached high-impact US calendar (refetched ~30 min)
	calTime     time.Time
	lastPushKey string          // dedupe Telegram push-warning alerts
	sentEvents  map[string]bool // dedupe high-impact event "30 min before" alerts

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
	oiCache     *ttlCache // OI-hist + long/short: 10-min TTL — /futures/data/* has its own ~1000req/5min IP cap
	klCache     *ttlCache // 1h klines for detail/radar: cached 8 min (futures WS unusable on this net)

	db           *DB              // optional MySQL persistence (nil = disabled)
	notifier     *notify.Telegram // outbound alerts (no-op unless configured)
	alertSignals bool             // push ±20 signal-cross alerts (ALERT_SIGNAL_CROSS=1)

	upbitW            *upbit.Watcher // Upbit announcement watcher → Telegram
	upbitListingsOnly bool           // only push listing/거래지원 notices (UPBIT_LISTINGS_ONLY=1)
	upbitMu           sync.RWMutex   // guards the on-page announcement board + translation cache
	upbitBoard        []UpbitNotice  // recent notices (newest first), titles translated to zh-TW
	upbitTrans        map[int]string // notice id → translated title (so we translate each title once)

	srMu    sync.Mutex         // guards the support/resistance monitor (VIP)
	srInfo  map[string]SRLevel // coin → current support/resistance read
	srState map[string]string  // coin → last emitted breach ("" | down | up) for alert dedupe
	srBar   int64              // last processed closed-bar Ts (per-bar throttle)

	gdeltW      *gdelt.Watcher  // GDELT market-news watcher (free, no key)
	gdeltMu     sync.RWMutex    // guards the news feed + dedupe set
	gdeltFeed   []NewsItem      // recent market-moving headlines (newest first), titles zh-TW
	gdeltSeen   map[string]bool // seen article URLs (dedupe; bounded)
	gdeltSeeded bool            // first tick only seeds (no push burst of history on boot)
	etfSeen     map[string]string // asset → last reported ETF-flow date (dedupe: once/day)

	pushMgr *push.Manager // Web Push (VAPID) sender

	trader *bitunixTrader // optional: mirror strategy opens to a real Bitunix account (admin, Phase 1)
}

func NewStore(coins []string) *Store {
	s := &Store{
		data:              map[string]Snapshot{},
		details:           map[string]CoinDetail{},
		ex:                exchange.NewClient(),
		coins:             coins,
		paperMain:         newBook("main", 55, true, 4*time.Hour, 0),         // disciplined, fixed TP/SL
		paperGamble:       newBook("gamble", 50, false, 1*time.Hour, 0),      // gamble, fixed TP/SL (門檻 50:實盤數據顯示 45–49 桶淨虧)
		paperGambleHedge:  newBook("gamblehedge", 50, false, 1*time.Hour, 0), // admin A/B: gamble + 保本停損
		paperEMA:          newBook("emaonly", 0, false, 0, 0),               // standalone EMA cross (no time cooldown; signal-hour dedup)
		prevScore:         map[string]int{},
		sentEvents:        map[string]bool{},
		liqSeen:           map[string]bool{},
		notifier:          notify.NewTelegram(),
		alertSignals:      os.Getenv("ALERT_SIGNAL_CROSS") == "1", // default off
		upbitW:            upbit.NewWatcher(),
		upbitListingsOnly: os.Getenv("UPBIT_LISTINGS_ONLY") == "1",
		upbitTrans:        map[int]string{},
		srInfo:            map[string]SRLevel{},
		srState:           map[string]string{},
		gdeltW:            gdelt.NewWatcher(),
		gdeltSeen:         map[string]bool{},
		etfSeen:           map[string]string{},
		homeCache:         newTTLCache(15 * time.Second),
		detailCache:       newTTLCache(30 * time.Second),
		klineCache:        newTTLCache(30 * time.Second),
		oiCache:           newTTLCache(10 * time.Minute),
		klCache:           newTTLCache(8 * time.Minute),
	}
	// live WebSocket feed: prices/funding/klines over one connection instead of
	// per-coin REST polling (the cause of recurring 418 bans). REST stays as seed
	// + fallback; OI/long-short have no WS stream and use oiCache (low-freq REST).
	s.feed = exchange.NewWSFeed(s.ex, coins, 260)
	s.feed.Start()
	s.trader = newBitunixTrader() // nil unless BITUNIX_AUTOTRADE=1 + keys set
	// NY session (12-18 UTC) now allowed for all books (user observed losses
	// weren't NY-concentrated; skipNY left at its default false).
	s.paperGambleHedge.adminOnly = true // admin-only tab + admin-only push
	s.paperGambleHedge.maxSLPct = 12    // FILTER@12%: skip SL>12% entries (回測最高報酬 +56%)
	if s.notifier.Enabled() {
		log.Printf("telegram alerts: enabled")
		go s.notifier.Send("✅ <b>datahunter 已啟動</b> · Telegram 通知已連線")
	}
	if db, err := openDB(mysqlDSN()); err != nil {
		log.Printf("mysql persistence disabled: %v", err)
	} else {
		s.db = db
		s.pushMgr = push.New(s) // VAPID keypair (persisted in site_config)
		s.scoreLog = db.loadScoreEvents(500)
		s.paperMain.trades = db.loadTrades("main")
		s.paperGamble.trades = db.loadTrades("gamble")
		s.paperGambleHedge.trades = db.loadTrades("gamblehedge")
		s.paperEMA.trades = db.loadTrades("emaonly")
		log.Printf("mysql loaded: %d score events, main=%d gamble=%d gamblehedge=%d emaonly=%d trades",
			len(s.scoreLog), len(s.paperMain.trades), len(s.paperGamble.trades),
			len(s.paperGambleHedge.trades), len(s.paperEMA.trades))
	}
	return s
}

// emaTopN is how many coins (by 24h quote volume) the standalone EMA strategy
// scans — the same broad universe as the momentum radar, capped so per-hour REST
// stays trivial on this network.
const emaTopN = 80

// setEMAUniverse picks the top-N coins by 24h quote volume (excluding
// dollar-stablecoin perps like USDCUSDT, which never trend) as the EMA
// strategy's scan universe. Called from Refresh, which already has the
// all-tickers snapshot.
func (s *Store) setEMAUniverse(tickers []exchange.MarketTicker, n int) {
	type cv struct {
		coin string
		vol  float64
	}
	list := make([]cv, 0, len(tickers))
	for _, t := range tickers {
		coin := coinOf(t.Symbol)
		if stableLike[coin] || t.QuoteVol <= 0 {
			continue
		}
		list = append(list, cv{coin, t.QuoteVol})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].vol > list[j].vol })
	if len(list) > n {
		list = list[:n]
	}
	coins := make([]string, len(list))
	for i, c := range list {
		coins[i] = c.coin
	}
	s.emaUniMu.Lock()
	s.emaUniverse = coins
	s.emaUniMu.Unlock()
}

// emaCoins returns the EMA strategy's scan universe (top-N by volume), falling
// back to the configured coins until the first Refresh populates it.
func (s *Store) emaCoins() []string {
	s.emaUniMu.Lock()
	u := s.emaUniverse
	s.emaUniMu.Unlock()
	if len(u) > 0 {
		return u
	}
	return s.coins
}

// mysqlDSN builds the go-sql-driver DSN. Set MYSQL_DSN directly, or the pieces
// DB_HOST / DB_PORT / DB_USER / DB_PASS / DB_NAME (sensible localhost defaults).
func mysqlDSN() string {
	if v := os.Getenv("MYSQL_DSN"); v != "" {
		return v
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	host := get("DB_HOST", "127.0.0.1")
	port := get("DB_PORT", "3306")
	user := get("DB_USER", "root")
	pass := os.Getenv("DB_PASS")
	name := get("DB_NAME", "datahunter")
	// epochs are stored as BIGINT so parseTime is unnecessary; force UTC + utf8mb4.
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=false&loc=UTC", user, pass, host, port, name)
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
	go s.notifier.Send(fmt.Sprintf("📊 <b>訊號穿越</b> %s %s 評分 %+d\n現價 $%s · %s",
		coin, dir, snap.Score, fmtPx(price), tags))
}

// refreshFunding refreshes the all-coins funding map — from the WS feed if it's
// healthy (no REST at all), else one REST premiumIndex call as fallback.
func (s *Store) refreshFunding() {
	if s.feed != nil && s.feed.Healthy() {
		if m := s.feed.FundingMap(); len(m) > 0 {
			s.fundMu.Lock()
			s.fundMap = m
			s.fundMu.Unlock()
			return
		}
	}
	m, err := s.ex.BinanceAllFunding()
	if err != nil || len(m) == 0 {
		return
	}
	s.fundMu.Lock()
	s.fundMap = m
	s.fundMu.Unlock()
}

// livePrices returns a coin->price map from the WS feed if healthy (no REST),
// else one cheap all-prices REST call (weight 2, vs 40 for full 24h tickers).
func (s *Store) livePrices() map[string]float64 {
	if s.feed != nil && s.feed.Healthy() {
		px := make(map[string]float64, len(s.coins))
		for _, coin := range s.coins {
			if p, ok := s.feed.Price(coin); ok {
				px[coin] = p
			}
		}
		if len(px) > 0 {
			return px
		}
	}
	prices, err := s.ex.BinanceAllPrices()
	if err != nil {
		return nil
	}
	px := make(map[string]float64, len(prices))
	for sym, p := range prices {
		px[coinOf(sym)] = p
	}
	return px
}

// klines1h returns 1h candles for a coin, preferring the live WS feed (last bar
// = current forming bar, matching REST shape) and falling back to REST only when
// the feed hasn't got enough history yet. This removes the per-coin kline REST
// fan-out that caused 418 bans.
func (s *Store) klines1h(coin string, limit int) []exchange.Candle {
	if s.feed != nil && s.feed.Healthy() { // only trust the feed while it's LIVE
		if kl := s.feed.KlinesLive(coin); len(kl) >= limit {
			return kl[len(kl)-limit:]
		}
	}
	kl, _ := s.ex.BinanceKlines(coin+"USDT", "1h", limit)
	return kl
}

// klines1hCached is like klines1h but the REST fallback is cached ~4 min and
// shared across the HIGH-frequency callers (detail every ~2 min, radar every
// ~3 min), so repeated per-coin kline fetching can't accumulate into a 418 ban.
// (Futures WS is unreachable on this network, so the fallback is the live path.)
// refreshEMA keeps the fresh klines1h — it runs only once per hour.
func (s *Store) klines1hCached(coin string, limit int) []exchange.Candle {
	if s.feed != nil && s.feed.Healthy() { // only trust the feed while it's LIVE
		if kl := s.feed.KlinesLive(coin); len(kl) >= limit {
			return kl[len(kl)-limit:]
		}
	}
	v, err := s.klCache.get(coin, func() (any, error) {
		return s.ex.BinanceKlines(coin+"USDT", "1h", 120)
	})
	if err != nil || v == nil {
		return nil
	}
	kl, _ := v.([]exchange.Candle)
	if len(kl) >= limit {
		return kl[len(kl)-limit:]
	}
	return kl
}

// oiHist1h returns recent 1h open-interest points for a coin (no WS stream
// exists), cached 5 min so 36 coins don't hammer REST every cycle.
func (s *Store) oiHist1h(coin string, limit int) []exchange.OIPoint {
	v, err := s.oiCache.get("oi|"+coin, func() (any, error) {
		return s.ex.BinanceOIHist(coin+"USDT", "1h", 15)
	})
	if err != nil || v == nil {
		return nil
	}
	pts, _ := v.([]exchange.OIPoint)
	if len(pts) > limit {
		return pts[len(pts)-limit:]
	}
	return pts
}

// longShortCached returns the latest long/short account ratio, cached 5 min
// (Binance futures/data endpoint, no WS stream).
func (s *Store) longShortCached(coin string) exchange.LongShort {
	v, err := s.oiCache.get("ls|"+coin, func() (any, error) {
		return s.ex.BinanceLongShort(coin+"USDT", "5m")
	})
	if err != nil || v == nil {
		return exchange.LongShort{}
	}
	ls, _ := v.(exchange.LongShort)
	return ls
}

// UpbitNotice is one Upbit announcement for the on-page board, with its Korean
// title translated to Traditional Chinese (TitleZH).
type UpbitNotice struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`    // original Korean title
	TitleZH  string `json:"title_zh"` // Traditional Chinese translation
	Category string `json:"category"`
	ListedAt string `json:"listed_at"`
	URL      string `json:"url"`
	Listing  bool   `json:"listing"` // trading-support / new-listing notice
}

// UpbitTick polls Upbit announcements: it pushes any newly posted ones to
// Telegram/Web Push, and rebuilds the on-page board (titles translated to
// Traditional Chinese). The push side no-ops when no channel is configured.
func (s *Store) UpbitTick() {
	if s.upbitW == nil {
		return
	}
	fresh, all, err := s.upbitW.Poll()
	if err != nil {
		return
	}
	// push newly posted notices (Telegram + Web Push); seeding tick has none.
	for _, n := range fresh {
		if s.upbitListingsOnly && !n.IsListing() {
			continue
		}
		tag := "Upbit 公告"
		if n.IsListing() {
			tag = "🚀 Upbit 上架"
		}
		// Web Push opens our own Upbit board tab (not upbit.com); the Telegram
		// message still links out to the real notice.
		s.PushSend(tag, n.Title, "/?tab=upbit")
		go s.notifier.Send(n.TelegramText())
	}
	s.updateUpbitBoard(all)
}

// updateUpbitBoard translates each notice title (ko→zh-TW, cached by id so every
// title is translated only once) and publishes the board newest-first.
func (s *Store) updateUpbitBoard(notices []upbit.Notice) {
	board := make([]UpbitNotice, 0, len(notices))
	for _, n := range notices {
		s.upbitMu.RLock()
		zh, ok := s.upbitTrans[n.ID]
		s.upbitMu.RUnlock()
		if !ok {
			zh = s.upbitW.TranslateKo(n.Title)
			s.upbitMu.Lock()
			s.upbitTrans[n.ID] = zh
			s.upbitMu.Unlock()
		}
		board = append(board, UpbitNotice{
			ID: n.ID, Title: n.Title, TitleZH: zh, Category: n.Category,
			ListedAt: n.ListedAt, URL: n.URL(), Listing: n.IsListing(),
		})
	}
	s.upbitMu.Lock()
	s.upbitBoard = board
	if len(s.upbitTrans) > 200 { // prune stale translations so the cache can't grow unbounded
		keep := make(map[int]string, len(board))
		for _, n := range board {
			keep[n.ID] = s.upbitTrans[n.ID]
		}
		s.upbitTrans = keep
	}
	s.upbitMu.Unlock()
}

// UpbitBoard returns the recent Upbit announcements (newest first) with titles
// translated to Traditional Chinese, for the public board.
func (s *Store) UpbitBoard() []UpbitNotice {
	s.upbitMu.RLock()
	defer s.upbitMu.RUnlock()
	out := make([]UpbitNotice, len(s.upbitBoard))
	copy(out, s.upbitBoard)
	return out
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

// Authenticate verifies the password and returns (role, status, ok). ok means
// the password matched; the caller must additionally require status=="active"
// to allow login (so it can message "審核中" / "已停用" distinctly).
func (s *Store) Authenticate(username, password string) (role, status string, ok bool) {
	if s.db == nil {
		return "", "", false
	}
	h, r, st, found := s.db.userAuth(username)
	if !found || !auth.CheckPassword(h, password) {
		return "", "", false
	}
	return r, st, true
}

// validAcct: 4–16 chars, ASCII letters/digits only.
func validAcct(s string) bool {
	if len(s) < 4 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// validPassword: 4–16 ASCII chars with at least one upper, lower, digit and special.
func validPassword(p string) bool {
	if len(p) < 4 || len(p) > 16 {
		return false
	}
	var up, lo, dig, sp bool
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z':
			up = true
		case r >= 'a' && r <= 'z':
			lo = true
		case r >= '0' && r <= '9':
			dig = true
		case r >= 0x21 && r <= 0x7e: // printable ASCII, non-alnum = special
			sp = true
		default:
			return false // space / non-ASCII not allowed
		}
	}
	return up && lo && dig && sp
}

// PrecheckRegister runs the account/password/duplicate validation WITHOUT
// creating anything — the register handler calls it before saving the proof
// image, so an invalid registration can never leave an orphan file on disk.
func (s *Store) PrecheckRegister(username, password string) error {
	if s.db == nil {
		return errors.New("persistence disabled")
	}
	if !validAcct(username) {
		return errors.New("帳號需 4–16 碼英文或數字")
	}
	if !validPassword(password) {
		return errors.New("密碼需 4–16 碼,且含大寫、小寫、數字與特殊符號")
	}
	if s.db.userExists(username) {
		return errors.New("帳號已存在")
	}
	return nil
}

// Register creates a self-service account in "pending" review status (member
// role). proof is the stored asset-proof image path; exchange goes in notes.
func (s *Store) Register(username, password, uid, exchange, proof string) error {
	if err := s.PrecheckRegister(username, password); err != nil {
		return err
	}
	h, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	s.db.registerUser(username, h, uid, exchange, proof)
	return nil
}

// LiveRoleStatus returns a user's CURRENT role+status from the DB (for the
// per-request gate: bans and role changes take effect immediately).
func (s *Store) LiveRoleStatus(username string) (role, status string, ok bool) {
	if s.db == nil {
		return "", "", false
	}
	return s.db.userRoleStatus(username)
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

// DeleteUser removes an account (and its push subscriptions).
func (s *Store) DeleteUser(username string) {
	if s.db != nil {
		s.db.deleteUser(username)
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
	if len(tickers) > 0 {
		s.setEMAUniverse(tickers, emaTopN) // top-N by volume → EMA strategy universe
	}

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
