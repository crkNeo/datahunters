package cache

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"datahunter/internal/newsfeed"
)

// newsalpha.go — 消息雷達(資訊落差交易計畫)。
//
// 把所有免費消息來源收進同一條管線:收集 → 標記(分類 + 多空 + 強度,AI 輔助)→ 寫進事件表 →
// 自動記錄之後 1m / 5m / 15m / 1h / 4h 的價格反應 → 統計哪類消息、哪個來源真的有方向。
// 可交易的類別(韓所上幣、交易所上幣 / 下架、警示、被駭)另外開模擬單(觀察書,永遠不實單)。
//
//	來源層 internal/newsfeed(抓 + 解析)
//	排程 / 分類 / 入庫 / 推播 / AI     newsalpha.go(本檔)
//	各來源的接法(含 Upbit / Robinhood / RSS 掛勾、溢價、預測市場、鏈上、解鎖、總經)newsalpha_src.go
//	價格反應回填 / 統計 / 分頁          newsalpha_stats.go
//	模擬單                              newsalpha_book.go
//
// 原則(文件的法律界線):只用公開資訊;不買內線、不入侵、不做 mempool 三明治。

// NewsEvent 是一則標記過的消息。R* 為 nil = 還沒回填(或那個時間點抓不到價格)。
type NewsEvent struct {
	ID         string   `json:"id"`
	Ts         int64    `json:"ts"`  // 我們第一次看到的時間(ms)
	Pub        int64    `json:"pub"` // 來源標示的發布時間(ms,0 = 不明)
	Source     string   `json:"source"`
	Category   string   `json:"category"`
	Coin       string   `json:"coin"`  // 主幣種(對應 Binance 合約名,例如 1000PEPE;沒有合約時為原代號)
	Coins      []string `json:"coins"` // 標題裡比對到的所有幣
	Title      string   `json:"title"`
	TitleZH    string   `json:"title_zh"`
	URL        string   `json:"url"`
	Dir        string   `json:"dir"`      // long | short | ""(中性)
	Strength   int      `json:"strength"` // 0..3
	Tradable   bool     `json:"tradable"` // 這類消息會開模擬單
	Value      float64  `json:"value"`    // 數值型來源的附帶值(鏈上 USD、溢價 %、機率變化 pt…)
	AIDir      string   `json:"ai_dir"`
	AIStrength int      `json:"ai_strength"`
	AIReason   string   `json:"ai_reason"`
	Price0     float64  `json:"price0"` // 看到消息當下的價格
	R1m        *float64 `json:"r1m"`
	R5m        *float64 `json:"r5m"`
	R15m       *float64 `json:"r15m"`
	R1h        *float64 `json:"r1h"`
	R4h        *float64 `json:"r4h"`
	Up60       *float64 `json:"up60"`   // 60 分鐘內最大漲幅 %
	Dn60       *float64 `json:"dn60"`   // 60 分鐘內最大跌幅 %(負值)
	Filled     int      `json:"filled"` // 0 未回填 / 1 回填到 1h / 2 全部完成
	Traded     bool     `json:"traded"`
	Late       bool     `json:"late"` // 停機補記:伺服器關機期間發布、重啟後才補進來的(只記錄、不推播、不開單)
}

// 分類 → 中文標籤(前端也用這份)。
var newsCatLabel = map[string]string{
	"listing_kr":   "韓所上幣",
	"listing":      "交易所上幣",
	"listing_us":   "美國券商上架",
	"delist":       "下架",
	"warning":      "警示 / 監控",
	"warning_lift": "警示解除",
	"hack":         "被駭 / 漏洞",
	"unlock":       "代幣解鎖",
	"etf":          "ETF / 申報",
	"regulation":   "監管",
	"whale":        "巨鯨 / 鏈上",
	"listing_hint": "上幣前兆(鏈上)",
	"governance":   "治理提案",
	"dev":          "專案升級",
	"premium":      "韓國溢價",
	"prediction":   "預測市場",
	"macro":        "總經數據",
	"partner":      "合作 / 主網",
	"other":        "其他",
}

