package cache

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// afterprice.go — 每張單「進場後 N 小時」的價格回填:脈衝星v12 補 1H / 2H / 4H / 8H,
// 流星(48 小時計時出場)多補 16H / 24H。
//
// 跟出場規則無關:看的是「如果進場後一直抱著」,那幾個時間點的價格相對進場價漲跌多少%,
// 用來判斷進場後通常怎麼走、出場是不是太早 / 太晚。
// 價格 = 該時間點那一分鐘的 1 分 K 開盤價(幣安永續)。用的是歷史 K 線,伺服器關機錯過的時間點
// 開機後照樣補得回來;資料庫裡最近一個月內的單(含已平倉、這個功能上線前的舊單)都會補,新的單先補。

// afterHours 是全部的時間點(對應 after_1h … after_24h 欄);每本書用前 n 個。
var afterHours = []int{1, 2, 4, 8, 16, 24}

const (
	afterBatch    = 40 // 每輪最多抓幾個時間點(每個 1 次 K 線請求,權重 1)
	afterPace     = 80 * time.Millisecond
	afterMaxTries = 5  // 同一個時間點抓不到幾次就放棄(例如幣已下架)
	afterDays     = 30 // 只補最近一個月內進場的單
)

type afterBook struct {
	b *microBook
	n int // 用 afterHours 的前幾個時間點
}

// afterBooks 是要回填的書:脈衝星v12 補到 8H,流星補到 24H。
func (s *Store) afterBooks() []afterBook {
	var out []afterBook
	for _, ab := range []afterBook{{s.pulsarV12Book, 4}, {s.dumpRevBook, 6}} {
		if ab.b != nil {
			out = append(out, ab)
		}
	}
	return out
}

var afterFails = struct {
	sync.Mutex
	m map[string]int // tradeID|hour → 失敗次數
}{m: map[string]int{}}

// afterSlot 回傳某張單第 i 個時間點的欄位指標。
func afterSlot(tr *PaperTrade, i int) **float64 {
	return [](**float64){&tr.After1h, &tr.After2h, &tr.After4h, &tr.After8h, &tr.After16h, &tr.After24h}[i]
}

type afterJob struct {
	id    string // 單號
	i     int    // 第幾個時間點(afterHours 的索引)
	coin  string
	entry float64
	at    int64 // 要取價的時間點(整分,ms)
	key   string
}

// AfterPriceTick 每分鐘由 main 呼叫:補上已到時間、還沒補的時間點價格。
// 有資料庫時直接從資料庫找還沒補的單(全部歷史,不受記憶體只留 500 張的限制),新的單先補;
// 開機時也一樣,停機期間到期的、這個功能上線前的舊單都會補上。沒有資料庫才看記憶體。
func (s *Store) AfterPriceTick() {
	now := time.Now().UnixMilli()
	var jobs []afterJob
	if s.db != nil && s.db.sql != nil {
		for _, ab := range s.afterBooks() {
			jobs = append(jobs, s.db.afterTodo(ab.b.name, ab.n, now, afterDead(), afterBatch-len(jobs))...)
			if len(jobs) >= afterBatch {
				break
			}
		}
	} else {
		jobs = s.afterMemTodo(now)
	}
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
		if s.db != nil {
			s.db.updateTradeAfterOne(j.id, afterHours[j.i], v)
		}
		// 記憶體裡也有這張單(進行中 / 近期)→ 同步,畫面不用等重啟
		for _, ab := range s.afterBooks() {
			ab.b.mu.Lock()
			for _, tr := range ab.b.trades {
				if tr.ID == j.id {
					x := v
					*afterSlot(tr, j.i) = &x
				}
			}
			ab.b.mu.Unlock()
		}
	}
}

