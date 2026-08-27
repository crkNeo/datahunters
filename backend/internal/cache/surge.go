package cache

import (
	"log"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
)

// surge.go — 「爆量脈搏」斥候(方向①+②的第一段)。
//
// 目的:現有雷達/策略只掃 24h 成交額 top-80,實測顯示 92~100% 的「靜默後暴漲」幣
// 在起漲當下排在 80 名外、完全看不到。這裡改用「相對自身基線的爆量」偵測——問的不是
// 「它量大不大」,而是「它是不是突然醒了」。
//
// 成本:全 ~700 檔的資料來自 Refresh() 本來就會抓的 all-tickers(一次 call),這裡只做
// 純記憶體運算,零額外 REST。每 2 分鐘(Refresh 週期)更新一次。
//
// 指標:24h quoteVolume 是滾動加總,直接看絕對值反應慢;改看「近窗成交量脈搏」——
// 相鄰快照的差 ≈ 這段期間新增的成交量,對死盤幣 24h 前滾掉的量近乎恆定可忽略,所以
// 脈搏在幣一醒就跳。surgeRatio = 近窗脈搏 / 該幣自身脈搏基線(中位)。
//
// 這只是「感測器」——負責提名可疑對象,不負責定罪。真正的品質閘門(OI/CVD)在下游。

const (
	surgeRing     = 240             // 每檔保留的快照數(2min×240 ≈ 8h 基線)
	surgeWin      = 15              // 脈搏窗:15 個 cycle ≈ 30min 的近窗成交量
	surgeMinHist  = 30             // 至少 30 個 cycle(~1h)才開始評分;啟動時由 K 線暖機直接補滿
	surgeVolFloor = 1_000_000.0     // 24h 成交額地板:低於此不評分(擋殭屍幣一筆單假爆量)
	surgePulseMin = 150_000.0       // 近窗脈搏絕對地板(USDT):過小的脈搏不算數
	surgeHotRatio = 3.0             // 進「熱名單」(供脈衝星策略)的爆量倍數門檻
	surgeHotK     = 40              // 熱名單上限
	surgeBoardMax = 60              // 面板最多顯示幾列
)

// surgeSnap is one all-tickers observation for a coin.
type surgeSnap struct {
	ts    int64
	qv    float64 // 24h quoteVolume
	price float64
	cnt   int64 // 24h trade count
}

// SurgeRow is one row on the 爆量脈搏 admin panel.
type SurgeRow struct {
	Coin      string  `json:"coin"`
	Price     float64 `json:"price"`
	Chg24     float64 `json:"chg24"`      // 24h 漲跌%
	SurgeX    float64 `json:"surge_x"`    // 爆量倍數(近窗脈搏 / 自身基線)
	Pulse30m  float64 `json:"pulse_30m"`  // 近 ~30min 成交量脈搏(USDT)
	Vol24h    float64 `json:"vol_24h"`    // 24h 成交額
	VolRank   int     `json:"vol_rank"`   // 絕對成交額排名(1=最大)
	Top80Out  bool    `json:"top80_out"`  // 是否排在 top-80 之外(現有雷達的盲區)
	CntX      float64 `json:"cnt_x"`      // 成交筆數爆增倍數(抗洗量的輔助訊號)
	Hot       bool    `json:"hot"`        // 是否已達脈衝星熱名單門檻
}

// surgeEngine holds per-coin snapshot rings + the latest computed board.
type surgeEngine struct {
	mu    sync.Mutex
	hist  map[string][]surgeSnap
	board []SurgeRow
	hot   []string
	upd   time.Time
}

func newSurgeEngine() *surgeEngine {
	return &surgeEngine{hist: make(map[string][]surgeSnap)}
}

// pulse returns the recent-window turnover (qv_now - qv_{win ago}) and this coin's
// baseline pulse (median of the per-window turnover over the ring). ok=false if
// there isn't enough clean history yet.
func pulseAndBaseline(h []surgeSnap, win int) (recent, base float64, ok bool) {
	n := len(h)
	if n < surgeMinHist || n <= win {
		return 0, 0, false
	}
	recent = h[n-1].qv - h[n-1-win].qv
	if recent < 0 {
		recent = 0 // 24h 加總偶有回檔(交易所修正),夾成 0
	}
	// 逐窗脈搏序列 → 取中位當基線
	deltas := make([]float64, 0, n-win)
	for i := win; i < n; i++ {
		d := h[i].qv - h[i-win].qv
		if d < 0 {
			d = 0
		}
		deltas = append(deltas, d)
	}
	if len(deltas) == 0 {
		return 0, 0, false
	}
	sort.Float64s(deltas)
	base = deltas[len(deltas)/2]
	return recent, base, true
}