// 交易所 / 券商官方來源:只有這些來源的消息會開模擬單(Telegram / 新聞的轉述可能是假消息)。
var newsOfficial = map[string]bool{"binance": true, "okx": true, "bybit": true, "upbit": true, "bithumb": true, "robinhood": true}

const (
	naKeepEvents  = 400              // 記憶體裡保留的最新事件數(沒 DB 時也靠它)
	naAIPerHour   = 30               // AI 判斷每小時上限(免費額度)
	naStaleTrade  = 15 * time.Minute // 來源發布時間早於這麼久才被看到 → 資訊落差已經沒了,只記錄不開單
	naPushMinStr  = 2                // 強度 ≥ 2 且有幣種才推播
	naPriceMaxAge = 20 * time.Second
	naCatchUp     = 12 * time.Hour // 重啟後補記:發布時間在這段內、資料庫還沒有的公告
	naCatchUpMax  = 30             // 每個來源最多補記幾則
)

// naSource 是一個排程來源。every = 0 代表由其他 tick 掛勾餵進來(Upbit / Robinhood / RSS)。
type naSource struct {
	key, label, group string
	every             time.Duration
	seed              bool // 第一次成功抓取只建基準、不發事件(公告類);計算型來源(溢價、解鎖…)不需要
	fetch             func() ([]naIn, error)
	note              string
}

// naIn 是送進管線的一則消息(來源 Item + 已經有的中文翻譯)。
type naIn struct {
	newsfeed.Item
	zh string
}

// NewsSourceStat 是「來源狀態」頁的一列:延遲、成功 / 失敗、最近抓到幾則。
type NewsSourceStat struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Group     string `json:"group"`
	EverySec  int    `json:"every_sec"` // 0 = 掛勾在其他排程上
	Note      string `json:"note"`
	LastTry   int64  `json:"last_try"`
	LastOK    int64  `json:"last_ok"`
	LatencyMs int64  `json:"latency_ms"`
	Fails     int    `json:"fails"` // 連續失敗次數
	LastErr   string `json:"last_err"`
	Items     int    `json:"items"`  // 最近一次抓到幾則
	Events    int    `json:"events"` // 開機以來產生幾則新事件
}

type newsAlpha struct {
	nf *newsfeed.Client

	mu         sync.Mutex
	srcs       []*naSource
	stat       map[string]*NewsSourceStat
	running    map[string]bool
	seeded     map[string]bool
	seen       map[string]map[string]bool // source → 已看過的 id
	nextRun    map[string]time.Time
	events     []*NewsEvent // 新的在前
	aiCalls    []int64      // 最近一小時的 AI 呼叫時間
	aiTried    map[string]bool
	reactFails map[string]int // 價格回填抓 K 線失敗次數(超過上限就放棄)
	started    time.Time

	pxMu   sync.Mutex
	pxAt   time.Time
	perp   map[string]float64 // Binance 永續:幣(含 1000PEPE 這種)→ 價格
	spot   map[string]float64 // Binance 現貨 USDT 對:幣 → 價格
	pxLock sync.Mutex         // 序列化價格刷新(避免同時多個來源各打一次)

	ext naExtras // 溢價 / 預測市場 / 鏈上等計算型來源的狀態(newsalpha_src.go)
}

func newNewsAlpha() *newsAlpha {
	return &newsAlpha{nf: newsfeed.New(), stat: map[string]*NewsSourceStat{}, running: map[string]bool{},
		seeded: map[string]bool{}, seen: map[string]map[string]bool{}, nextRun: map[string]time.Time{},
		aiTried: map[string]bool{}, reactFails: map[string]int{}, started: time.Now(), perp: map[string]float64{}, spot: map[string]float64{},
		ext: newNAExtras()}
}

