package cache

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"datahunter/internal/auth"
	"datahunter/internal/exchange"
	"datahunter/internal/gdelt"
	"datahunter/internal/marketai"
	"datahunter/internal/notify"
	"datahunter/internal/push"
	"datahunter/internal/robinhood"
	"datahunter/internal/unlock"
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

	paperMu       sync.Mutex // guards the paper-trading books
	paperMain     *paperBook // disciplined: high bar, fresh-cross only
	paperGamble   *paperBook // loose: low bar, chases already-elevated coins; 分批止盈 + FILTER@12% + 逾時6h
	paperEMA      *paperBook // standalone: 1h EMA5/20 cross + 15m EMA200 side (long+short)

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

	srmMu    sync.Mutex // guards the 錘子/流星 插針 monitor (support_mtf.go)
	srmBar1h int64      // last processed closed 1h bar Ts
	srmBar4h int64      // last processed closed 4h bar Ts

	gdeltW      *gdelt.Watcher    // GDELT market-news watcher (free, no key)
	gdeltMu     sync.RWMutex      // guards the news feed + dedupe set
	gdeltFeed   []NewsItem        // recent market-moving headlines (newest first), titles zh-TW
	gdeltSeen   map[string]bool   // seen article URLs (dedupe; bounded)
	gdeltSeeded bool              // first tick only seeds (no push burst of history on boot)
	etfSeen     map[string]string // asset → last reported ETF-flow date (dedupe: once/day)

	convMu       sync.Mutex    // guards the 冥王星 (動態ATR均線收斂 4H) strategy (VIP, convergence.go)
	convTrades   []*PaperTrade // simulated convergence trades (long+short)
	conv4hBucket int64         // last processed 4H wall-clock bucket
	convSeeded   bool          // first tick only sets the baseline — no boot-time backfill of entries

	meanRevBook  *microBook // 火星:乖離回歸 1h (admin, microrev.go)
	bollEMABook  *microBook // 布林EMA 4H 突破蓄勢 多空 (admin, microrev.go)
	ema2155Books []*microBook // 2155多:EMA21/55 金叉 只做多,死叉即時止損;1h/4h/1d 三週期同頁 (microrev.go)
	surge        *surgeEngine // 爆量脈搏斥候:全700檔相對爆量偵測 (surge.go)
	pulsarBook   *microBook   // 脈衝星:建在爆量熱名單上的觀察策略 (microrev.go)
	pulsarV2Book *microBook   // 脈衝星v2:脈衝星 + OI/CVD 品質閘門 (microrev.go)
	pulsarV3Book *microBook   // 脈衝星v3:ATR 自適應止損 + 追尾 runner + 大盤閘門 (microrev.go)
	pulsarV4Book *microBook   // 脈衝星v4:= v1,但關閉 4h 逾時 (microrev.go)
	pulsarV5Book *microBook   // 脈衝星v5:= v1,但固定百分比止盈 5%/10%/15% (microrev.go)
	pulsarV3sBook *microBook  // 反脈衝星v3:脈衝星v3 的做空鏡像(爆量下殺初段)(microrev.go)
	smcBooks     []*microBook // 訂單塊:LuxAlgo SMC 訂單塊拉斐波,回撤 0.142-0.382 + 頭槌/射擊星進場,四段套保;15m/1h/4h 三週期同頁 (orderblock.go)

	rlMu    sync.Mutex      // guards external-API health tracking (apihealth.go)
	rlFails map[string]int  // source → consecutive failure count
	rlDown  map[string]bool // source → currently reported down (alert dedupe)

	fundBoardMu   sync.RWMutex // guards the OKX funding board (public tab)
	fundBoard     []FundingRow
	fundBoardTime time.Time

	unlockW     *unlock.Watcher // 代幣解鎖 board (DefiLlama emissions, free, no key)
	unlockMu    sync.RWMutex    // guards the token-unlock board (public tab)
	unlockBoard []unlock.Row
	unlockTime  time.Time

	rhW     *robinhood.Watcher // Robinhood 上架 watcher (currency-pair diff, no key)
	rhMu    sync.RWMutex       // guards the Robinhood board
	rhBoard []RHCoin
	rhNew   map[string]int64 // code → first-seen ms (recent-listing badge)
	rhTime  time.Time

	maiW       *marketai.Client // 大盤 AI 分析 (Groq;免費、可從 VPS 用)
	maiMu      sync.RWMutex     // guards the market-AI commentary
	maiText    string           // latest full zh-TW analysis
	maiSummary string           // first line (push title / one-liner)
	maiTime    time.Time
	maiBucket  int64     // last SUCCESSFUL hour bucket (once-per-hour gate)
	maiRetryAt time.Time // after a failure, don't retry before this (5-min backoff)
	maiSeeded  bool      // first analysis shows but doesn't push

	sectorMu     sync.RWMutex       // guards the 板塊強弱 board (hourly)
	sectorBoard  []SectorRow        // ranked sectors (strongest first)
	sectorPrev   map[string]float64 // sector → last-hour VsBTC (for the rotation Δ)
	sectorBtcChg float64
	sectorTime   time.Time
	sectorBucket int64 // last processed hour bucket
	sectorSeeded bool  // first tick seeds the baseline (no rotation push)

	stratMu  sync.RWMutex        // guards the per-strategy on/off switches + config (admin)
	stratOff map[string]bool     // strategy name → disabled (won't open new trades)
	stratCfg map[string]StratCfg // strategy name → admin config override (empty = code default)

	tabMu    sync.RWMutex      // guards the tab→role/kind tables (admin, tabperm.go)
	tabPerms map[string]string // tab → 最低角色 override(空 = 用 tabMeta 預設)
	tabKinds map[string]string // tab → 類型 info/signal override(空 = 用預設)

	pushMgr *push.Manager // Web Push (VAPID) sender

	trader *bitunixTrader // optional: mirror strategy opens to a real Bitunix account (admin, Phase 1)
}

