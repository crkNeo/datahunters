package cache

import (
	"database/sql"
	"math"
	"sync"
	"time"
)

// afterprice.go — 脈衝星v12:每張單「進場後 1H / 2H / 4H / 8H」的價格回填。
//
// 跟出場規則無關:看的是「如果進場後一直抱著」,那幾個時間點的價格相對進場價漲跌多少%,
// 用來判斷 v12 進場後通常怎麼走、出場是不是太早 / 太晚。
// 價格 = 該時間點那一分鐘的 1 分 K 開盤價(幣安永續)。用的是歷史 K 線,伺服器關機錯過的時間點
// 開機後照樣補得回來;記憶體裡的單(含已平倉的)都會補,包含這個功能上線前的舊單。

var afterHours = []int{1, 2, 4, 8}

const (
	afterBatch    = 40 // 每輪最多抓幾個時間點(每個 1 次 K 線請求,權重 1)
	afterPace     = 80 * time.Millisecond
	afterMaxTries = 5 // 同一個時間點抓不到幾次就放棄(例如幣已下架)
)

// afterBooks 是要回填的書(目前只有脈衝星v12)。
func (s *Store) afterBooks() []*microBook {
	if s.pulsarV12Book == nil {
		return nil
	}
	return []*microBook{s.pulsarV12Book}
}

var afterFails = struct {
	sync.Mutex
	m map[string]int // tradeID|hour → 失敗次數
}{m: map[string]int{}}

// afterSlot 回傳某張單第 i 個時間點的欄位指標。
func afterSlot(tr *PaperTrade, i int) **float64 {
	return [](**float64){&tr.After1h, &tr.After2h, &tr.After4h, &tr.After8h}[i]
}

type afterJob struct {
	b     *microBook
	tr    *PaperTrade
	i     int
	coin  string
	entry float64
	at    int64 // 要取價的時間點(整分,ms)
	key   string
}

// AfterPriceTick 每分鐘由 main 呼叫:補上已到時間、還沒補的 1H/2H/4H/8H 價格。
func (s *Store) AfterPriceTick() {
	now := time.Now().UnixMilli()
	var jobs []afterJob
	for _, b := range s.afterBooks() {
		b.mu.Lock()
		for _, tr := range b.trades {
			if tr.Entry <= 0 || tr.Status == "pending" {
				continue
			}
			open := tr.OpenTime.UnixMilli()
			for i, h := range afterHours {
				at := (open + int64(h)*3600_000) / 60_000 * 60_000
				if *afterSlot(tr, i) != nil || now < at+60_000 { // 已補 / 那一分鐘的 K 還沒開始
					continue
				}
				key := tr.ID + "|" + time.Duration(h*int(time.Hour)).String()
				afterFails.Lock()
				dead := afterFails.m[key] >= afterMaxTries
				afterFails.Unlock()
				if dead {
					continue
				}
				jobs = append(jobs, afterJob{b: b, tr: tr, i: i, coin: tr.Coin, entry: tr.Entry, at: at, key: key})
			}
			if len(jobs) >= afterBatch {
				break
			}
		}
		b.mu.Unlock()
		if len(jobs) >= afterBatch {
			jobs = jobs[:afterBatch]
			break
		}
	}
	dirty := map[*PaperTrade]*microBook{}
	for _, j := range jobs {
		cs, err := s.ex.BinanceKlinesRange(j.coin+"USDT", "1m", j.at, j.at+59_999, 1)
		time.Sleep(afterPace)
		if err != nil || len(cs) == 0 || cs[0].Ts != j.at || cs[0].Open <= 0 {
			afterFails.Lock()
			afterFails.m[j.key]++
			afterFails.Unlock()
			continue
		}
		v := math.Round((cs[0].Open-j.entry)/j.entry*100*100) / 100
		j.b.mu.Lock()
		*afterSlot(j.tr, j.i) = &v
		j.b.mu.Unlock()
		dirty[j.tr] = j.b
	}
	if s.db == nil {
		return
	}
	for tr, b := range dirty {
		b.mu.Lock()
		a1, a2, a4, a8, id := tr.After1h, tr.After2h, tr.After4h, tr.After8h, tr.ID
		b.mu.Unlock()
		s.db.updateTradeAfter(id, a1, a2, a4, a8)
	}
}

func (db *DB) updateTradeAfter(id string, a1, a2, a4, a8 *float64) {
	if db.sql == nil {
		return
	}
	db.sql.Exec(`UPDATE paper_trades SET after_1h=?, after_2h=?, after_4h=?, after_8h=? WHERE id=?`,
		nullF(a1), nullF(a2), nullF(a4), nullF(a8), id)
}

// nullPtr 把可為 NULL 的欄位轉成 *float64(NULL → nil)。
func nullPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}