// register 登記一個來源(NewStore 之後由 initNewsAlpha 呼叫)。
func (n *newsAlpha) register(src *naSource) {
	n.srcs = append(n.srcs, src)
	n.stat[src.key] = &NewsSourceStat{Key: src.key, Label: src.label, Group: src.group, EverySec: int(src.every / time.Second), Note: src.note}
	n.seen[src.key] = map[string]bool{}
}

// NewsAlphaTick 每 5 秒由 main 呼叫:到期的來源各開一條 goroutine 去抓(同一來源不重疊),
// 再順手跑 AI 判斷(有額度時)。
func (s *Store) NewsAlphaTick() {
	n := s.news
	if n == nil {
		return
	}
	now := time.Now()
	n.mu.Lock()
	var due []*naSource
	for _, src := range n.srcs {
		if src.every <= 0 || n.running[src.key] || now.Before(n.nextRun[src.key]) {
			continue
		}
		n.running[src.key] = true
		n.nextRun[src.key] = now.Add(src.every)
		due = append(due, src)
	}
	n.mu.Unlock()
	for _, src := range due {
		go s.naRun(src)
	}
	s.naAITick()
}

// naRun 抓一次某個來源,記錄延遲 / 成敗,再把結果送進管線。
func (s *Store) naRun(src *naSource) {
	n := s.news
	defer func() {
		if r := recover(); r != nil { // 單一來源的解析 bug 不能拖垮整個伺服器
			log.Printf("消息雷達[%s] panic: %v", src.key, r)
		}
		n.mu.Lock()
		n.running[src.key] = false
		n.mu.Unlock()
	}()
	t0 := time.Now()
	items, err := src.fetch()
	s.naMark(src.key, t0, len(items), err)
	if err != nil {
		return
	}
	s.naIngest(src, items)
}

// naMark 更新來源狀態;連續失敗交給 apiFail(達門檻會發一次 Telegram 告警)。
func (s *Store) naMark(key string, t0 time.Time, items int, err error) {
	label := s.naStat(key, t0, items, err)
	if err != nil {
		s.apiFail("消息雷達·"+label, err.Error())
	} else {
		s.apiOK("消息雷達·" + label)
	}
}

// naHookMark:掛勾來源(Upbit / Robinhood / 新聞 RSS)每次排程跑完都回報狀態,不論有沒有新消息。
// 不另發告警 —— 原本的排程自己有。
func (s *Store) naHookMark(key string, t0 time.Time, items int, err error) {
	if s.news != nil {
		s.naStat(key, t0, items, err)
	}
}

// naStat 只更新「來源狀態」表,回傳來源名稱。
func (s *Store) naStat(key string, t0 time.Time, items int, err error) string {
	n := s.news
	n.mu.Lock()
	st := n.stat[key]
	if st != nil {
		st.LastTry = t0.UnixMilli()
		st.LatencyMs = time.Since(t0).Milliseconds()
		if err != nil {
			st.Fails++
			st.LastErr = trimRunes(err.Error(), 160)
		} else {
			st.Fails, st.LastErr, st.LastOK, st.Items = 0, "", time.Now().UnixMilli(), items
		}
	}
	label := key
	if st != nil {
		label = st.Label
	}
	n.mu.Unlock()
	return label
}

// naHook 給 Upbit / Robinhood / RSS 這些「本來就有排程」的地方餵新消息(它們自己已處理過開機基準)。
// 狀態由呼叫端用 naHookMark 回報。
func (s *Store) naHook(key string, items []naIn) {
	n := s.news
	if n == nil {
		return
	}
	n.mu.Lock()
	var src *naSource
	for _, x := range n.srcs {
		if x.key == key {
			src = x
		}
	}
	n.seeded[key] = true
	n.mu.Unlock()
	if src == nil {
		return
	}
	if len(items) > 0 {
		s.naIngest(src, items)
	}
}

// naEventID:來源 + 原始 id;太長(網址當 id)就雜湊,塞得進 VARCHAR(191) 主鍵。
func naEventID(src, id string) string {
	full := src + "|" + id
	if len(full) <= 180 {
		return full
	}
	h := sha1.Sum([]byte(id))
	return src + "|h:" + hex.EncodeToString(h[:])
}

