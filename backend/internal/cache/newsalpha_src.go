package cache

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"datahunter/internal/gdelt"
	"datahunter/internal/newsfeed"
	"datahunter/internal/robinhood"
	"datahunter/internal/upbit"
)

// newsalpha_src.go — 消息雷達的各個來源怎麼接進管線。全部免費:
//
//	優先 1 交易所公告:幣安 / OKX / Bybit / Bithumb(輪詢)、Upbit(沿用既有 Upbit 排程)
//	優先 1 美國券商:Robinhood 上架(沿用既有排程)
//	優先 2 鏈上:以太坊交易所熱錢包大額轉入 / 轉出、首次出現的新代幣(需設 ALCHEMY_URL 或 INFURA_URL)
//	優先 3 Telegram 公開頻道(t.me/s 網頁預覽,不用帳號):Whale Alert、快訊頻道(NEWS_TG_CHANNELS)
//	優先 4 GitHub 新版本、Snapshot 治理提案、SEC EDGAR 申報
//	優先 5 Polymarket 機率急變、韓國溢價(Upbit 韓元價 vs 幣安)
//	優先 6 代幣解鎖(既有解鎖面板)、總經數據公布(既有經濟日曆)、新聞 RSS(既有新聞快訊)

// 環境變數(全部選填):
//
//	NEWS_TG_CHANNELS  逗號分隔「頻道[:語言]」,預設 whale_alert_io:en,WatcherGuru:en,BWEnews:zh,binance_announcements:en
//	NEWS_GH_REPOS     逗號分隔「owner/repo:幣種」,預設見 naDefaultRepos
//	GITHUB_TOKEN      有的話 GitHub 每小時 5000 次(沒有 60 次)
//	SEC_UA            SEC 規定要帶聯絡方式的 User-Agent,例如「MySite admin@example.com」
//	ALCHEMY_URL / INFURA_URL  以太坊 HTTPS RPC(免費額度),沒設就不跑鏈上監控
//	NEWS_WALLETS      逗號分隔「名稱:地址」,覆蓋預設的交易所熱錢包清單

var naDefaultTG = "whale_alert_io:en,WatcherGuru:en,BWEnews:zh,binance_announcements:en"

var naDefaultRepos = map[string]string{
	"bitcoin/bitcoin": "BTC", "ethereum/go-ethereum": "ETH", "anza-xyz/agave": "SOL", "XRPLF/rippled": "XRP",
	"IntersectMBO/cardano-node": "ADA", "dogecoin/dogecoin": "DOGE", "MystenLabs/sui": "SUI",
	"aptos-labs/aptos-core": "APT", "near/nearcore": "NEAR", "paritytech/polkadot-sdk": "DOT",
	"ava-labs/avalanchego": "AVAX", "cosmos/gaia": "ATOM", "tronprotocol/java-tron": "TRX",
	"litecoin-project/litecoin": "LTC", "OffchainLabs/nitro": "ARB", "ethereum-optimism/optimism": "OP",
	"stellar/stellar-core": "XLM", "filecoin-project/lotus": "FIL", "hashgraph/hedera-services": "HBAR",
	"ton-blockchain/ton": "TON", "sei-protocol/sei-chain": "SEI", "celestiaorg/celestia-node": "TIA",
}

var naSnapshotSpaces = map[string]string{
	"aave.eth": "AAVE", "uniswapgovernance.eth": "UNI", "arbitrumfoundation.eth": "ARB", "opcollective.eth": "OP",
	"ens.eth": "ENS", "lido-snapshot.eth": "LDO", "gmx.eth": "GMX", "curve.eth": "CRV", "balancer.eth": "BAL",
	"safe.eth": "SAFE", "cvx.eth": "CVX", "apecoin.eth": "APE", "1inch.eth": "1INCH", "sushigov.eth": "SUSHI",
	"pancakeswap.eth": "CAKE",
}

// SEC 全文檢索的關鍵字 → 幣種(BTC / ETH 太多公司財報會提到,雜訊太大,不查)。
var naSECQueries = map[string]string{
	"Solana": "SOL", "XRP": "XRP", "Dogecoin": "DOGE", "Litecoin": "LTC", "Cardano": "ADA", "Avalanche": "AVAX",
	"Polkadot": "DOT", "Hedera": "HBAR", "Sui": "SUI", "Chainlink": "LINK",
}