// afterMemTodo:沒有資料庫時,從記憶體裡的單找到期、還沒補的時間點(新的單先)。
func (s *Store) afterMemTodo(now int64) []afterJob {
	var jobs []afterJob
	for _, ab := range s.afterBooks() {
		b := ab.b
		b.mu.Lock()
		for k := len(b.trades) - 1; k >= 0 && len(jobs) < afterBatch; k-- {
			tr := b.trades[k]
			if tr.Entry <= 0 || tr.Status == "pending" || now-tr.OpenTime.UnixMilli() > afterDays*86400_000 {
				continue
			}
			for i := range ab.n {
				if *afterSlot(tr, i) != nil {
					continue
				}
				if j, ok := newAfterJob(tr.ID, tr.Coin, tr.Entry, tr.OpenTime.UnixMilli(), i, now); ok {
					jobs = append(jobs, j)
				}
			}
		}
		b.mu.Unlock()
	}
	if len(jobs) > afterBatch {
		jobs = jobs[:afterBatch]
	}
	return jobs
}

// newAfterJob:第 i 個時間點到了沒、是不是已經放棄;ok=false 代表現在不用抓。
func newAfterJob(id, coin string, entry float64, openMs int64, i int, now int64) (afterJob, bool) {
	h := afterHours[i]
	at := (openMs + int64(h)*3600_000) / 60_000 * 60_000
	if now < at+60_000 { // 那一分鐘的 K 還沒開始
		return afterJob{}, false
	}
	key := fmt.Sprintf("%s|%dh", id, h)
	afterFails.Lock()
	dead := afterFails.m[key] >= afterMaxTries
	afterFails.Unlock()
	if dead {
		return afterJob{}, false
	}
	return afterJob{id: id, i: i, coin: coin, entry: entry, at: at, key: key}, true
}

// afterDead 回傳已放棄的單(任一時間點抓不到超過上限),資料庫查詢時排除,免得一直佔住名額。
func afterDead() []string {
	afterFails.Lock()
	defer afterFails.Unlock()
	seen := map[string]bool{}
	var out []string
	for key, n := range afterFails.m {
		if n < afterMaxTries {
			continue
		}
		id := key[:strings.LastIndex(key, "|")]
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// afterTodo:資料庫裡這本書(前 n 個時間點)有到期、還沒補的單,新的先。
func (db *DB) afterTodo(book string, n int, now int64, dead []string, limit int) []afterJob {
	if limit <= 0 {
		return nil
	}
	cond := make([]string, n)
	args := []any{book, now - afterDays*86400_000} // 只補最近一個月內進場的單
	for i, h := range afterHours[:n] {
		cond[i] = fmt.Sprintf("(after_%dh IS NULL AND open_time <= ?)", h)
		args = append(args, now-int64(h)*3600_000-60_000)
	}
	q := `SELECT id,coin,entry,open_time,after_1h,after_2h,after_4h,after_8h,after_16h,after_24h FROM paper_trades
	  WHERE book=? AND status<>'pending' AND entry>0 AND open_time >= ? AND (` + strings.Join(cond, " OR ") + `)`
	if len(dead) > 0 {
		q += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(dead)), ",") + `)`
		for _, d := range dead {
			args = append(args, d)
		}
	}
	q += ` ORDER BY open_time DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var jobs []afterJob
	for rows.Next() {
		var id, coin string
		var entry float64
		var ot int64
		var a [6]sql.NullFloat64
		if rows.Scan(&id, &coin, &entry, &ot, &a[0], &a[1], &a[2], &a[3], &a[4], &a[5]) != nil {
			continue
		}
		for i := range n {
			if a[i].Valid {
				continue
			}
			if j, ok := newAfterJob(id, coin, entry, ot, i, now); ok {
				jobs = append(jobs, j)
			}
		}
		if len(jobs) >= limit {
			break
		}
	}
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs
}

func (db *DB) updateTradeAfterOne(id string, hour int, v float64) {
	if db.sql == nil {
		return
	}
	db.sql.Exec(fmt.Sprintf(`UPDATE paper_trades SET after_%dh=? WHERE id=?`, hour), v, id)
}

// nullPtr 把可為 NULL 的欄位轉成 *float64(NULL → nil)。
func nullPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}