// naIngest:去重 → 分類 → 對幣種 → 記價格 → 翻譯 → 入庫 → 推播 → 開模擬單。
func (s *Store) naIngest(src *naSource, items []naIn) {
	n := s.news
	n.mu.Lock()
	first := src.seed && !n.seeded[src.key]
	n.seeded[src.key] = true
	seen := n.seen[src.key]
	var fresh, late []naIn
	now := time.Now()
	for _, it := range items {
		if it.ID == "" || seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		switch {
		case !first:
			fresh = append(fresh, it)
		case s.db != nil && !it.Published.IsZero() && now.Sub(it.Published) <= naCatchUp && len(late) < naCatchUpMax:
			late = append(late, it) // 開機基準裡較新的:可能是停機期間發的,資料庫沒有就補記
		}
	}
	if len(seen) > 3000 { // 防無限長大:清掉後,這一批仍標記為已看過
		m := make(map[string]bool, len(items))
		for _, it := range items {
			m[it.ID] = true
		}
		n.seen[src.key] = m
	}
	n.mu.Unlock()
	if len(late) > 0 {
		s.naCatchUpIngest(src, late)
	}
	if len(fresh) == 0 {
		return
	}
	s.naPrices(0) // 盡量用最新的價格當事件的基準價
	for _, it := range fresh {
		ev := s.naBuild(src, it)
		if ev == nil {
			continue
		}
		if s.db != nil && !s.db.insertNewsEvent(ev) {
			continue // 主鍵重複(重啟前已記過)→ 不重推、不重開單
		}
		s.naNotify(ev) // 先推播再放進共享清單(之後 AI / 回填會改這筆)
		n.mu.Lock()
		n.events = append([]*NewsEvent{ev}, n.events...)
		if len(n.events) > naKeepEvents {
			n.events = n.events[:naKeepEvents]
		}
		if st := n.stat[src.key]; st != nil {
			st.Events++
		}
		n.mu.Unlock()
		if ev.Tradable {
			s.naTrade(ev, it.Published)
		}
	}
}

// naCatchUpIngest 補記停機期間的公告:時間記成來源發布時間(價格反應從那一分鐘起算),
// 已經在資料庫的(停機前就記過)由主鍵擋掉。只記錄,不推播、不開單、不花 AI 額度。
func (s *Store) naCatchUpIngest(src *naSource, items []naIn) {
	n := s.news
	s.naPrices(0)
	added := 0
	for _, it := range items {
		if s.db.hasNewsEvent(naEventID(it.Source, it.ID)) {
			continue // 停機前已記過 → 不重做翻譯
		}
		ev := s.naBuild(src, it)
		if ev == nil {
			continue
		}
		ev.Ts, ev.Price0, ev.Tradable, ev.Late = it.Published.UnixMilli(), 0, false, true
		if !s.db.insertNewsEvent(ev) {
			continue
		}
		n.mu.Lock()
		n.events = append(n.events, ev)
		n.aiTried[ev.ID] = true
		if st := n.stat[src.key]; st != nil {
			st.Events++
		}
		n.mu.Unlock()
		added++
	}
	if added == 0 {
		return
	}
	n.mu.Lock()
	sort.SliceStable(n.events, func(i, j int) bool { return n.events[i].Ts > n.events[j].Ts })
	if len(n.events) > naKeepEvents {
		n.events = n.events[:naKeepEvents]
	}
	n.mu.Unlock()
	log.Printf("消息雷達[%s] 補記停機期間的公告 %d 則", src.key, added)
}

// NewsAlphaDelete 刪除一則事件(管理員):記憶體、資料庫都刪,並清掉分頁 / 統計快取。
func (s *Store) NewsAlphaDelete(id string) bool {
	n := s.news
	if n == nil || id == "" {
		return false
	}
	found := false
	n.mu.Lock()
	for i, ev := range n.events {
		if ev.ID == id {
			n.events = append(n.events[:i], n.events[i+1:]...)
			found = true
			break
		}
	}
	n.mu.Unlock()
	if s.db != nil && s.db.deleteNewsEvent(id) {
		found = true
	}
	if found {
		naHistCache.clear()
		naStatsCache.clear()
	}
	return found
}