// countSurge returns the 24h trade-count ratio vs the coin's own baseline count.
func countSurge(h []surgeSnap) float64 {
	n := len(h)
	if n < surgeMinHist {
		return 0
	}
	cnts := make([]float64, 0, n)
	for _, s := range h {
		cnts = append(cnts, float64(s.cnt))
	}
	sort.Float64s(cnts)
	base := cnts[len(cnts)/2]
	if base <= 0 {
		return 0
	}
	return float64(h[n-1].cnt) / base
}

// scan ingests one all-tickers batch: append snapshots, prune rings, recompute the
// board + hot set. Called from Refresh (already holds the tickers). Zero extra REST.
func (e *surgeEngine) scan(tickers []exchange.MarketTicker, now time.Time) {
	ts := now.UnixMilli()
	// absolute-volume rank (for the 盲區 column)
	sort.Slice(tickers, func(i, j int) bool { return tickers[i].QuoteVol > tickers[j].QuoteVol })
	rank := make(map[string]int, len(tickers))
	for i, t := range tickers {
		rank[coinOf(t.Symbol)] = i + 1
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// 1) ingest
	for _, t := range tickers {
		coin := coinOf(t.Symbol)
		if stableLike[coin] {
			continue
		}
		h := append(e.hist[coin], surgeSnap{ts: ts, qv: t.QuoteVol, price: t.Price, cnt: t.Count})
		if len(h) > surgeRing {
			h = h[len(h)-surgeRing:]
		}
		e.hist[coin] = h
	}

	// 2) score every coin
	rows := make([]SurgeRow, 0, len(e.hist))
	for _, t := range tickers {
		coin := coinOf(t.Symbol)
		if stableLike[coin] || t.QuoteVol < surgeVolFloor {
			continue
		}
		h := e.hist[coin]
		recent, base, ok := pulseAndBaseline(h, surgeWin)
		if !ok || base <= 0 || recent < surgePulseMin {
			continue
		}
		x := recent / base
		if x < 1.2 { // 只留有醒意的
			continue
		}
		r := rank[coin]
		rows = append(rows, SurgeRow{
			Coin:     coin,
			Price:    t.Price,
			Chg24:    t.ChgPct,
			SurgeX:   round2(x),
			Pulse30m: recent,
			Vol24h:   t.QuoteVol,
			VolRank:  r,
			Top80Out: r > emaTopN,
			CntX:     round2(countSurge(h)),
			Hot:      x >= surgeHotRatio,
		})
	}
	// 3) rank by surge, cut board + hot set
	sort.Slice(rows, func(i, j int) bool { return rows[i].SurgeX > rows[j].SurgeX })
	hot := make([]string, 0, surgeHotK)
	for _, r := range rows {
		if r.Hot && len(hot) < surgeHotK {
			hot = append(hot, r.Coin)
		}
	}
	if len(rows) > surgeBoardMax {
		rows = rows[:surgeBoardMax]
	}
	e.board = rows
	e.hot = hot
	e.upd = now
}

// SurgeBoard returns the latest 爆量脈搏 board (admin panel).
func (s *Store) SurgeBoard() []SurgeRow {
	if s.surge == nil {
		return nil
	}
	s.surge.mu.Lock()
	defer s.surge.mu.Unlock()
	out := make([]SurgeRow, len(s.surge.board))
	copy(out, s.surge.board)
	return out
}

// ---- 脈衝星v2 品質閘門(OI/CVD)----

const (
	pulsarCVDWin = 12 // CVD 近窗:15m × 12 = 近 3 小時的淨主買佔比
	pulsarCVDMin = 0  // CVD 佔比門檻(%):> 0 = 買方主導
	pulsarOIMin  = 0  // 近 1h 名目 OI 變化門檻(%):> 0 = OI 擴張(新多進場)
)

// oiCvdGate is 脈衝星v2 的品質閘門。只放行「有真實需求」的爆量:CVD 為正(積極買盤主導,
// 不是被動掛單)且 OI 上升(新多進場,不是空頭回補)。對應 collector detectB 的哲學 ——
// 量價齊升且 OI 擴張才是有底的行情;純槓桿/軋空的假爆量會被擋掉。
//
// CVD 直接由該幣的 K 線(主買量)算出,不需額外抓取;OI 用快取 5 分的 1h 名目 OI 變化。
// 缺 OI 資料時保守擋掉(v2 是嚴格版,要有憑證才進)。綁定 s 後掛在 microBook.gate。
func (s *Store) oiCvdGate(coin string, cs []exchange.Candle) bool {
	if indicator.CVDFromKlines(cs, pulsarCVDWin) <= pulsarCVDMin {
		return false
	}
	oiHist := s.oiHist1h(coin, 2)
	if len(oiHist) < 2 {
		return false // 沒 OI 憑證 → 擋
	}
	return indicator.PctChange(oiHist[0].SumOIValue, oiHist[len(oiHist)-1].SumOIValue) > pulsarOIMin
}

// ---- 啟動暖機:用歷史 1m K 線重建基線,讓面板一開機就有資料 ----

// mergeSeed injects reconstructed historical snapshots as the base of a coin's
// ring, keeping any live snapshots already appended that are newer than the seed.
// Held briefly per-coin so warmup fetches never block the live scan.
func (e *surgeEngine) mergeSeed(coin string, seed []surgeSnap) {
	if len(seed) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	lastSeedTs := seed[len(seed)-1].ts
	merged := seed
	for _, s := range e.hist[coin] { // 保留暖機期間已進來的即時點(較新的)
		if s.ts > lastSeedTs {
			merged = append(merged, s)
		}
	}
	if len(merged) > surgeRing {
		merged = merged[len(merged)-surgeRing:]
	}
	e.hist[coin] = merged
}

// reconstructSurgeSeed rebuilds ~surgeMinHist snapshots at 2-min spacing from 1m
// klines. Each snapshot's qv is the 24h-rolling quote volume ending at that bar —
// exactly what all-tickers reports live, so the seam with live scans is smooth.
func reconstructSurgeSeed(ks []exchange.Candle, points int) []surgeSnap {
	const day = 1440 // 1m bars in 24h
	const step = 2   // 2min ring cadence (Refresh 週期)
	n := len(ks)
	if n < day+(points-1)*step+1 {
		return nil // 不夠 24h + ring,交給即時暖機
	}
	pqv := make([]float64, n+1)
	pcnt := make([]float64, n+1)
	for i, k := range ks {
		pqv[i+1] = pqv[i] + k.QuoteVol
		pcnt[i+1] = pcnt[i] + k.Trades
	}
	out := make([]surgeSnap, 0, points)
	for p := 0; p < points; p++ {
		j := n - 1 - p*step
		lo := j + 1 - day
		if lo < 0 {
			break
		}
		out = append(out, surgeSnap{
			ts: ks[j].Ts, price: ks[j].Close,
			qv: pqv[j+1] - pqv[lo], cnt: int64(pcnt[j+1] - pcnt[lo]),
		})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 { // 反轉成 ts 升冪
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// WarmSurge seeds every candidate coin's baseline from 1m klines so the 爆量脈搏
// board works within minutes of startup instead of waiting ~1h. Fetches concurrently
// (bounded), merges per-coin without blocking the live scan. Safe to run once at boot.
func (s *Store) WarmSurge() {
	tickers, err := s.ex.BinanceAllTickers()
	if err != nil {
		log.Printf("surge: 暖機取得 tickers 失敗: %v", err)
		return
	}
	t0 := time.Now()
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	var seeded int64
	var mu sync.Mutex
	for _, t := range tickers {
		coin := coinOf(t.Symbol)
		if stableLike[coin] || t.QuoteVol < surgeVolFloor {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(coin string) {
			defer wg.Done()
			defer func() { <-sem }()
			ks, err := s.ex.BinanceKlines(coin+"USDT", "1m", 1500)
			if err != nil {
				return
			}
			seed := reconstructSurgeSeed(ks, surgeMinHist)
			if len(seed) < surgeMinHist {
				return
			}
			s.surge.mergeSeed(coin, seed)
			mu.Lock()
			seeded++
			mu.Unlock()
		}(coin)
	}
	wg.Wait()
	log.Printf("surge: 暖機完成,%d 檔已建基線,耗時 %s", seeded, time.Since(t0).Round(time.Second))
}

// surgeHotCoins is the strategy universe (爆量熱名單) — coins currently above the
// hot threshold, for the 脈衝星 book to evaluate. Falls back to empty (not top-80),
// so the strategy is silent until real surges appear.
func (s *Store) surgeHotCoins() []string {
	if s.surge == nil {
		return nil
	}
	s.surge.mu.Lock()
	defer s.surge.mu.Unlock()
	out := make([]string, len(s.surge.hot))
	copy(out, s.surge.hot)
	return out
}
