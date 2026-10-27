package cache

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// ema200.go — EMA200 穿越監控(VIP,純提示、不下單)。
//
// 掃描爆量脈搏全市場裡成交額前 200 名(surgeUniverse 前 radarPoolMax 檔,與爆發雷達同一批),看 4H 收盤價
// 與 4H EMA200 的關係:最新一根已收的 4H 收盤從下方站上 EMA200 = 上穿,從上方跌破 = 下穿,第一次
// 偵測到時推播(使用者可在帳號頁開關「EMA200 穿越」)。4H K 線不夠算 EMA200 的幣(上市太新)跳過。
//
// 成本:每根 4H 收盤後,每個幣抓一次 4H K 線(limit 260,權重 2),每 120ms 一檔 → 200 檔約 40 秒
// 掃完,不會把 Binance 打到限流。同一根 4H 內不重抓。穿越紀錄存 MySQL(ema200_events),頁面分頁讀。
// 現價取爆量脈搏最新快照的價格(全市場都有,不限 WS feed 的幣)。

const (
	e2Period  = 200
	e2MinBars = 230 // EMA 從第一根收盤起算,至少要這麼多根已收 K 才算收斂;上市太新的幣跳過
	e2Limit   = 260
	e2BarMs   = int64(4 * time.Hour / time.Millisecond)
	e2Keep    = 200 // 保留的穿越紀錄筆數
	e2Recent  = 6   // 頁面只列最近 6 根 4H(24 小時)內穿越的幣
	e2Pace    = 120 * time.Millisecond
)

// e2Coin 是某幣在「最新一根已收 4H」時的 EMA200 狀態(快取到下一根 4H 收盤)。
type e2Coin struct {
	bar       int64   // 最新已收 4H 的開盤時戳(ms)
	close     float64 // 該根收盤
	ema       float64 // 該根的 EMA200
	crossBar  int64   // 最近一次穿越發生在哪根(開盤時戳 ms);0 = 資料內沒有穿越
	crossDir  string  // up | down
	barsSince int     // 最近一次穿越距今幾根(0 = 就是最新這根)
}

// EMA200Row 是頁面上的一張卡。
type EMA200Row struct {
	Coin      string  `json:"coin"`
	Price     float64 `json:"price"`      // 現價(爆量脈搏面板)
	EMA       float64 `json:"ema"`        // 4H EMA200(最新已收那根)
	DistPct   float64 `json:"dist_pct"`   // 現價離 EMA200 的 %
	Above     bool    `json:"above"`      // 最新 4H 收盤在 EMA200 之上
	CrossDir  string  `json:"cross_dir"`  // 最近一次穿越方向 up | down | ""
	BarsSince int     `json:"bars_since"` // 最近一次穿越距今幾根 4H(0 = 最新收盤剛穿越)
	CrossTime string  `json:"cross_time"` // 穿越那根 4H 的收盤時間(RFC3339)
}

// EMA200Event 是一筆穿越紀錄(推播時寫入)。
type EMA200Event struct {
	Coin    string  `json:"coin"`
	Dir     string  `json:"dir"`      // up | down
	Close   float64 `json:"close"`    // 4H 收盤價
	EMA     float64 `json:"ema"`      // 當根 EMA200
	BarTime string  `json:"bar_time"` // 4H 收盤時間(RFC3339)
}

// EMA200Data 是 VIP 頁的資料(穿越紀錄另走 EMA200History 分頁)。
type EMA200Data struct {
	Rows      []EMA200Row `json:"rows"`    // 最近 24 小時內穿越的幣
	Scanned   int         `json:"scanned"` // 有算出 EMA200 的幣數
	Above     int         `json:"above"`   // 其中最新 4H 收在 EMA200 之上的幣數
	Below     int         `json:"below"`
	UpdatedAt string      `json:"updated_at"`
}

// e2Universe = 爆量脈搏全市場依成交額排序的前 200 名(與爆發雷達的掃描池同規模)。
func (s *Store) e2Universe() []string {
	u := s.surgeUniverse()
	if len(u) > radarPoolMax {
		u = u[:radarPoolMax]
	}
	return u
}

type ema200Engine struct {
	mu      sync.Mutex
	coins   map[string]e2Coin
	alerted map[string]int64 // coin → 已推播過的穿越 K 棒(同一根只推一次)
	events  []EMA200Event    // 新的在前
	seeded  bool             // 第一輪只建基準、不推播(避免重啟時把當下的穿越全推一遍)
}