// 預設監控的以太坊交易所熱錢包(公開標記的地址)。
var naDefaultWallets = map[string]string{
	"0x28c6c06298d514db089934071355e5743bf21d60": "Binance 14",
	"0xbe0eb53f46cd790cd13851d5eff43d12404d33e8": "Binance 7",
	"0x71660c4005ba85c37ccec55d0c4493e66fe775d3": "Coinbase 1",
	"0x503828976d22510aad0201ac7ec88293211d23da": "Coinbase 2",
	"0x6cc5f688a315f3dc28a7781717a9a798a59fda7b": "OKX",
	"0xf89d7b9c864f589bbf53a82105107622b35eaa40": "Bybit",
}

// Polymarket 問題裡的名字 → 幣種。
var naPolyNames = map[string]string{"bitcoin": "BTC", "btc": "BTC", "ethereum": "ETH", "eth ": "ETH", "solana": "SOL",
	"xrp": "XRP", "dogecoin": "DOGE", "doge": "DOGE", "cardano": "ADA", "hyperliquid": "HYPE", "bnb": "BNB"}

var naStables = map[string]bool{"USDT": true, "USDC": true, "DAI": true, "FDUSD": true, "TUSD": true, "USDE": true, "PYUSD": true, "USDS": true, "BUSD": true}

const (
	naWhaleUSD       = 1_000_000  // 非穩定幣大額門檻
	naStableUSD      = 10_000_000 // 穩定幣大額門檻
	naPremJump       = 5.0        // 單幣溢價 10 分鐘內跳升 ≥ 5 個百分點才發事件
	naPremMin        = 8.0        // 且溢價本身 ≥ 8%
	naPolyMove       = 0.10       // Polymarket 1 小時內機率變動 ≥ 10 個百分點
	naHintWarmup     = 2 * time.Hour
	naChainMaxRange  = 10 // eth_getLogs 每次最多查幾個區塊(免費額度常見上限)
	naChainMaxBehind = 60 // 落後超過這麼多區塊就直接跳到最新(重啟 / 斷線後不補爬)
)

// naExtras 是計算型來源的狀態。
type naExtras struct {
	mu sync.Mutex
	// 韓國溢價
	prem     []NewsPremium
	premBTC  float64
	premAvg  float64
	premAt   time.Time
	premHist map[string][]naPt // 幣 → 最近的溢價(10 分鐘)
	// Polymarket
	poly     []NewsPoly
	polyHist map[string][]naPt // 市場 id → 最近 1 小時的「是」機率
	// 鏈上
	chainBlock uint64
	chainHead  uint64
	tokMeta    map[string]naTok
	tokSeen    map[string]bool // 曾轉進交易所錢包的代幣合約
	chainStart time.Time
}

type naPt struct {
	t int64
	v float64
}

type naTok struct {
	sym string
	dec int
	ok  bool
}

// NewsPremium 是一個幣的韓國溢價(Upbit 韓元價換成美元 vs 幣安)。
type NewsPremium struct {
	Coin    string  `json:"coin"`
	Premium float64 `json:"premium"` // %
	KRW     float64 `json:"krw"`
	USD     float64 `json:"usd"` // 幣安價
}

// NewsPoly 是 Polymarket 一個市場的機率與 1 小時變化。
type NewsPoly struct {
	ID       string  `json:"id"`
	Question string  `json:"question"`
	URL      string  `json:"url"`
	Yes      float64 `json:"yes"`   // 0..1
	Chg1h    float64 `json:"chg1h"` // 1 小時變化(pt,0..1)
	Vol24h   float64 `json:"vol24h"`
}

func newNAExtras() naExtras {
	return naExtras{premHist: map[string][]naPt{}, polyHist: map[string][]naPt{}, tokMeta: map[string]naTok{}, tokSeen: map[string]bool{}}
}