// naBuild 把一則來源消息變成事件(分類、幣種、基準價、中文標題)。
func (s *Store) naBuild(src *naSource, it naIn) *NewsEvent {
	n := s.news
	title := strings.TrimSpace(it.Title)
	if title == "" {
		return nil
	}
	cat, dir, str, trad := naClassify(it.Source, title, it.Category, it.Value)
	coins := it.Coins
	if len(coins) == 0 {
		coins = newsfeed.ExtractTickers(title, n.known)
		if len(coins) == 0 && newsOfficial[it.Source] { // 官方公告的新幣常常還沒有行情 → 放寬只看明確格式
			coins = newsfeed.LooseTickers(title)
		}
	}
	if len(coins) > 8 {
		coins = coins[:8]
	}
	for i, c := range coins {
		if tc := n.perpName(c); tc != "" {
			coins[i] = tc
		}
	}
	ev := &NewsEvent{ID: naEventID(it.Source, it.ID), Ts: time.Now().UnixMilli(), Source: it.Source, Category: cat,
		Coins: coins, Title: trimRunes(title, 600), URL: it.URL, Dir: dir, Strength: str, Value: it.Value}
	if !it.Published.IsZero() {
		ev.Pub = it.Published.UnixMilli()
	}
	if len(coins) > 0 {
		ev.Coin = coins[0]
		ev.Price0, _ = n.price(ev.Coin)
	}
	// 只有官方來源、而且抓得到幣種的才開單;沒有永續合約的幣也開不了
	ev.Tradable = trad && newsOfficial[it.Source] && ev.Coin != "" && n.hasPerp(ev.Coin)
	ev.TitleZH = it.zh
	if ev.TitleZH == "" && it.Lang == "zh" {
		ev.TitleZH = title
	}
	if ev.TitleZH == "" && (cat != "other" || ev.Coin != "") { // 跟幣價有關的才翻(省翻譯額度)
		ev.TitleZH = s.naTranslate(title, it.Lang)
	}
	return ev
}

// naTranslate:韓文走 MyMemory,英文走 Google 免金鑰端點;失敗回空字串(AI 判斷時會補)。
func (s *Store) naTranslate(text, lang string) string {
	switch lang {
	case "ko":
		if s.upbitW != nil {
			if zh, ok := s.upbitW.TranslateMyMemory(text); ok && zh != text {
				return zh
			}
		}
	case "en":
		if s.gdeltW != nil {
			if zh := s.gdeltW.Translate(text); zh != text {
				return zh
			}
		}
	}
	return ""
}

// ── 分類規則(多語關鍵字)──

