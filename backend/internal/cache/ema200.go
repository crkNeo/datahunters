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
// 掃描「爆量脈搏」面板上的幣(surgeBoardCoins),看 4H 收盤價與 4H EMA200 的關係:
// 最新一根已收的 4H 收盤從下方站上 EMA200 = 上穿,從上方跌破 = 下穿,第一次偵測到時推播
// (使用者可在帳號頁開關「EMA200 穿越」)。
//
// 成本:每根 4H 收盤後,對面板上的幣各抓一次 4H K 線(約 60 檔);面板中途新進來的幣也只抓一次。
// 同一根 4H 內不會重抓。現價取爆量脈搏面板上的價格(面板幣大多不在 WS feed 裡)。

const (
	e2Period  = 200
	e2MinBars = 230 // EMA 從第一根收盤起算,至少要這麼多根已收 K 才算收斂;上市太新的幣跳過
	e2Limit   = 260
	e2BarMs   = int64(4 * time.Hour / time.Millisecond)
	e2Keep    = 200 // 保留的穿越紀錄筆數
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

// EMA200Data 是 VIP 頁的資料。
type EMA200Data struct {
	Rows      []EMA200Row   `json:"rows"`
	Events    []EMA200Event `json:"events"`
	UpdatedAt string        `json:"updated_at"`
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

// EMA200Tick 每分鐘呼叫:面板上的幣若還沒有「最新已收 4H」的狀態就抓一次 K 線算好,
// 最新那根剛好穿越且還沒推播過就推播。
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
	for _, coin := range s.surgeBoardCoins() {
		e.mu.Lock()
		cur, have := e.coins[coin]
		e.mu.Unlock()
		if have && cur.bar == lastBar {
			continue // 這根 4H 已算過
		}
		cs, err := s.ex.BinanceKlines(coin+"USDT", "4h", e2Limit)
		time.Sleep(25 * time.Millisecond) // pace the REST batch
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
		s.notifyEMA200(h.coin, h.st)
	}
}

// EMA200 回傳 VIP 頁資料:目前爆量脈搏面板上、已有 EMA200 狀態的幣 + 最近的穿越紀錄。
// 排序:最近剛穿越的在前,其餘依離 EMA200 的距離由近到遠。
func (s *Store) EMA200() EMA200Data {
	out := EMA200Data{Rows: []EMA200Row{}, Events: []EMA200Event{}, UpdatedAt: time.Now().Format(time.RFC3339)}
	if s.ema200 == nil {
		return out
	}
	board := s.SurgeBoard()
	e := s.ema200
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range board {
		st, ok := e.coins[r.Coin]
		if !ok || st.ema <= 0 {
			continue
		}
		px := r.Price
		if px <= 0 {
			px = st.close
		}
		row := EMA200Row{Coin: r.Coin, Price: px, EMA: st.ema, DistPct: round2((px - st.ema) / st.ema * 100),
			Above: st.close > st.ema, CrossDir: st.crossDir, BarsSince: st.barsSince}
		if st.crossBar > 0 {
			row.CrossTime = time.UnixMilli(st.crossBar + e2BarMs).UTC().Format(time.RFC3339)
		}
		out.Rows = append(out.Rows, row)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		ra, rb := a.CrossDir != "" && a.BarsSince == 0, b.CrossDir != "" && b.BarsSince == 0
		if ra != rb {
			return ra // 最新收盤剛穿越的排最前
		}
		return math.Abs(a.DistPct) < math.Abs(b.DistPct)
	})
	out.Events = append(out.Events, e.events...)
	return out
}

// notifyEMA200 推播一筆 EMA200 穿越(使用者在帳號頁可開關「EMA200 穿越」)。
func (s *Store) notifyEMA200(coin string, st e2Coin) {
	emoji, verb := "📈", "上穿"
	if st.crossDir == "down" {
		emoji, verb = "📉", "下穿"
	}
	title := fmt.Sprintf("%s %s %s EMA200(4H)", emoji, coin, verb)
	closeAt := time.UnixMilli(st.bar + e2BarMs).Local().Format("01/02 15:04")
	body := fmt.Sprintf("%s 4H 收盤(%s)$%s %s EMA200 $%s", coin, closeAt, fmtPx(st.close), verb, fmtPx(st.ema))
	s.PushSendTopic("msg:ema200", title, body, "/?tab=ema200")
}