// initNewsAlpha 登記所有來源(NewStore 呼叫)。
func (s *Store) initNewsAlpha() {
	n := newNewsAlpha()
	s.news = n
	wrap := func(f func() ([]newsfeed.Item, error)) func() ([]naIn, error) {
		return func() ([]naIn, error) {
			items, err := f()
			out := make([]naIn, len(items))
			for i, it := range items {
				out[i] = naIn{Item: it}
			}
			return out, err
		}
	}
	nf := n.nf
	// 優先 1:交易所公告
	n.register(&naSource{key: "binance", label: "幣安公告", group: "交易所", every: 20 * time.Second, seed: true, fetch: wrap(nf.Binance)})
	n.register(&naSource{key: "okx", label: "OKX 公告", group: "交易所", every: 30 * time.Second, seed: true, fetch: wrap(nf.OKX)})
	n.register(&naSource{key: "bybit", label: "Bybit 公告", group: "交易所", every: 30 * time.Second, seed: true, fetch: wrap(nf.Bybit)})
	n.register(&naSource{key: "bithumb", label: "Bithumb 公告", group: "交易所", every: 45 * time.Second, seed: true, fetch: wrap(nf.Bithumb)})
	n.register(&naSource{key: "upbit", label: "Upbit 公告", group: "交易所", note: "沿用 Upbit 公告排程"})
	n.register(&naSource{key: "robinhood", label: "Robinhood 上架", group: "交易所", note: "沿用 Robinhood 排程"})
	// 優先 2:鏈上
	if ep := naRPC(); ep != "" {
		n.register(&naSource{key: "onchain", label: "鏈上交易所錢包", group: "鏈上", every: 30 * time.Second, fetch: func() ([]naIn, error) { return s.naChain(ep) }})
	} else {
		n.register(&naSource{key: "onchain", label: "鏈上交易所錢包", group: "鏈上", note: "未設定 ALCHEMY_URL 或 INFURA_URL(以太坊節點網址,Alchemy / Infura 免費註冊即可取得),設定後重啟才會啟用"})
	}
	// 優先 3:Telegram 公開頻道
	tg := os.Getenv("NEWS_TG_CHANNELS")
	if strings.TrimSpace(tg) == "" {
		tg = naDefaultTG
	}
	for i, ch := range strings.Split(tg, ",") {
		name, lang, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(ch, "@")), ":")
		if name == "" {
			continue
		}
		if lang == "" {
			lang = "en"
		}
		key := "tg:" + name
		n.register(&naSource{key: key, label: "TG " + name, group: "Telegram", every: 60 * time.Second, seed: true,
			fetch: wrap(func() ([]newsfeed.Item, error) { return nf.Telegram(name, lang) })})
		n.nextRun[key] = time.Now().Add(time.Duration(i*7) * time.Second) // 錯開,不要同一秒全打
	}
	// 優先 4:GitHub / Snapshot / SEC
	repos := naRepos()
	token := os.Getenv("GITHUB_TOKEN")
	ghEvery := 30 * time.Minute
	if token != "" {
		ghEvery = 10 * time.Minute
	}
	n.register(&naSource{key: "github", label: "GitHub 新版本", group: "開發 / 治理 / 監管", every: ghEvery, seed: true,
		note: fmt.Sprintf("%d 個 repo", len(repos)), fetch: func() ([]naIn, error) {
			return naMulti(repos, func(repo, coin string) ([]newsfeed.Item, error) { return nf.GitHubRelease(repo, coin, token) })
		}})
	n.register(&naSource{key: "snapshot", label: "Snapshot 治理", group: "開發 / 治理 / 監管", every: 5 * time.Minute, seed: true,
		note: fmt.Sprintf("%d 個 space", len(naSnapshotSpaces)), fetch: wrap(func() ([]newsfeed.Item, error) { return nf.Snapshot(naSnapshotSpaces) })})
	ua := os.Getenv("SEC_UA")
	if ua == "" {
		ua = "DataHunters research contact@datahunters.app"
	}
	n.register(&naSource{key: "sec", label: "SEC EDGAR", group: "開發 / 治理 / 監管", every: 30 * time.Minute, seed: true,
		fetch: func() ([]naIn, error) {
			return naMulti(naSECQueries, func(q, coin string) ([]newsfeed.Item, error) {
				time.Sleep(300 * time.Millisecond) // SEC 每秒 10 次上限,保守一點
				return nf.SEC(q, coin, ua)
			})
		}})
	// 優先 5:預測市場 / 韓國溢價
	n.register(&naSource{key: "polymarket", label: "Polymarket", group: "領先指標", every: 5 * time.Minute, fetch: s.naPoly})
	n.register(&naSource{key: "premium", label: "韓國溢價", group: "領先指標", every: 60 * time.Second, fetch: s.naPremium})
	// 優先 6:可預期事件 + 新聞
	n.register(&naSource{key: "unlock", label: "代幣解鎖", group: "可預期事件", every: 30 * time.Minute, fetch: s.naUnlocks, note: "取自解鎖面板"})
	n.register(&naSource{key: "macro", label: "總經數據", group: "可預期事件", every: 2 * time.Minute, fetch: s.naMacro, note: "取自經濟日曆"})
	n.register(&naSource{key: "rss", label: "新聞 RSS", group: "新聞", note: "沿用新聞快訊排程"})
}