func newEMA200Engine() *ema200Engine {
	return &ema200Engine{coins: map[string]e2Coin{}, alerted: map[string]int64{}}
}

// ema200State 由已收的 4H K 線(舊→新)算出最新一根的 EMA200 狀態。資料不足回 ok=false。
func ema200State(cs []exchange.Candle) (st e2Coin, ok bool) {
	n := len(cs)
	if n < e2MinBars {
		return
	}
	e := emaSeries(cs, e2Period)
	st = e2Coin{bar: cs[n-1].Ts, close: cs[n-1].Close, ema: e[n-1]}
	// 由新往舊找最近一次「收盤相對 EMA 換邊」的那根(只看 EMA 已收斂的區段)
	for i := n - 1; i >= e2Period; i-- {
		above, prevAbove := cs[i].Close > e[i], cs[i-1].Close > e[i-1]
		if above != prevAbove {
			st.crossBar, st.barsSince = cs[i].Ts, n-1-i
			st.crossDir = "down"
			if above {
				st.crossDir = "up"
			}
			break
		}
	}
	return st, true
}

// EMA200Tick 每分鐘呼叫:全市場的幣若還沒有「最新已收 4H」的狀態就抓一次 K 線算好,
// 最新那根剛好穿越且還沒推播過就推播。一輪掃完約 2~3 分鐘(ticker 會自動跳過重疊的那幾次)。
func (s *Store) EMA200Tick() {
	if s.ema200 == nil {
		return
	}
	now := time.Now().UnixMilli()
	lastBar := (now/e2BarMs - 1) * e2BarMs // 最新已收 4H 的開盤時戳
	e := s.ema200

	type hit struct {
		coin string
		st   e2Coin
	}
	var hits []hit
	universe := s.e2Universe()
	if len(universe) == 0 {
		return // 爆量脈搏還沒掃到行情(剛開機)→ 先別建基準,否則下一輪會把當下的穿越全推一遍
	}
	for _, coin := range universe {
		e.mu.Lock()
		cur, have := e.coins[coin]
		e.mu.Unlock()
		if have && cur.bar == lastBar {
			continue // 這根 4H 已算過
		}
		cs, err := s.ex.BinanceKlines(coin+"USDT", "4h", e2Limit)
		time.Sleep(e2Pace) // 放慢,700 檔不要一次打爆 Binance 權重
		if err != nil || len(cs) < 2 {
			continue // 下一輪再試
		}
		cs = cs[:len(cs)-1] // 丟掉還在走的那根
		if cs[len(cs)-1].Ts != lastBar {
			continue // 交易所新棒還沒生成 → 下一輪再算
		}
		st, ok := ema200State(cs)
		if !ok { // 上市太新、K 線不夠 → 記下這根已看過,這根 4H 內不再重抓(ema=0 不上頁面)
			e.mu.Lock()
			e.coins[coin] = e2Coin{bar: lastBar}
			e.mu.Unlock()
			continue
		}
		e.mu.Lock()
		e.coins[coin] = st
		if st.crossBar == lastBar && e.alerted[coin] != lastBar {
			e.alerted[coin] = lastBar
			if e.seeded {
				hits = append(hits, hit{coin, st})
			}
		}
		e.mu.Unlock()
	}
	e.mu.Lock()
	e.seeded = true // 第一輪結束後才開始推播
	for _, h := range hits {
		e.events = append([]EMA200Event{{Coin: h.coin, Dir: h.st.crossDir, Close: h.st.close, EMA: h.st.ema,
			BarTime: time.UnixMilli(h.st.bar + e2BarMs).UTC().Format(time.RFC3339)}}, e.events...)
	}
	if len(e.events) > e2Keep {
		e.events = e.events[:e2Keep]
	}
	e.mu.Unlock()

	for _, h := range hits {
		if s.db != nil {
			s.db.insertEMA200Event(h.coin, h.st)
		}
		s.notifyEMA200(h.coin, h.st)
	}
}

