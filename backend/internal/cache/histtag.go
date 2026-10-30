package cache

import (
	"log"
	"math"
	"strings"
	"time"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
)

// histtag.go — 啟動時替脈衝星舊單補上後來才加的欄位(只補缺的、不覆蓋已有的值):
//
//	健康度用的進場因子:1h CVD、24h 漲幅、散戶做多比例(進場因子大約 10/13 才開始記)
//	EMA200 / 大盤:幣進場價離 4H EMA200 的 %、BTC 4H 收盤離 EMA200 的 %
//
// 全部只用「進場時刻之前」的資料,口徑跟線上進場時記的一樣(盤中單那根 15m 用 1m K 拼出進場前的部分 K)。
// 「廣度」(全市場站上 EMA200 的比例)要 200 個幣的歷史 K 線,太重,不補 —— 只有新單會有。
// 只補最近一個月內進場的單(histTagDays);幣安的多空比歷史也只保留約 30 天。
// 每筆補完(不論成敗)標 hist_tagged=1,下次啟動不再重抓。新單由 tagEntry 記錄,但萬一當時抓失敗,
// 這裡也會在下次啟動時補上。

var histTagBooks = []string{"pulsarv3", "pulsarv12"}

const (
	histTagBatch = 50
	histTagPace  = 1200 * time.Millisecond // 每筆約 4~5 個請求,放慢一點不要跟線上搶幣安額度
	lsHistDays   = 29                      // 多空比歷史只查這麼多天內的單
	histTagDays  = 30                      // 只補最近一個月內進場的單,更舊的不補
)

type histTagRow struct {
	id, book, coin              string
	entry                       float64
	openMs                      int64
	cvd1h, chg24, longAcct      float64
	ema200Dist, btcEma200       float64
	needHealth, needEMA, needBT bool
}

// BackfillPulsarTags 啟動時由 main 在背景呼叫一次:一批一批補,直到沒有缺的為止。
func (s *Store) BackfillPulsarTags() {
	if s.db == nil || s.db.sql == nil {
		return
	}
	btcCache := map[int64]float64{} // 4H 棒開盤 → BTC 離 EMA200 的 %(NaN = 抓不到)
	seen := map[string]bool{}       // 寫回 DB 失敗時同一批會一直被撈到 → 看到重複就停,不空轉
	done, filled := 0, 0
	for {
		rows := s.db.histTagTodo(histTagBatch)
		if len(rows) == 0 || seen[rows[0].id] {
			break
		}
		for _, r := range rows {
			if seen[r.id] {
				continue
			}
			seen[r.id] = true
			if s.histTagOne(&r, btcCache) {
				filled++
			}
			s.db.saveHistTag(&r)
			s.histTagSyncMem(&r)
			done++
			time.Sleep(histTagPace)
		}
	}
	if done > 0 {
		stratHistCache.clear() // 已結束列表快取重抓,燈號 / EMA200 欄才會出現
		log.Printf("脈衝星舊單補欄位:處理 %d 筆,補到資料 %d 筆", done, filled)
	}
}

// histTagOne 補一筆;回傳是否至少補到一個欄位。
func (s *Store) histTagOne(r *histTagRow, btcCache map[int64]float64) bool {
	sym := r.coin + "USDT"
	got := false
	if r.needHealth {
		barStart := r.openMs - r.openMs%(15*60_000)
		cs, err := s.ex.BinanceKlinesRange(sym, "15m", 0, barStart-1, 200)
		if err == nil && r.openMs != barStart { // 盤中進場:補上進場前的部分 K
			if ms, err := s.ex.BinanceKlinesRange(sym, "1m", barStart, r.openMs-r.openMs%60_000-1, 15); err == nil {
				if p, ok := PartialBar15m(ms, barStart); ok {
					cs = append(cs, p)
				}
			}
		}
		if err == nil && len(cs) >= 4 {
			r.cvd1h = indicator.CVDFromKlines(cs, 4)
			if n := len(cs); n >= 97 {
				r.chg24 = indicator.PctChange(cs[n-97].Close, cs[n-1].Close)
			}
			got = true
		}
		if time.Since(time.UnixMilli(r.openMs)) < lsHistDays*24*time.Hour {
			if ls, err := s.ex.BinanceLongShortAt(sym, "5m", r.openMs); err == nil {
				r.longAcct = ls.LongAccount
				got = true
			}
		}
	}
	bar4 := r.openMs - r.openMs%e2BarMs
	if r.needEMA {
		if cs4, err := s.ex.BinanceKlinesRange(sym, "4h", 0, bar4-1, e2Limit); err == nil {
			if d, ok := EMA200Dist(cs4, r.entry); ok {
				r.ema200Dist, got = d, true
			}
		}
	}
	if r.needBT {
		d, ok := btcCache[bar4]
		if !ok {
			d = math.NaN()
			if cs4, err := s.ex.BinanceKlinesRange("BTCUSDT", "4h", 0, bar4-1, e2Limit); err == nil && len(cs4) > 0 {
				if v, ok := EMA200Dist(cs4, cs4[len(cs4)-1].Close); ok {
					d = v
				}
			}
			btcCache[bar4] = d
		}
		if !math.IsNaN(d) {
			r.btcEma200, got = d, true
		}
	}
	return got
}