func naRPC() string {
	if v := strings.TrimSpace(os.Getenv("ALCHEMY_URL")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("INFURA_URL"))
}

func naRepos() map[string]string {
	env := strings.TrimSpace(os.Getenv("NEWS_GH_REPOS"))
	if env == "" {
		return naDefaultRepos
	}
	out := map[string]string{}
	for _, p := range strings.Split(env, ",") {
		repo, coin, ok := strings.Cut(strings.TrimSpace(p), ":")
		if ok && repo != "" && coin != "" {
			out[repo] = strings.ToUpper(coin)
		}
	}
	return out
}

// naMulti 依序抓多個子來源(repo / 關鍵字),全失敗才算失敗。
func naMulti(m map[string]string, f func(k, coin string) ([]newsfeed.Item, error)) ([]naIn, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []naIn
	var lastErr error
	okN := 0
	for _, k := range keys {
		items, err := f(k, m[k])
		if err != nil {
			lastErr = err
			continue
		}
		okN++
		for _, it := range items {
			out = append(out, naIn{Item: it})
		}
	}
	if okN == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// ── 既有排程的掛勾 ──

// naUpbitHook:UpbitTick 的新公告(已過開機基準)。zh 為已翻好的中文(可空)。
func (s *Store) naUpbitHook(fresh []upbit.Notice, zh map[int]string) {
	if s.news == nil {
		return
	}
	items := make([]naIn, 0, len(fresh))
	for _, nt := range fresh {
		it := newsfeed.Item{Source: "upbit", ID: fmt.Sprint(nt.ID), Title: nt.Title, URL: nt.URL(), Lang: "ko"}
		if t, err := time.Parse(time.RFC3339, nt.ListedAt); err == nil {
			it.Published = t.UTC()
		}
		items = append(items, naIn{Item: it, zh: zh[nt.ID]})
	}
	s.naHook("upbit", items)
}

// naRobinhoodHook:Robinhood 新上架的幣。
func (s *Store) naRobinhoodHook(fresh []robinhood.Coin) {
	if s.news == nil {
		return
	}
	items := make([]naIn, 0, len(fresh))
	for _, c := range fresh {
		t := fmt.Sprintf("Robinhood 上架 %s(%s)", c.Name, c.Code)
		items = append(items, naIn{Item: newsfeed.Item{Source: "robinhood", ID: c.Code, Title: t, Lang: "zh",
			URL: "https://robinhood.com/crypto/" + c.Code, Coins: []string{strings.ToUpper(c.Code)}, Category: "listing_us"}, zh: t})
	}
	s.naHook("robinhood", items)
}

// naRSSHook:新聞快訊的新文章(標題已翻成中文的一併帶入)。
func (s *Store) naRSSHook(arts []gdelt.Article, zh []string) {
	if s.news == nil {
		return
	}
	items := make([]naIn, 0, len(arts))
	for i, a := range arts {
		lang := "en"
		if a.Zh {
			lang = "zh"
		}
		it := newsfeed.Item{Source: "rss", ID: a.URL, Title: a.Title, URL: a.URL, Lang: lang}
		if t, err := time.Parse(time.RFC3339, gdelt.ParseTime(a.SeenDate)); err == nil {
			it.Published = t
		}
		in := naIn{Item: it}
		if i < len(zh) && zh[i] != a.Title {
			in.zh = zh[i]
		}
		items = append(items, in)
	}
	s.naHook("rss", items)
}

// ── 韓國溢價 ──

// naPremium 算 Upbit 每個韓元市場幣的溢價(KRW 價 ÷ USDT 韓元價 ÷ 幣安價 − 1)。
// 某幣溢價在 10 分鐘內跳升 ≥ 5pt 且本身 ≥ 8% → 發事件(韓國散戶在搶,常是行情前兆)。
func (s *Store) naPremium() ([]naIn, error) {
	krw, err := s.news.nf.UpbitKRWPrices()
	if err != nil {
		return nil, err
	}
	s.naPrices(0)
	n := s.news
	fx := krw["USDT"]
	now := time.Now()
	var rows []NewsPremium
	for coin, k := range krw {
		if coin == "USDT" || coin == "USDC" || k <= 0 {
			continue
		}
		usd, _ := n.price(coin)
		if usd <= 0 {
			continue
		}
		p := (k/fx/usd - 1) * 100
		if math.Abs(p) > 80 { // 同代號不同幣(或 1000 倍合約)會算出離譜的數字 → 丟掉
			continue
		}
		rows = append(rows, NewsPremium{Coin: coin, Premium: round2(p), KRW: k, USD: usd})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("沒有可比對的幣")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Premium > rows[j].Premium })
	var sum float64
	for _, r := range rows {
		sum += r.Premium
	}
	x := &n.ext
	var out []naIn
	x.mu.Lock()
	x.prem, x.premAt, x.premAvg = rows, now, round2(sum/float64(len(rows)))
	for _, r := range rows {
		if r.Coin == "BTC" {
			x.premBTC = r.Premium
		}
		h := append(x.premHist[r.Coin], naPt{now.UnixMilli(), r.Premium})
		for len(h) > 0 && now.UnixMilli()-h[0].t > 10*60_000 {
			h = h[1:]
		}
		x.premHist[r.Coin] = h
		if len(h) < 3 || r.Premium < naPremMin {
			continue
		}
		low := h[0].v
		for _, p := range h {
			low = math.Min(low, p.v)
		}
		if r.Premium-low >= naPremJump {
			id := fmt.Sprintf("%s|%d", r.Coin, now.Unix()/3600) // 同一幣一小時最多一則
			t := fmt.Sprintf("%s 韓國溢價 10 分鐘內由 %.1f%% 升到 %.1f%%(Upbit 韓元價明顯高於幣安)", r.Coin, low, r.Premium)
			out = append(out, naIn{Item: newsfeed.Item{Source: "premium", ID: id, Title: t, Lang: "zh", Category: "premium",
				Coins: []string{r.Coin}, Value: r.Premium, URL: "https://upbit.com/exchange?code=CRIX.UPBIT.KRW-" + r.Coin}, zh: t})
		}
	}
	x.mu.Unlock()
	return out, nil
}