func NewStore(coins []string) *Store {
	s := &Store{
		data:              map[string]Snapshot{},
		details:           map[string]CoinDetail{},
		ex:                exchange.NewClient(),
		coins:             coins,
		surge:             newSurgeEngine(),
		paperMain:         newBook("main", 55, true, 4*time.Hour, 0),    // disciplined, fixed TP/SL
		paperGamble:       newBook("gamble", 50, false, 1*time.Hour, 0), // gamble (門檻 50:實盤數據顯示 45–49 桶淨虧;逾時6h)
		paperEMA:          newBook("emaonly", 0, false, 0, 0),           // standalone EMA cross (no time cooldown; signal-hour dedup)
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
		unlockW:           unlock.NewWatcher(),
		rhW:               robinhood.NewWatcher(),
		rhNew:             map[string]int64{},
		maiW:              marketai.NewClient(),
		sectorPrev:        map[string]float64{},
		stratOff:          map[string]bool{},
		stratCfg:          map[string]StratCfg{},
		tabPerms:          map[string]string{},
		gdeltSeen:         map[string]bool{},
		etfSeen:           map[string]string{},
		rlFails:           map[string]int{},
		rlDown:            map[string]bool{},
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
	// 分批止盈 (TP1/TP2 = 進場→TP3 的 40%/70%) 套用到 radar/EMA 書。gamble 另加 FILTER@12%。
	s.paperGamble.plan = tpMomentum
	s.paperGamble.maxSLPct = 12  // FILTER@12%: skip SL>12% entries (回測最高報酬 +56%)
	s.paperGamble.expiry = 6 * time.Hour // 超新星改用短逾時(原超新星v2 的邏輯):24h→6h,動能不快出現就是死單
	s.paperMain.plan = tpMomentum
	s.paperEMA.plan = tpMomentum
	// admin A/B observation books: same 超新星 entries + 分批止盈, each isolating ONE
	// candidate fix so it can be compared against the base 超新星.
	// 火星:乖離回歸 1h (microrev.go)
	s.meanRevBook = &microBook{name: "meanrev", tf: "1h", barSec: 3600, klimit: 300, minBars: 210, expiry: 24, cooldown: 4, keep: 500, plan: tpMeanRevFront, maxSLPct: 10, signal: meanRevSignal}
	// 2155多:EMA21/55 金叉進場(只做多)、SL=近20根低點、TP 1:2/1:3/1:4 分批、死叉即時出場。
	// expiry=0 → 無時間出場;分批位置(50%/75% → 2R/3R)與比例由 strat 設定驅動。
	// 三個週期(1h/4h/1d)同頁呈現、共用一個開關與設定(stratKey=ema2155),各自獨立持倉、
	// 不做同幣互斥(同一幣可同時在 1h 與 4h 各開一單,以 TF 欄位分類)。
	s.ema2155Books = []*microBook{
		{name: "ema2155", tf: "1h", barSec: 3600, klimit: 300, minBars: 80, expiry: 0, cooldown: 4, keep: 500, plan: tpEMA2155, stratKey: "ema2155", tfTag: true, signal: ema2155Signal, exitSignal: ema2155DeathCross, tpLevels: ema2155TPLevels, gate: s.cryptoOnly},
		{name: "ema2155_4h", tf: "4h", barSec: 14400, klimit: 300, minBars: 80, expiry: 0, cooldown: 4, keep: 500, plan: tpEMA2155, stratKey: "ema2155", tfTag: true, signal: ema2155Signal, exitSignal: ema2155DeathCross, tpLevels: ema2155TPLevels, gate: s.cryptoOnly},
		{name: "ema2155_1d", tf: "1d", barSec: 86400, klimit: 300, minBars: 80, expiry: 0, cooldown: 4, keep: 500, plan: tpEMA2155, stratKey: "ema2155", tfTag: true, signal: ema2155Signal, exitSignal: ema2155DeathCross, tpLevels: ema2155TPLevels, gate: s.cryptoOnly},
	}
	// 脈衝星:建在爆量熱名單(surge.go)上的觀察策略。宇宙 = surgeHotCoins(可含 top-80 以外),
	// 15m 動能確認進場、近10根 swing-low 止損、1:4 分批(50/75 → 1:2/1:3)、12h 逾時。
	s.pulsarBook = &microBook{name: "pulsar", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, cooldown: 16, keep: 500, plan: tpMomentum, universe: s.surgeHotCoins, signal: surgeSignal}
	// 脈衝星v2:與脈衝星完全相同,但多一道 OI/CVD 品質閘門(只放行 OI 擴張 + 買盤主導的爆量)。
	// 兩本並存是為了 A/B 對照:閘門有沒有把品質拉起來。
	s.pulsarV2Book = &microBook{name: "pulsarv2", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, cooldown: 16, keep: 500, plan: tpMomentum, universe: s.surgeHotCoins, signal: surgeSignal, gate: s.oiCvdGate}
	// 脈衝星v3:ATR 自適應進出場 + 追尾 runner。主倉 4h 逾時(expiry 16);runner(Legs≥2)改用
	// runnerExpiry 96 根 = 24h,突破 4h 讓小倉測後續跑動。plan=tpPulsarV3(1R/2R + 追尾)。
	// gate 先關掉(不加 BTC 大盤閘門);要再開回來就把 gate: s.btcRegimeGate 加回去。
	s.pulsarV3Book = &microBook{name: "pulsarv3", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, runnerExpiry: 96, cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeHotCoins, signal: surgeV3Signal}
	// 脈衝星v4:與 v1 完全相同,唯一差別 expiry=0 → 關閉 4h 逾時(只靠 TP/SL 出場)。
	s.pulsarV4Book = &microBook{name: "pulsarv4", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 0, cooldown: 16, keep: 500, plan: tpMomentum, universe: s.surgeHotCoins, signal: surgeSignal}
	// 脈衝星v5:= v1(含 4h 逾時),但止盈改成固定百分比 TP1=+5%/TP2=+10%/最終=+15%(tpLevels 覆蓋)。
	s.pulsarV5Book = &microBook{name: "pulsarv5", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, cooldown: 16, keep: 500, plan: tpMomentum, universe: s.surgeHotCoins, signal: surgeSignal, tpLevels: pulsarPctTPLevels}
	// 反脈衝星v3:脈衝星v3 的做空鏡像。結構完全對稱(ATR 自適應 + 追尾 runner + 4h/24h 逾時 + 4h 冷卻),
	// 只是做空。宇宙同爆量熱名單(崩跌也爆量)。
	s.pulsarV3sBook = &microBook{name: "pulsarv3s", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, runnerExpiry: 96, cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeHotCoins, signal: antiSurgeV3Signal}
	// 布林EMA:4H 突破蓄勢。單段止盈(1:3 RR)、無分批;beAt=0.3 只發「已達保本位」通知,不動止損。
	s.bollEMABook = &microBook{name: "bollema", tf: "4h", barSec: 14400, klimit: 300, minBars: 120, expiry: 180, cooldown: 3, keep: 500, beAt: 0.3, signal: bollEMASignal}

	// 訂單塊 SMC:15m/1h/4h 三週期同頁。無逾時(expiry:0)—— 掛單型,位置到 + 型態成立才進場。
	s.smcBooks = []*microBook{
		{name: "orderblock", tf: "15m", barSec: 900, klimit: 500, minBars: obSwingSize + 5, expiry: 0, cooldown: 4, keep: 500, plan: tpSMCFib, stratKey: "orderblock", tfTag: true, signal: smcFibSignal, tpLevels4: smcFibTPLevels},
		{name: "orderblock_1h", tf: "1h", barSec: 3600, klimit: 500, minBars: obSwingSize + 5, expiry: 0, cooldown: 4, keep: 500, plan: tpSMCFib, stratKey: "orderblock", tfTag: true, signal: smcFibSignal, tpLevels4: smcFibTPLevels},
		{name: "orderblock_4h", tf: "4h", barSec: 14400, klimit: 500, minBars: obSwingSize + 5, expiry: 0, cooldown: 4, keep: 500, plan: tpSMCFib, stratKey: "orderblock", tfTag: true, signal: smcFibSignal, tpLevels4: smcFibTPLevels},
	}
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
		s.paperEMA.trades = db.loadTrades("emaonly")
		s.convTrades = db.loadTrades("conv")
		s.meanRevBook.trades = db.loadTrades("meanrev")
		s.bollEMABook.trades = db.loadTrades("bollema")
		for _, b := range s.ema2155Books {
			b.trades = db.loadTrades(b.name)
		}
		s.pulsarBook.trades = db.loadTrades("pulsar")
		s.pulsarV2Book.trades = db.loadTrades("pulsarv2")
		s.pulsarV3Book.trades = db.loadTrades("pulsarv3")
		s.pulsarV4Book.trades = db.loadTrades("pulsarv4")
		s.pulsarV5Book.trades = db.loadTrades("pulsarv5")
		s.pulsarV3sBook.trades = db.loadTrades("pulsarv3s")
		for _, b := range s.smcBooks {
			b.trades = db.loadTrades(b.name)
		}
		log.Printf("mysql loaded: %d score events, main=%d gamble=%d emaonly=%d trades",
			len(s.scoreLog), len(s.paperMain.trades), len(s.paperGamble.trades), len(s.paperEMA.trades))
	}
	// Bitunix 完全跟隨:有 follow 帳號才注入 DB + 掛鉤,並把上次未平的追蹤表載回續管。
	if s.trader != nil && s.trader.hasFollow() {
		s.trader.db = s.db
		if s.db != nil {
			for _, fp := range s.db.loadFollows() {
				s.trader.follows[fkey(fp.TradeID, fp.Acct)] = fp
			}
			log.Printf("bitunix follow: 載回 %d 筆進行中的跟隨單(重啟續管)", len(s.trader.follows))
		}
		exitMirrorLeg = s.trader.onLeg
		exitMirrorClose = s.trader.onClose
	}
	s.retrofitMultiTP() // backfill 分批止盈 levels onto open trades that predate multi-TP
	s.loadStratOff()    // restore per-strategy on/off switches
	s.loadStratCfg()    // restore per-strategy admin config (類型/風控/止損上限/保本/分批)
	s.loadTabPerms()    // restore 各身分組可見標籤 設定
	s.loadTabKinds()    // restore 各標籤 資訊/訊號 類型設定
	if s.db != nil {
		s.db.backfillRefCodes() // 每個帳號都要有推薦碼(不能等他自己開過我的推廣才生成)
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

// markLiveOpen refreshes each open trade's 現價 / 未實現損益 to the live WS price.
// Display-only: TP/SL exits are still evaluated on closed bars in the strategy
// tick, so it's safe for strategies (conv/pool) that only run per 4H/1H bar —
// their price would otherwise appear frozen between bars.
func markLiveOpen(open []*PaperTrade, px map[string]float64) {
	for _, tr := range open {
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		tr.Cur = roundPx(p)
		// 即時損益顯示採『鎖住的獲利階梯』口徑(settledPnL);各批實際結算仍記在 Realized。
		tr.PnLPct = round2(settledPnL(tr, p))
	}
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

// upbitTranslate turns a Korean title into Traditional Chinese. It tries the free
// Google endpoint first (fast); when that 429s / fails it falls back to the AI
// client (Gemini, else keyless Pollinations) which isn't IP-rate-limited. ok=false
// means BOTH failed → caller returns the Korean original but must NOT cache it, so
// it retries next tick. geminiBudget (nil = unlimited) caps AI calls per board
// rebuild so one tick can't stall on a big backlog of untranslated titles.
func (s *Store) upbitTranslate(text string, geminiBudget *int) (string, bool) {
	if strings.TrimSpace(text) == "" {
		return text, true
	}
	// 主翻譯:MyMemory(免金鑰、不封 IP)。Google 免費端點已因封 IP 淘汰。
	if zh, ok := s.upbitW.TranslateMyMemory(text); ok && zh != text {
		return zh, true
	}
	if geminiBudget != nil && *geminiBudget <= 0 {
		return text, false // 本輪 AI 額度用完 → 下輪再補
	}
	if s.maiW != nil {
		zh, err := s.maiW.Analyze(
			"你是專業翻譯。把使用者提供的韓國交易所公告標題翻成繁體中文,只輸出翻譯後的標題本身,不要加引號、說明或任何前後綴。",
			text)
		if geminiBudget != nil {
			*geminiBudget--
		}
		if err == nil {
			zh = strings.TrimSpace(strings.Trim(strings.TrimSpace(zh), "\"'「」"))
			if zh != "" && zh != text {
				return zh, true
			}
		}
	}
	return text, false
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
		// translate the title to zh-TW once, and reuse it for the board (seed the
		// cache) so both the push and Telegram messages are in Traditional Chinese.
		zh, ok := s.upbitTranslate(n.Title, nil) // fresh 很少,不限 AI 額度
		if ok {
			s.upbitMu.Lock()
			s.upbitTrans[n.ID] = zh
			s.upbitMu.Unlock()
		}
		// Web Push opens our own Upbit board tab (not upbit.com); the Telegram
		// message still links out to the real notice.
		s.PushSend(tag, zh, "/?tab=upbit")
		go s.notifier.Send(n.TelegramTextZH(zh))
	}
	s.updateUpbitBoard(all)
}

// updateUpbitBoard translates each notice title (ko→zh-TW, cached by id so every
// title is translated only once) and publishes the board newest-first.
func (s *Store) updateUpbitBoard(notices []upbit.Notice) {
	board := make([]UpbitNotice, 0, len(notices))
	geminiBudget := 6 // 每輪最多用 AI 補譯 6 筆,避免一次 tick 卡在整批未譯標題上
	for _, n := range notices {
		s.upbitMu.RLock()
		zh, cached := s.upbitTrans[n.ID]
		s.upbitMu.RUnlock()
		if !cached {
			z, ok := s.upbitTranslate(n.Title, &geminiBudget)
			zh = z
			if ok { // 只快取「成功」的翻譯;失敗(回原文)不快取,下輪重試
				s.upbitMu.Lock()
				s.upbitTrans[n.ID] = zh
				s.upbitMu.Unlock()
			}
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
func (s *Store) Register(username, password, uid, exchange, proof, refCode string) error {
	if err := s.PrecheckRegister(username, password); err != nil {
		return err
	}
	h, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	// 綁定推薦人 — 這是「唯一」會發生綁定的時機,之後永不變更。
	// 無效/不存在的碼一律當沒帶(不讓註冊因此失敗)。自我推薦在此結構下不可能:
	// 新用戶的 ref_code 是註冊當下才生成的,他不可能事先知道自己的碼。
	refBy := ""
	if refCode != "" {
		if owner := s.db.userByRefCode(refCode); owner != "" && !strings.EqualFold(owner, username) {
			refBy = owner
		}
	}
	s.db.registerUser(username, h, uid, exchange, proof, refBy)
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

// UserCreated returns the account's registration epoch (ms), 0 if unknown.
func (s *Store) UserCreated(username string) int64 {
	if s.db == nil {
		return 0
	}
	return s.db.userCreated(username)
}

func (s *Store) Users() []User {
	if s.db == nil {
		return nil
	}
	return s.db.listUsers()
}

// randPassword makes a readable random password (crypto/rand) from an
// ambiguity-free alphabet (no 0/O/1/l/I) so it can be dictated/copied cleanly.
func randPassword(n int) string {
	const alpha = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	for i := range b {
		b[i] = alpha[int(b[i])%len(alpha)]
	}
	return string(b)
}

// AdminResetPassword sets a fresh random password for a user and returns the
// PLAINTEXT (shown once to the admin to hand over). ok=false if user absent / db off.
func (s *Store) AdminResetPassword(username string) (string, bool) {
	if s.db == nil {
		return "", false
	}
	pw := randPassword(10)
	if pw == "" {
		return "", false
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return "", false
	}
	if !s.db.setPassword(username, h) {
		return "", false
	}
	return pw, true
}

// ChangePassword verifies the caller's CURRENT password, then sets a new one.
func (s *Store) ChangePassword(username, oldPw, newPw string) error {
	if s.db == nil {
		return errors.New("persistence disabled")
	}
	hash, _, _, ok := s.db.userAuth(username)
	if !ok {
		return errors.New("使用者不存在")
	}
	if !auth.CheckPassword(hash, oldPw) {
		return errors.New("目前密碼不正確")
	}
	nh, err := auth.HashPassword(newPw)
	if err != nil {
		return err
	}
	if !s.db.setPassword(username, nh) {
		return errors.New("更新失敗")
	}
	return nil
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
		s.surge.scan(tickers, time.Now())  // 爆量脈搏斥候(零額外 REST,純記憶體)
	}

	// OI 儀表板 / 數據訊號 / 評分穿越的監控幣池 = 動態「成交量前 N」(emaCoins,與策略同
	// 池、自動換血)。setEMAUniverse 已在上面跑過,這裡拿到的就是本輪最新的 top-N;
	// 開機第一輪(還沒抓到行情)emaCoins() 會 fallback 到 s.coins。
	coins := s.emaCoins()
	nextSnaps := make(map[string]Snapshot, len(coins))
	nextDetails := make(map[string]CoinDetail, len(coins))
	for _, coin := range coins {
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