// histTagSyncMem 把補到的值同步到記憶體裡的同一張單(進行中 / 近期),畫面不用等下次重啟。
func (s *Store) histTagSyncMem(r *histTagRow) {
	b := s.pulsarBookByName(r.book)
	if b == nil {
		return
	}
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.ID == r.id {
			tr.CVD1h, tr.Chg24, tr.LongAcct = r.cvd1h, r.chg24, r.longAcct
			tr.Ema200Dist, tr.BtcEma200 = r.ema200Dist, r.btcEma200
		}
	}
	b.mu.Unlock()
}

// PartialBar15m 用 1m K 拼出「這根 15m 到進場前為止」的部分 K(線上盤中進場看到的那根)。
func PartialBar15m(ms []exchange.Candle, barStart int64) (exchange.Candle, bool) {
	const barMs = 15 * 60_000
	p := exchange.Candle{Ts: barStart}
	n := 0
	for _, m := range ms {
		if m.Ts < barStart || m.Ts >= barStart+barMs {
			continue
		}
		if n == 0 {
			p.Open, p.High, p.Low = m.Open, m.High, m.Low
		}
		p.High = math.Max(p.High, m.High)
		p.Low = math.Min(p.Low, m.Low)
		p.Close = m.Close
		p.Volume += m.Volume
		p.TakerBuy += m.TakerBuy
		p.QuoteVol += m.QuoteVol
		p.TakerBuyQuote += m.TakerBuyQuote
		p.Trades += m.Trades
		n++
	}
	return p, n > 0
}

// histTagTodo:最近一個月內、還沒補過、而且缺健康度因子或 EMA200 的脈衝星單,新的先。
func (db *DB) histTagTodo(limit int) []histTagRow {
	ph := strings.TrimSuffix(strings.Repeat("?,", len(histTagBooks)), ",")
	args := make([]any, 0, len(histTagBooks)+1)
	for _, b := range histTagBooks {
		args = append(args, b)
	}
	args = append(args, time.Now().AddDate(0, 0, -histTagDays).UnixMilli(), limit)
	rows, err := db.sql.Query(`SELECT id,book,coin,entry,open_time,cvd_1h,chg_24,long_acct,ema200_dist,btc_ema200
	  FROM paper_trades WHERE book IN (`+ph+`) AND hist_tagged=0 AND entry>0 AND open_time >= ?
	    AND ((cvd_1h=0 AND chg_24=0 AND long_acct=0) OR ema200_dist=0 OR btc_ema200=0)
	  ORDER BY open_time DESC LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []histTagRow
	for rows.Next() {
		var r histTagRow
		if rows.Scan(&r.id, &r.book, &r.coin, &r.entry, &r.openMs, &r.cvd1h, &r.chg24, &r.longAcct, &r.ema200Dist, &r.btcEma200) != nil {
			continue
		}
		r.needHealth = r.cvd1h == 0 && r.chg24 == 0 && r.longAcct == 0
		r.needEMA = r.ema200Dist == 0
		r.needBT = r.btcEma200 == 0
		out = append(out, r)
	}
	return out
}

func (db *DB) saveHistTag(r *histTagRow) {
	db.sql.Exec(`UPDATE paper_trades SET cvd_1h=?, chg_24=?, long_acct=?, ema200_dist=?, btc_ema200=?, hist_tagged=1 WHERE id=?`,
		r.cvd1h, r.chg24, r.longAcct, r.ema200Dist, r.btcEma200, r.id)
}