// ── Polymarket ──

// naPoly 抓成交量前 40 的加密類市場;1 小時內「是」的機率變動 ≥ 10pt → 發事件。
func (s *Store) naPoly() ([]naIn, error) {
	ms, err := s.news.nf.Polymarket(40)
	if err != nil {
		return nil, err
	}
	x := &s.news.ext
	now := time.Now().UnixMilli()
	var out []naIn
	var rows []NewsPoly
	x.mu.Lock()
	live := map[string]bool{}
	for _, m := range ms {
		live[m.ID] = true
		h := append(x.polyHist[m.ID], naPt{now, m.Yes})
		for len(h) > 1 && now-h[0].t > 65*60_000 {
			h = h[1:]
		}
		x.polyHist[m.ID] = h
		chg := m.Yes - h[0].v
		url := "https://polymarket.com/event/" + m.Slug
		rows = append(rows, NewsPoly{ID: m.ID, Question: m.Question, URL: url, Yes: m.Yes, Chg1h: chg, Vol24h: m.Vol24h})
		if math.Abs(chg) < naPolyMove || now-h[0].t < 20*60_000 {
			continue
		}
		var coins []string
		q := strings.ToLower(m.Question) + " "
		for name, c := range naPolyNames {
			if strings.Contains(q, name) {
				coins = append(coins, c)
				break
			}
		}
		t := fmt.Sprintf("Polymarket「%s」機率 1 小時內 %+.0f 個百分點,現在 %.0f%%", m.Question, chg*100, m.Yes*100)
		out = append(out, naIn{Item: newsfeed.Item{Source: "polymarket", ID: fmt.Sprintf("%s|%d", m.ID, now/3600_000), Title: t,
			Lang: "zh", URL: url, Category: "prediction", Coins: coins, Value: round2(chg * 100)}, zh: t})
	}
	for id := range x.polyHist { // 掉出前 40 的市場不再追
		if !live[id] {
			delete(x.polyHist, id)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return math.Abs(rows[i].Chg1h) > math.Abs(rows[j].Chg1h) })
	x.poly = rows
	x.mu.Unlock()
	return out, nil
}