func hasAny(s string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

var naExchangeWords = []string{"binance", "coinbase", "okx", "okex", "bybit", "kraken", "bitfinex", "huobi", "htx", "kucoin", "gate.io", "gateio", "bitget", "upbit", "bithumb", "mexc", "crypto.com", "bitstamp", "gemini"}
var naStableWords = []string{"usdt", "usdc", "dai ", "fdusd", "tusd", "usde", "pyusd", "usds", "busd"}

// naClassify 判斷分類、方向、強度(0..3)與是否屬於「會開模擬單」的類別。
// preset 是來源本身已確定的分類(鏈上、Snapshot、GitHub…),value 為數值型來源的附帶值。
func naClassify(source, title, preset string, value float64) (cat, dir string, str int, trad bool) {
	t := strings.ToLower(title)
	official := newsOfficial[source]
	switch preset {
	case "whale":
		return "whale", naWhaleDir(t, value), 1, false
	case "premium":
		if value > 0 {
			return "premium", "long", 1, false
		}
		return "premium", "short", 1, false
	case "unlock":
		return "unlock", "short", 1, false
	case "governance", "dev", "prediction", "macro":
		return preset, "", 1, false
	case "listing_hint":
		return "listing_hint", "long", 1, false
	case "listing_us":
		return "listing_us", "long", 2, true
	case "etf":
		return "etf", naApproveDir(t), 1, false
	}
	if strings.HasPrefix(source, "tg:whale") || strings.Contains(t, "transferred from") {
		return "whale", naWhaleDir(t, value), 1, false
	}
	korean := source == "upbit" || source == "bithumb"
	switch {
	case hasAny(t, "hacked", "hack ", "exploit", "drained", "stolen", "security incident", "attack", "被駭", "駭客", "黑客", "被盗", "被盜", "攻擊", "攻击", "해킹", "탈취"):
		return "hack", "short", 2, true
	case hasAny(t, "해제") && hasAny(t, "유의"), hasAny(t, "remove the monitoring tag", "removal of monitoring tag", "解除"):
		return "warning_lift", "long", 1, false
	case hasAny(t, "delist", "will remove", "cease trading", "removal of", "거래지원 종료", "거래 지원 종료", "下架", "下線", "下线", "終止交易", "终止交易"):
		if hasAny(t, "spot trading pairs", "trading pairs", "margin", "交易對", "交易对") && !hasAny(t, "delist", "거래지원 종료", "下架") {
			return "other", "", 0, false // 只是移除部分交易對,不是整個幣下架
		}
		if official {
			return "delist", "short", 3, true
		}
		return "delist", "short", 2, true
	case hasAny(t, "monitoring tag", "유의 종목", "유의종목", "투자유의", "투자 유의", "거래유의", "investment warning", "警示", "風險警告", "风险警告"):
		return "warning", "short", 2, true
	case hasAny(t, "will list", "to list ", "will be listed", "new listing", "listing of", "lists ", "will launch", "launches", "신규 거래지원", "디지털 자산 추가", "마켓 추가", "원화 마켓", "krw 마켓", "上幣", "上币", "上線", "上线", "上架", "新增交易"):
		if hasAny(t, "launch") && !hasAny(t, "perpetual", "futures", "spot", "pre-market", "listing") {
			return "other", "", 0, false // Binance Launches Campaign 這類活動公告
		}
		if korean {
			if hasAny(t, "krw", "원화") {
				return "listing_kr", "long", 3, true
			}
			return "listing_kr", "long", 2, true
		}
		if official {
			return "listing", "long", 2, true
		}
		return "listing", "long", 1, false
	case hasAny(t, "will add", "adds support", "now available on"):
		return "listing", "long", 1, false
	case hasAny(t, "unlock", "解鎖", "解锁", "언락"):
		return "unlock", "short", 1, false
	case hasAny(t, " etf", "etf ", "現貨etf", "现货etf"):
		return "etf", naApproveDir(t), 1, false
	case hasAny(t, " sec ", "sec:", "lawsuit", "regulat", "cftc", "doj", "監管", "监管", "起訴", "起诉", "규제", "금융위"):
		return "regulation", naApproveDir(t), 1, false
	case hasAny(t, "partnership", "partners with", "mainnet", "integrat", "合作", "主網", "主网", "파트너"):
		return "partner", "long", 1, false
	}
	return "other", "", 0, false
}

// naApproveDir:批准 → 多;拒絕 / 起訴 → 空;否則中性。
func naApproveDir(t string) string {
	switch {
	case hasAny(t, "approve", "approval", "approved", "greenlight", "批准", "通過", "通过", "승인"):
		return "long"
	case hasAny(t, "reject", "denied", "denies", "delay", "lawsuit", "sues ", "charged", "拒絕", "拒绝", "延後", "推遲", "起訴", "起诉", "거절"):
		return "short"
	}
	return ""
}

// naWhaleDir:非穩定幣轉進交易所 = 準備賣(空);從交易所轉出 = 囤幣(多);穩定幣轉進交易所 = 準備買(多)。
// value < 0 代表鏈上監控判斷為「轉出交易所」,> 0 為「轉入」。
func naWhaleDir(t string, value float64) string {
	stable := hasAny(t+" ", naStableWords...)
	into, out := value > 0, value < 0
	if value == 0 {
		for _, ex := range naExchangeWords {
			if strings.Contains(t, "to #"+ex) || strings.Contains(t, "to "+ex) {
				into = true
			}
			if strings.Contains(t, "from #"+ex) || strings.Contains(t, "from "+ex) {
				out = true
			}
		}
		if into && out { // 交易所之間互轉:沒有方向
			return ""
		}
	}
	switch {
	case into && stable:
		return "long"
	case into:
		return "short"
	case out && !stable:
		return "long"
	}
	return ""
}

// ── 幣種 / 價格 ──

// naPrices 刷新 Binance 永續 + 現貨全市場價格(maxAge 內不重抓;0 = 用預設 20 秒)。
func (s *Store) naPrices(maxAge time.Duration) {
	n := s.news
	if maxAge <= 0 {
		maxAge = naPriceMaxAge
	}
	n.pxLock.Lock()
	defer n.pxLock.Unlock()
	n.pxMu.Lock()
	fresh := time.Since(n.pxAt) < maxAge
	n.pxMu.Unlock()
	if fresh {
		return
	}
	perp := map[string]float64{}
	if px, err := s.ex.BinanceAllPrices(); err == nil {
		for sym, p := range px {
			if strings.HasSuffix(sym, "USDT") && p > 0 {
				perp[coinOf(sym)] = p
			}
		}
	}
	spot := map[string]float64{}
	if px, err := s.ex.BinanceSpotAllPrices(); err == nil {
		for sym, p := range px {
			if strings.HasSuffix(sym, "USDT") && p > 0 {
				spot[coinOf(sym)] = p
			}
		}
	}
	n.pxMu.Lock()
	if len(perp) > 0 {
		n.perp = perp
	}
	if len(spot) > 0 {
		n.spot = spot
	}
	if len(perp) > 0 || len(spot) > 0 {
		n.pxAt = time.Now()
	}
	n.pxMu.Unlock()
}

// known:幣安永續 / 現貨有這個幣(含 1000 倍合約)。
func (n *newsAlpha) known(t string) bool {
	n.pxMu.Lock()
	defer n.pxMu.Unlock()
	return n.perp[t] > 0 || n.perp["1000"+t] > 0 || n.spot[t] > 0
}

// perpName 把代號換成 Binance 永續合約名(PEPE → 1000PEPE);沒有合約回 ""。
func (n *newsAlpha) perpName(t string) string {
	n.pxMu.Lock()
	defer n.pxMu.Unlock()
	switch {
	case n.perp[t] > 0:
		return t
	case n.perp["1000"+t] > 0:
		return "1000" + t
	}
	return ""
}

func (n *newsAlpha) hasPerp(coin string) bool {
	n.pxMu.Lock()
	defer n.pxMu.Unlock()
	return n.perp[coin] > 0
}

// price 回傳幣價(優先永續,沒有再看現貨)與是否為永續。
func (n *newsAlpha) price(coin string) (float64, bool) {
	n.pxMu.Lock()
	defer n.pxMu.Unlock()
	if p := n.perp[coin]; p > 0 {
		return p, true
	}
	return n.spot[coin], false
}

// ── 推播 ──

// naNotify:強度 ≥ 2 且有幣種的消息推「消息雷達」(使用者可在帳號頁關掉)。
func (s *Store) naNotify(ev *NewsEvent) {
	if ev.Strength < naPushMinStr || ev.Coin == "" {
		return
	}
	title := "📢 消息雷達 · " + newsCatLabel[ev.Category]
	t := ev.TitleZH
	if t == "" {
		t = ev.Title
	}
	d := ""
	switch ev.Dir {
	case "long":
		d = " 偏多"
	case "short":
		d = " 偏空"
	}
	s.PushSendTopic("msg:newsalpha", title, fmt.Sprintf("%s%s · %s", ev.Coin, d, trimRunes(t, 120)), "/?tab=newsalpha")
}

// ── AI 判斷(步驟 4 後半:規則跑順後再加 AI)──

const naAISystem = `你是加密貨幣消息面分析師。判斷這則消息對指定幣種「接下來 1 小時」價格的影響。
只輸出一行 JSON,不要其他文字:{"dir":"long|short|neutral","strength":0-3,"reason":"繁體中文一句話理由(30字內)","zh":"消息的繁體中文翻譯(60字內)"}
strength:0=無影響 1=弱 2=中 3=強。注意「買傳聞、賣事實」、消息是否早已被市場消化、來源是否可信。`

// naAITick 每輪最多判斷 2 則還沒判斷過的事件(強度高的優先),每小時最多 naAIPerHour 次。
func (s *Store) naAITick() {
	n := s.news
	if s.maiW == nil {
		return
	}
	now := time.Now().UnixMilli()
	n.mu.Lock()
	keep := n.aiCalls[:0]
	for _, t := range n.aiCalls {
		if now-t < 3600_000 {
			keep = append(keep, t)
		}
	}
	n.aiCalls = keep
	budget := naAIPerHour - len(n.aiCalls)
	if budget > 2 {
		budget = 2
	}
	var todo []*NewsEvent
	for _, ev := range n.events {
		if n.aiTried[ev.ID] || ev.Coin == "" || now-ev.Ts > 30*60_000 {
			continue
		}
		switch ev.Category { // 數值型 / 高頻來源不花 AI 額度
		case "whale", "premium", "prediction", "macro", "unlock":
			continue
		}
		todo = append(todo, ev)
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].Strength > todo[j].Strength })
	if len(todo) > budget {
		todo = todo[:max(budget, 0)]
	}
	for _, ev := range todo {
		n.aiTried[ev.ID] = true
		n.aiCalls = append(n.aiCalls, now)
	}
	if len(n.aiTried) > 2000 {
		n.aiTried = map[string]bool{}
		for _, ev := range n.events {
			n.aiTried[ev.ID] = true
		}
	}
	n.mu.Unlock()
	for _, ev := range todo {
		go s.naAIJudge(ev)
	}
}