// EMA200 回傳 VIP 頁資料:最近 24 小時(6 根 4H)內穿越的幣 + 全市場上方/下方統計 + 穿越紀錄。
// 排序:越近穿越的越前面,同一根的依離 EMA200 的距離由近到遠。
func (s *Store) EMA200() EMA200Data {
	out := EMA200Data{Rows: []EMA200Row{}, UpdatedAt: time.Now().Format(time.RFC3339)}
	if s.ema200 == nil {
		return out
	}
	universe := s.e2Universe()
	px := s.surgePrices(universe)
	e := s.ema200
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, coin := range universe {
		st, ok := e.coins[coin]
		if !ok || st.ema <= 0 {
			continue // 還沒算到,或 K 線不夠算 EMA200
		}
		out.Scanned++
		if st.close > st.ema {
			out.Above++
		} else {
			out.Below++
		}
		if st.crossDir == "" || st.barsSince >= e2Recent {
			continue
		}
		p := px[coin]
		if p <= 0 {
			p = st.close
		}
		out.Rows = append(out.Rows, EMA200Row{Coin: coin, Price: p, EMA: st.ema, DistPct: round2((p - st.ema) / st.ema * 100),
			Above: st.close > st.ema, CrossDir: st.crossDir, BarsSince: st.barsSince,
			CrossTime: time.UnixMilli(st.crossBar + e2BarMs).UTC().Format(time.RFC3339)})
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.BarsSince != b.BarsSince {
			return a.BarsSince < b.BarsSince
		}
		return math.Abs(a.DistPct) < math.Abs(b.DistPct)
	})
	return out
}

// ── 穿越紀錄分頁(與訊號紀錄 / 策略紀錄同一套:時間範圍 + 每頁 50 筆)──

var ema200HistCache = newTTLCache(15 * time.Second)

// EMA200HistResult 是一頁穿越紀錄 + 總數。
type EMA200HistResult struct {
	Rows  []EMA200Event `json:"rows"`
	Total int           `json:"total"`
	Pages int           `json:"pages"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// EMA200History 回一頁穿越紀錄(可帶時間窗,最新在前)。沒有 DB 時退回記憶體裡的紀錄。
func (s *Store) EMA200History(winMs int64, page, size int) EMA200HistResult {
	if size <= 0 || size > 200 {
		size = 50
	}
	var since int64
	if winMs > 0 {
		since = time.Now().UnixMilli() - winMs
	}
	var all []EMA200Event
	if s.db != nil {
		v, _ := ema200HistCache.get(fmt.Sprintf("%d", since), func() (any, error) { return s.db.loadEMA200Events(since), nil })
		all, _ = v.([]EMA200Event)
	} else if s.ema200 != nil {
		s.ema200.mu.Lock()
		for _, ev := range s.ema200.events {
			if t, err := time.Parse(time.RFC3339, ev.BarTime); err == nil && t.UnixMilli() >= since {
				all = append(all, ev)
			}
		}
		s.ema200.mu.Unlock()
	}
	total := len(all)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page <= 0 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	start, end := (page-1)*size, page*size
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	rows := append([]EMA200Event{}, all[start:end]...)
	return EMA200HistResult{Rows: rows, Total: total, Pages: pages, Page: page, Size: size}
}

func (db *DB) insertEMA200Event(coin string, st e2Coin) {
	if db.sql == nil {
		return
	}
	db.sql.Exec(`INSERT INTO ema200_events(ts,coin,dir,close,ema) VALUES(?,?,?,?,?)`,
		st.bar+e2BarMs, coin, st.crossDir, st.close, st.ema)
}

// loadEMA200Events 撈穿越紀錄(ts = 4H 收盤時間,可帶下限),最新在前。
func (db *DB) loadEMA200Events(sinceMs int64) []EMA200Event {
	if db.sql == nil {
		return nil
	}
	q := `SELECT ts,coin,dir,close,ema FROM ema200_events`
	args := []any{}
	if sinceMs > 0 {
		q += ` WHERE ts >= ?`
		args = append(args, sinceMs)
	}
	q += ` ORDER BY ts DESC, coin ASC`
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []EMA200Event{}
	for rows.Next() {
		var ev EMA200Event
		var ts int64
		if rows.Scan(&ts, &ev.Coin, &ev.Dir, &ev.Close, &ev.EMA) == nil {
			ev.BarTime = time.UnixMilli(ts).UTC().Format(time.RFC3339)
			out = append(out, ev)
		}
	}
	return out
}

// notifyEMA200 推播一筆 EMA200 穿越(使用者在帳號頁可開關「EMA200 穿越」)。
func (s *Store) notifyEMA200(coin string, st e2Coin) {
	emoji, verb := "📈", "上穿"
	if st.crossDir == "down" {
		emoji, verb = "📉", "下穿"
	}
	title := fmt.Sprintf("%s %s %s EMA200(4H)", emoji, coin, verb)
	body := fmt.Sprintf("%s 4H 收盤 $%s %s EMA200 $%s", coin, fmtPx(st.close), verb, fmtPx(st.ema))
	s.PushSendTopic("msg:ema200", title, body, "/?tab=ema200")
}