// ── 解鎖 / 總經(既有面板的資料轉成事件)──

// naUnlocks:24 小時內有大額解鎖(單日 ≥ 0.5% 最大供給)的幣 → 事件(偏空,只記錄)。
func (s *Store) naUnlocks() ([]naIn, error) {
	rows := s.Unlocks().Rows
	if len(rows) == 0 {
		return nil, fmt.Errorf("解鎖面板還沒有資料")
	}
	now := time.Now()
	var out []naIn
	for _, r := range rows {
		t, err := time.Parse(time.RFC3339, r.PeakDate)
		if err != nil || t.Before(now) || t.Sub(now) > 24*time.Hour || r.PeakPctMax < 0.5 {
			continue
		}
		title := fmt.Sprintf("%s 將於 %s 解鎖約 %.2f%% 最大供給(30 天內合計約 $%s)", r.Coin, t.Local().Format("01/02 15:04"), r.PeakPctMax, fmtUSD(r.USD30))
		out = append(out, naIn{Item: newsfeed.Item{Source: "unlock", ID: r.Coin + "|" + r.PeakDate, Title: title, Lang: "zh",
			Category: "unlock", Coins: []string{r.Coin}, Value: r.PeakPctMax}, zh: title})
	}
	return out, nil
}

// naMacro:剛公布(30 分鐘內)的總經數據 → 事件(對 BTC,只記錄、看反應)。
func (s *Store) naMacro() ([]naIn, error) {
	now := time.Now()
	var out []naIn
	for _, e := range s.Events() {
		if !e.Released || e.Time.IsZero() || now.Sub(e.Time) > 30*time.Minute || now.Before(e.Time) {
			continue
		}
		title := fmt.Sprintf("%s 公布:實際 %s / 預期 %s / 前值 %s", e.Title, naDash(e.Actual), naDash(e.Forecast), naDash(e.Previous))
		out = append(out, naIn{Item: newsfeed.Item{Source: "macro", ID: e.Title + "|" + e.Time.UTC().Format(time.RFC3339), Title: title,
			Lang: "zh", Category: "macro", Coins: []string{"BTC"}, Published: e.Time}, zh: title})
	}
	return out, nil
}

func naDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func fmtUSD(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.0fK", v/1e3)
	}
	return fmt.Sprintf("%.0f", v)
}

// ── 鏈上(以太坊交易所熱錢包)──

func naWallets() map[string]string {
	env := strings.TrimSpace(os.Getenv("NEWS_WALLETS"))
	if env == "" {
		return naDefaultWallets
	}
	out := map[string]string{}
	for _, p := range strings.Split(env, ",") {
		name, addr, ok := strings.Cut(strings.TrimSpace(p), ":")
		if ok && strings.HasPrefix(addr, "0x") {
			out[strings.ToLower(addr)] = name
		}
	}
	return out
}

