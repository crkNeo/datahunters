package cache

import (
	"fmt"
	"strings"
	"time"
)

// strathistory.go —— 策略「已結束」與「訊號紀錄」的後端 DB 分頁。
//
// 記憶體只留精簡的即時狀態(open + 近期);完整歷史直接從 MySQL 撈,前端每次只要一頁。
// 損益採 settledPnL 在 Go 端即時重算(不依賴 DB 存的舊值,免 backfill),統計於全篩選集聚合。
// 同一 (策略, 時間窗) 的結果用 15s TTL 快取,翻頁不重查 DB。

var stratHistCache = newTTLCache(15 * time.Second)

// stratBooks 把「策略 key」對應到 paper_trades 裡的 book 名(多週期策略會有多個)。
func stratBooks(key string) []string {
	switch key {
	case "orderblock":
		return []string{"orderblock", "orderblock_4h"}
	case "orderblockv2":
		return []string{"orderblockv2", "orderblockv2_4h"}
	}
	return []string{key}
}

// bookTF 由 book 名推週期標籤(多週期策略的「週期」欄用);單週期回空字串。
func bookTF(book string) string {
	switch book {
	case "orderblock", "orderblockv2":
		return "1h"
	case "orderblock_4h", "orderblockv2_4h":
		return "4h"
	}
	return ""
}