func (s *Store) naAIJudge(ev *NewsEvent) {
	n := s.news
	n.mu.Lock()
	user := fmt.Sprintf("來源:%s\n規則分類:%s\n幣種:%s\n消息:%s", ev.Source, newsCatLabel[ev.Category], ev.Coin, ev.Title)
	n.mu.Unlock()
	out, err := s.maiW.Analyze(naAISystem, user)
	if err != nil {
		return
	}
	dir, str, reason, zh, ok := parseNAAI(out)
	if !ok {
		return
	}
	n.mu.Lock()
	ev.AIDir, ev.AIStrength, ev.AIReason = dir, str, reason
	if ev.TitleZH == "" && zh != "" {
		ev.TitleZH = zh
	}
	cp := *ev
	n.mu.Unlock()
	if s.db != nil {
		s.db.updateNewsAI(&cp)
	}
}

// parseNAAI 解析 AI 回的 JSON(容忍前後多餘文字 / ```json 包裝)。
func parseNAAI(out string) (dir string, str int, reason, zh string, ok bool) {
	a, b := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if a < 0 || b <= a {
		return
	}
	var r struct {
		Dir      string  `json:"dir"`
		Strength float64 `json:"strength"`
		Reason   string  `json:"reason"`
		ZH       string  `json:"zh"`
	}
	if json.Unmarshal([]byte(out[a:b+1]), &r) != nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.Dir)) {
	case "long", "bullish", "多":
		dir = "long"
	case "short", "bearish", "空":
		dir = "short"
	default:
		dir = "neutral"
	}
	str = int(r.Strength)
	if str < 0 {
		str = 0
	}
	if str > 3 {
		str = 3
	}
	return dir, str, trimRunes(strings.TrimSpace(r.Reason), 120), trimRunes(strings.TrimSpace(r.ZH), 200), true
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