// naChain 每 30 秒掃新區塊裡「轉進 / 轉出交易所熱錢包」的 ERC-20:
//   - 非穩定幣 ≥ $1M 轉入 → 可能要賣(偏空);轉出 → 囤幣(偏多)
//   - 穩定幣 ≥ $10M 轉入 → 準備買(對 BTC 偏多)
//   - 幣安沒有行情的代幣「第一次」轉進交易所錢包 → 上幣前測試轉帳的可能(上幣前兆)
func (s *Store) naChain(ep string) ([]naIn, error) {
	n := s.news
	nf := n.nf
	x := &n.ext
	head, err := nf.BlockNumber(ep)
	if err != nil {
		return nil, err
	}
	x.mu.Lock()
	if x.chainStart.IsZero() {
		x.chainStart = time.Now()
	}
	from := x.chainBlock + 1
	if x.chainBlock == 0 || head-x.chainBlock > naChainMaxBehind {
		from = head // 第一次 / 落後太多:從最新開始
	}
	x.chainHead = head
	warm := time.Since(x.chainStart) > naHintWarmup
	x.mu.Unlock()
	if from > head {
		return nil, nil
	}
	to := min(head, from+naChainMaxRange-1)
	wm := naWallets()
	wallets := make([]string, 0, len(wm))
	for a := range wm {
		wallets = append(wallets, a)
	}
	in, err := nf.TransferLogs(ep, from, to, wallets, true)
	if err != nil {
		return nil, err
	}
	outL, err := nf.TransferLogs(ep, from, to, wallets, false)
	if err != nil {
		return nil, err
	}
	s.naPrices(0)
	var out []naIn
	metaCalls := 0
	handle := func(l newsfeed.ChainLog, into bool) {
		if wm[l.From] != "" && wm[l.To] != "" {
			return // 交易所自己錢包之間互轉
		}
		x.mu.Lock()
		meta, have := x.tokMeta[l.Token]
		x.mu.Unlock()
		if !have {
			if metaCalls >= 10 { // 每輪最多查 10 個新代幣的 symbol/decimals
				return
			}
			metaCalls++
			sym, dec, err := nf.TokenMeta(ep, l.Token)
			meta = naTok{sym: sym, dec: dec, ok: err == nil && sym != ""}
			x.mu.Lock()
			x.tokMeta[l.Token] = meta
			x.mu.Unlock()
		}
		if !meta.ok {
			return
		}
		amt := newsfeed.TokenAmount(l.Amount, meta.dec)
		sym := meta.sym
		if sym == "WETH" || sym == "WBTC" { // 包裝幣用原幣的價格
			sym = sym[1:]
		}
		px, _ := n.price(sym)
		if naStables[meta.sym] {
			px = 1
		}
		ex := wm[l.To]
		if !into {
			ex = wm[l.From]
		}
		if px <= 0 { // 幣安沒有行情的代幣:只關心「第一次轉進交易所」
			if !into {
				return
			}
			x.mu.Lock()
			first := !x.tokSeen[l.Token]
			x.tokSeen[l.Token] = true
			x.mu.Unlock()
			if first && warm {
				t := fmt.Sprintf("幣安尚無行情的代幣 %s 首次轉入 %s 熱錢包(可能是上幣前的測試轉帳)", meta.sym, ex)
				out = append(out, naIn{Item: newsfeed.Item{Source: "onchain", ID: "hint|" + l.Token, Title: t, Lang: "zh",
					URL: "https://etherscan.io/tx/" + l.Tx, Category: "listing_hint", Coins: []string{meta.sym}}, zh: t})
			}
			return
		}
		usd := amt * px
		stable := naStables[meta.sym]
		if (stable && usd < naStableUSD) || (!stable && usd < naWhaleUSD) {
			return
		}
		if stable && !into {
			return // 穩定幣轉出交易所沒有明確方向
		}
		verb, val := "轉入", usd
		if !into {
			verb, val = "轉出", -usd
		}
		coin := sym
		if stable {
			coin = "BTC" // 穩定幣大額進交易所 = 準備買,以 BTC 代表大盤
		}
		t := fmt.Sprintf("鏈上:%s %s(約 $%s)%s %s", fmtAmt(amt), meta.sym, fmtUSD(usd), verb, ex)
		out = append(out, naIn{Item: newsfeed.Item{Source: "onchain", ID: l.Tx + "|" + l.Token, Title: t, Lang: "zh",
			URL: "https://etherscan.io/tx/" + l.Tx, Category: "whale", Coins: []string{coin}, Value: val}, zh: t})
	}
	for _, l := range in {
		handle(l, true)
	}
	for _, l := range outL {
		handle(l, false)
	}
	x.mu.Lock()
	x.chainBlock = to
	x.mu.Unlock()
	return out, nil
}

func fmtAmt(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%.2fB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fK", v/1e3)
	}
	return fmt.Sprintf("%.2f", v)
}