// StratHistoryResult 是一頁已結束歷史 + 全篩選集的統計。
type StratHistoryResult struct {
	Rows  []*PaperTrade `json:"rows"`
	Total int           `json:"total"`
	Pages int           `json:"pages"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
	Stats PaperStats    `json:"stats"`
}

type stratHist struct {
	all   []*PaperTrade
	stats PaperStats
}

// strategyHistFull 撈某策略「全部已結束(可帶時間窗)」並算好統計,結果 15s 快取。
func (s *Store) strategyHistFull(key string, winMs int64) *stratHist {
	if s.db == nil {
		return &stratHist{}
	}
	ck := fmt.Sprintf("%s|%d", key, winMs)
	v, _ := stratHistCache.get(ck, func() (any, error) {
		var since int64
		if winMs > 0 {
			since = time.Now().UnixMilli() - winMs
		}
		all := s.db.loadClosedFiltered(stratBooks(key), since)
		multiTP := s.StratConfigOf(key).ExitMode == "split"
		return &stratHist{all: all, stats: computeClosedStats(all, multiTP)}, nil
	})
	if v == nil {
		return &stratHist{}
	}
	return v.(*stratHist)
}

// StrategyHistory 回一頁(size 筆)已結束 + 統計。page 從 1 起。
func (s *Store) StrategyHistory(key string, winMs int64, page, size int) StratHistoryResult {
	if size <= 0 || size > 200 {
		size = 50
	}
	if page <= 0 {
		page = 1
	}
	h := s.strategyHistFull(key, winMs)
	total := len(h.all)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	end := start + size
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	rows := h.all[start:end]
	if rows == nil {
		rows = []*PaperTrade{}
	}
	return StratHistoryResult{Rows: rows, Total: total, Pages: pages, Page: page, Size: size, Stats: h.stats}
}

// computeClosedStats 於全篩選集算勝率/平均/累計/獲利因子/分批漏斗(與前端 filterBook 同口徑)。
func computeClosedStats(all []*PaperTrade, multiTP bool) PaperStats {
	st := PaperStats{MultiTP: multiTP}
	var sum, gW, gL float64
	for _, t := range all {
		st.Closed++
		if t.PnLPct > 0 {
			st.Wins++
			gW += t.PnLPct
		} else {
			st.Losses++
			gL += -t.PnLPct
		}
		sum += t.PnLPct
		if t.Legs >= 1 {
			st.Tp1++
		}
		if t.Legs >= 2 {
			st.Tp2++
		}
		if t.Legs >= 3 {
			st.Tp3++
		}
	}
	if st.Closed > 0 {
		st.WinRate = round2(float64(st.Wins) / float64(st.Closed) * 100)
		st.AvgPnl = round2(sum / float64(st.Closed))
		st.TotalPnl = round2(sum)
		if gL > 0 {
			st.ProfitFactor = round2(gW / gL)
		} else if gW > 0 {
			st.ProfitFactor = 99.99
		}
	}
	return st
}

// loadClosedFiltered 撈多個 book 的「已結束」交易(可帶 close_time 下限),最新在前。
// 損益用 settledPnL 即時重算;多週期由 book 名填 TF。
func (db *DB) loadClosedFiltered(books []string, sinceMs int64) []*PaperTrade {
	if db.sql == nil || len(books) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(books)), ",")
	args := make([]any, 0, len(books)+1)
	for _, b := range books {
		args = append(args, b)
	}
	q := `SELECT id,book,coin,dir,score,entry,tp,sl,cur,pnl_pct,status,outcome,open_time,close_time,oi,cvd,funding,tp1,tp2,tp3,legs,filled,realized,be_hit,be_price,max_gain
	  FROM paper_trades WHERE book IN (` + ph + `) AND status='closed'`
	if sinceMs > 0 {
		q += ` AND close_time >= ?`
		args = append(args, sinceMs)
	}
	q += ` ORDER BY close_time DESC`
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []*PaperTrade{}
	for rows.Next() {
		t := &PaperTrade{}
		var book string
		var ot, ct int64
		if rows.Scan(&t.ID, &book, &t.Coin, &t.Dir, &t.Score, &t.Entry, &t.TP, &t.SL,
			&t.Cur, &t.PnLPct, &t.Status, &t.Outcome, &ot, &ct, &t.OI, &t.CVD, &t.Funding,
			&t.TP1, &t.TP2, &t.TP3, &t.Legs, &t.Filled, &t.Realized, &t.BEHit, &t.BEPrice, &t.MaxGain) != nil {
			continue
		}
		t.OpenTime = time.UnixMilli(ot).UTC()
		if ct > 0 {
			tt := time.UnixMilli(ct).UTC()
			t.CloseTime = &tt
		}
		t.TF = bookTF(book)
		t.PnLPct = round2(settledPnL(t, t.Cur)) // 統一階梯口徑,不依賴 DB 舊值
		out = append(out, t)
	}
	return out
}

// ── 訊號紀錄(score_events)的 DB 分頁 ─────────────────────────────────────

var scoreLogCache = newTTLCache(15 * time.Second)

// ScoreLogResult 是一頁訊號紀錄 + 總數。
type ScoreLogResult struct {
	Rows  []ScoreEvent `json:"rows"`
	Total int          `json:"total"`
	Pages int          `json:"pages"`
	Page  int          `json:"page"`
	Size  int          `json:"size"`
}

// ScoreLogHistory 回一頁訊號紀錄(可帶時間窗)。
func (s *Store) ScoreLogHistory(winMs int64, page, size int) ScoreLogResult {
	if s.db == nil {
		return ScoreLogResult{Rows: []ScoreEvent{}, Pages: 1, Page: 1, Size: size}
	}
	if size <= 0 || size > 200 {
		size = 50
	}
	if page <= 0 {
		page = 1
	}
	var since int64
	if winMs > 0 {
		since = time.Now().UnixMilli() - winMs
	}
	ck := fmt.Sprintf("%d", since)
	v, _ := scoreLogCache.get(ck, func() (any, error) { return s.db.loadScoreFiltered(since), nil })
	all, _ := v.([]ScoreEvent)
	total := len(all)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * size
	end := start + size
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	rows := all[start:end]
	if rows == nil {
		rows = []ScoreEvent{}
	}
	return ScoreLogResult{Rows: rows, Total: total, Pages: pages, Page: page, Size: size}
}

// loadScoreFiltered 撈訊號紀錄(可帶時間下限),最新在前。
func (db *DB) loadScoreFiltered(sinceMs int64) []ScoreEvent {
	if db.sql == nil {
		return nil
	}
	q := `SELECT ts,coin,score,bias,price FROM score_events`
	args := []any{}
	if sinceMs > 0 {
		q += ` WHERE ts >= ?`
		args = append(args, sinceMs)
	}
	q += ` ORDER BY ts DESC`
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ScoreEvent{}
	for rows.Next() {
		var e ScoreEvent
		var ts int64
		if rows.Scan(&ts, &e.Coin, &e.Score, &e.Bias, &e.Price) == nil {
			e.Time = time.UnixMilli(ts).UTC()
			out = append(out, e)
		}
	}
	return out
}
