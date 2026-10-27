package cache

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

// newsalpha_stats.go — 消息雷達的「事件資料庫 + 價格反應紀錄」(文件步驟 3)與統計(步驟 7)。
//
// 每則有幣種的事件,各時間點一到就回填:看到後 1 分鐘補 1m、5 分鐘補 5m、15 分鐘補 15m、
// 1 小時補 1h 與 60 分鐘內最大漲跌、4 小時補 4h(stage = 已完成幾個時間點,0..5)。
// 價格用 Binance 1 分 K(永續優先、沒有就現貨),從「看到消息那一分鐘」起算、依 K 線時間戳對齊。
// 用的是歷史 K 線,伺服器關機錯過的時間點開機後照樣補得回來。

var naHorizons = []struct {
	Key string
	Min int
}{{"1m", 1}, {"5m", 5}, {"15m", 15}, {"1h", 60}, {"4h", 240}}

const (
	naReactBatch = 25 // 每輪最多回填幾則
	naReactPace  = 120 * time.Millisecond
	naReactTries = 8 // 同一則抓 K 線連續失敗幾次就放棄(間隔 1、2、4…最多 30 分鐘重試)
	naReactDays  = 7 // 只回填最近幾天的事件(伺服器關機再久也補得回來)
	naStageDone  = 5 // stage = 5:全部時間點都處理完
)

// naDueStage:到 now 為止,這則事件應該完成幾個時間點(每個時間點過後多等 1 分鐘,確保那根 K 已收)。
func naDueStage(ts, now int64) int {
	k := 0
	for _, h := range naHorizons {
		if ts+int64(h.Min+1)*60_000 <= now {
			k++
		}
	}
	return k
}

// naLegacyFilled:舊欄位 filled(0 未回填 / 1 到 1h / 2 全部)由 stage 推回,維持相容。
func naLegacyFilled(stage int) int {
	switch {
	case stage >= naStageDone:
		return 2
	case stage >= 4:
		return 1
	}
	return 0
}

// NewsAlphaReactTick 每 30 秒由 main 呼叫:回填到期事件的價格反應。
func (s *Store) NewsAlphaReactTick() {
	n := s.news
	if n == nil {
		return
	}
	now := time.Now().UnixMilli()
	var cand []NewsEvent
	if s.db != nil {
		cand = s.db.newsReactTodo(now, naReactBatch*3)
	} else {
		n.mu.Lock()
		for _, ev := range n.events {
			if ev.Coin != "" && ev.Ts > now-naReactDays*86400_000 && ev.Stage < naStageDone && naDueStage(ev.Ts, now) > ev.Stage {
				cand = append(cand, *ev)
			}
		}
		n.mu.Unlock()
		sort.SliceStable(cand, func(i, j int) bool { return cand[i].Stage < cand[j].Stage })
	}
	// 失敗重試中的(還沒到下次重試時間)先跳過
	var todo []NewsEvent
	n.mu.Lock()
	for _, ev := range cand {
		if n.reactNext[ev.ID] > now {
			continue
		}
		todo = append(todo, ev)
		if len(todo) >= naReactBatch {
			break
		}
	}
	n.mu.Unlock()
	for _, ev := range todo {
		r, ok := s.naReact(ev, now)
		if ok {
			n.mu.Lock()
			for _, m := range n.events {
				if m.ID == ev.ID {
					m.R1m, m.R5m, m.R15m, m.R1h, m.R4h, m.Up60, m.Dn60 = r.R1m, r.R5m, r.R15m, r.R1h, r.R4h, r.Up60, r.Dn60
					m.Stage, m.Filled, m.NoPx = r.Stage, r.Filled, r.NoPx
					if m.Price0 == 0 {
						m.Price0 = r.Price0
					}
				}
			}
			n.mu.Unlock()
			if s.db != nil {
				s.db.updateNewsReact(&r)
			}
		}
		time.Sleep(naReactPace)
	}
}

// naReact 抓事件之後的 1 分 K,補上已到期的時間點。ok=false = 這次抓失敗,稍後重試(不寫入)。
func (s *Store) naReact(ev NewsEvent, now int64) (NewsEvent, bool) {
	n := s.news
	due := naDueStage(ev.Ts, now)
	if due <= ev.Stage {
		return ev, false
	}
	start := ev.Ts / 60_000 * 60_000
	cs, noMarket, err := s.naKlines(ev.Coin, start, naHorizons[due-1].Min+2)
	if err != nil {
		n.mu.Lock()
		n.reactFails[ev.ID]++
		fails := n.reactFails[ev.ID]
		if fails >= naReactTries {
			delete(n.reactFails, ev.ID)
			delete(n.reactNext, ev.ID)
		} else {
			n.reactNext[ev.ID] = now + int64(min(1<<(fails-1), 30))*60_000
		}
		n.mu.Unlock()
		if fails < naReactTries {
			return ev, false
		}
		ev.Stage = naStageDone // 一直抓不到 → 放棄,不卡住佇列
		ev.Filled = naLegacyFilled(ev.Stage)
		return ev, true
	}
	n.mu.Lock()
	delete(n.reactFails, ev.ID)
	delete(n.reactNext, ev.ID)
	n.mu.Unlock()
	if noMarket {
		ev.NoPx, ev.Stage = true, naStageDone // 幣安(永續 / 現貨)都沒有這個幣
		ev.Filled = naLegacyFilled(ev.Stage)
		return ev, true
	}
	ev.Stage = fillNAReact(&ev, cs, start, due)
	if ev.Stage < due && now-ev.Ts > 6*3600_000 {
		ev.Stage = naStageDone // 過了很久 K 線還是缺(停止交易等)→ 不再等
	}
	ev.Filled = naLegacyFilled(ev.Stage)
	return ev, true
}

// naKlines 先抓永續、沒有再抓現貨。noMarket = 兩邊都沒有這個交易對(或這段時間完全沒成交)。
// 不依賴記憶體裡的幣價表(剛開機 / 被限流時那張表是空的,會誤判成沒有行情)。
func (s *Store) naKlines(coin string, start int64, limit int) (cs []exchange.Candle, noMarket bool, err error) {
	sym := coin + "USDT"
	cs, err = s.ex.BinanceKlinesRange(sym, "1m", start, 0, limit)
	if err == nil && len(cs) > 0 {
		return cs, false, nil
	}
	if err != nil && !naInvalidSymbol(err) {
		return nil, false, err
	}
	cs, err = s.ex.BinanceSpotKlinesRange(sym, "1m", start, 0, limit)
	if err == nil && len(cs) > 0 {
		return cs, false, nil
	}
	if err != nil && !naInvalidSymbol(err) {
		return nil, false, err
	}
	return nil, true, nil
}

// naInvalidSymbol:幣安回「沒有這個交易對」(code -1121 / Invalid symbol)。
func naInvalidSymbol(err error) bool {
	m := err.Error()
	return strings.Contains(m, "-1121") || strings.Contains(strings.ToLower(m), "invalid symbol")
}

// fillNAReact 用「看到消息那一分鐘」起的 1 分 K 算前 due 個時間點的報酬(%),依 K 線時間戳對齊:
// N 分鐘後 = 開盤時間為 start+(N−1) 分那根的收盤。回傳實際補到第幾個時間點(K 線缺就停在那)。
func fillNAReact(ev *NewsEvent, cs []exchange.Candle, start int64, due int) int {
	byTs := make(map[int64]exchange.Candle, len(cs))
	for _, c := range cs {
		byTs[c.Ts] = c
	}
	p0 := ev.Price0
	if p0 <= 0 {
		if c, ok := byTs[start]; ok {
			p0 = c.Open
		} else {
			p0 = cs[0].Open // 消息當下還沒開始交易(例如預告上幣)→ 用第一筆成交
		}
		ev.Price0 = p0
	}
	if p0 <= 0 {
		return ev.Stage
	}
	ret := func(px float64) *float64 {
		v := math.Round((px-p0)/p0*100*100) / 100 // 四捨五入到 0.01%(round2 是無條件捨去)
		return &v
	}
	slots := []**float64{&ev.R1m, &ev.R5m, &ev.R15m, &ev.R1h, &ev.R4h}
	done := 0
	for i := 0; i < due; i++ {
		c, ok := byTs[start+int64(naHorizons[i].Min-1)*60_000]
		if !ok {
			break
		}
		*slots[i] = ret(c.Close)
		done = i + 1
	}
	if done >= 4 { // 60 分鐘內最大漲 / 跌
		hi, lo := 0.0, 0.0
		for _, c := range cs {
			if c.Ts < start || c.Ts >= start+60*60_000 {
				continue
			}
			if hi == 0 || c.High > hi {
				hi = c.High
			}
			if lo == 0 || c.Low < lo {
				lo = c.Low
			}
		}
		if hi > 0 {
			ev.Up60, ev.Dn60 = ret(hi), ret(lo)
		}
	}
	return max(done, ev.Stage)
}

// ── 統計(步驟 7:哪類消息、哪個來源有穩定方向)──

// NewsStatRow 是統計表的一列(一個分類或一個來源)。陣列依序為 1m / 5m / 15m / 1h / 4h。
type NewsStatRow struct {
	Key   string     `json:"key"`
	Label string     `json:"label"`
	N     int        `json:"n"`      // 事件數
	Coin  int        `json:"coin"`   // 有幣種的
	Dirn  int        `json:"dirn"`   // 有方向的(多 / 空)
	Cnt   [5]int     `json:"cnt"`    // 各時間點有價格的筆數
	Avg   [5]float64 `json:"avg"`    // 原始平均報酬 %
	Edge  [5]float64 `json:"edge"`   // 依方向調整後的平均報酬 %(做空的取反)= 照標記方向進場的平均獲利
	Hit   [5]float64 `json:"hit"`    // 照標記方向的勝率 %
	Up60  float64    `json:"up60"`   // 60 分鐘內平均最大漲幅 %
	Dn60  float64    `json:"dn60"`   // 60 分鐘內平均最大跌幅 %
	AIN   int        `json:"ai_n"`   // AI 有給方向(多 / 空)且有 1h 價格的筆數
	AIHit float64    `json:"ai_hit"` // AI 方向在 1h 的勝率 %
	Best  string     `json:"best"`   // 調整後平均報酬最好的時間點
}

// NewsStats 是統計頁資料。
type NewsStats struct {
	ByCategory []NewsStatRow `json:"by_category"`
	BySource   []NewsStatRow `json:"by_source"`
	Total      int           `json:"total"`
	Filled     int           `json:"filled"`
	Since      int64         `json:"since"`
}

type naAgg struct {
	row            NewsStatRow
	sum, esum, hit [5]float64
	ecnt           [5]int
	up, dn         float64
	updn           int
	aiHit          float64
}

func (a *naAgg) add(ev *NewsEvent) {
	a.row.N++
	if ev.Coin == "" {
		return
	}
	a.row.Coin++
	sign := 0.0
	switch ev.Dir {
	case "long":
		sign = 1
	case "short":
		sign = -1
	}
	if sign != 0 {
		a.row.Dirn++
	}
	rs := [5]*float64{ev.R1m, ev.R5m, ev.R15m, ev.R1h, ev.R4h}
	for i, r := range rs {
		if r == nil {
			continue
		}
		a.row.Cnt[i]++
		a.sum[i] += *r
		if sign != 0 {
			a.ecnt[i]++
			a.esum[i] += sign * *r
			if sign**r > 0 {
				a.hit[i]++
			}
		}
	}
	if ev.Up60 != nil && ev.Dn60 != nil {
		a.up += *ev.Up60
		a.dn += *ev.Dn60
		a.updn++
	}
	if ev.R1h != nil && (ev.AIDir == "long" || ev.AIDir == "short") {
		a.row.AIN++
		if (ev.AIDir == "long") == (*ev.R1h > 0) && *ev.R1h != 0 {
			a.aiHit++
		}
	}
}

func (a *naAgg) done() NewsStatRow {
	r := a.row
	best, bestV := -1, 0.0
	for i := range naHorizons {
		if r.Cnt[i] > 0 {
			r.Avg[i] = round2(a.sum[i] / float64(r.Cnt[i]))
		}
		if a.ecnt[i] > 0 {
			r.Edge[i] = round2(a.esum[i] / float64(a.ecnt[i]))
			r.Hit[i] = round2(a.hit[i] / float64(a.ecnt[i]) * 100)
			if best < 0 || r.Edge[i] > bestV {
				best, bestV = i, r.Edge[i]
			}
		}
	}
	if best >= 0 && bestV > 0 {
		r.Best = naHorizons[best].Key
	}
	if a.updn > 0 {
		r.Up60, r.Dn60 = round2(a.up/float64(a.updn)), round2(a.dn/float64(a.updn))
	}
	if r.AIN > 0 {
		r.AIHit = round2(a.aiHit / float64(r.AIN) * 100)
	}
	return r
}

func computeNewsStats(evs []NewsEvent, since int64) NewsStats {
	cats, srcs := map[string]*naAgg{}, map[string]*naAgg{}
	out := NewsStats{Since: since, ByCategory: []NewsStatRow{}, BySource: []NewsStatRow{}}
	for i := range evs {
		ev := &evs[i]
		out.Total++
		if ev.Stage > 0 {
			out.Filled++
		}
		if cats[ev.Category] == nil {
			cats[ev.Category] = &naAgg{row: NewsStatRow{Key: ev.Category, Label: newsCatLabel[ev.Category]}}
		}
		cats[ev.Category].add(ev)
		sk := naSourceGroup(ev.Source)
		if srcs[sk] == nil {
			srcs[sk] = &naAgg{row: NewsStatRow{Key: sk, Label: sk}}
		}
		srcs[sk].add(ev)
	}
	for _, a := range cats {
		out.ByCategory = append(out.ByCategory, a.done())
	}
	for _, a := range srcs {
		out.BySource = append(out.BySource, a.done())
	}
	byN := func(rows []NewsStatRow) {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].N != rows[j].N {
				return rows[i].N > rows[j].N
			}
			return rows[i].Key < rows[j].Key
		})
	}
	byN(out.ByCategory)
	byN(out.BySource)
	return out
}

// naSourceGroup:統計時來源用原始代號(tg:xxx 各頻道分開,才看得出哪個頻道有用)。
func naSourceGroup(src string) string { return src }

var naStatsCache = newTTLCache(60 * time.Second)

// NewsAlphaStats 回傳時間窗內(winMs = 0 為全部)的統計。
func (s *Store) NewsAlphaStats(winMs int64) NewsStats {
	var since int64
	if winMs > 0 {
		since = time.Now().UnixMilli() - winMs
	}
	if s.db != nil {
		v, _ := naStatsCache.get(fmt.Sprint(since/60_000), func() (any, error) {
			return computeNewsStats(s.db.loadNewsEvents(since, "", "", "", 0), since), nil
		})
		if st, ok := v.(NewsStats); ok {
			return st
		}
	}
	return computeNewsStats(s.naMemEvents(since, "", "", ""), since)
}

func (s *Store) naMemEvents(since int64, source, category, coin string) []NewsEvent {
	var out []NewsEvent
	if s.news == nil {
		return out
	}
	s.news.mu.Lock()
	defer s.news.mu.Unlock()
	for _, ev := range s.news.events {
		if ev.Ts >= since && naMatch(ev, source, category, coin) {
			out = append(out, *ev)
		}
	}
	return out
}

func naMatch(ev *NewsEvent, source, category, coin string) bool {
	if source != "" && ev.Source != source && !(source == "tg" && strings.HasPrefix(ev.Source, "tg:")) {
		return false
	}
	if category != "" && ev.Category != category {
		return false
	}
	return coin == "" || ev.Coin == coin
}

// ── 消息流分頁 ──

var naHistCache = newTTLCache(10 * time.Second)

// NewsEventsPage 是一頁消息流。
type NewsEventsPage struct {
	Rows  []NewsEvent `json:"rows"`
	Total int         `json:"total"`
	Pages int         `json:"pages"`
	Page  int         `json:"page"`
	Size  int         `json:"size"`
}

// NewsAlphaEvents 回一頁事件(最新在前),可依來源 / 分類 / 幣種篩選。
// 最新那一批若 AI 剛判斷完、記憶體比 DB 新,以記憶體為準。
func (s *Store) NewsAlphaEvents(winMs int64, page, size int, source, category, coin string) NewsEventsPage {
	if size <= 0 || size > 200 {
		size = 50
	}
	var since int64
	if winMs > 0 {
		since = time.Now().UnixMilli() - winMs
	}
	coin = strings.ToUpper(strings.TrimSpace(coin))
	var all []NewsEvent
	if s.db != nil {
		key := fmt.Sprintf("%d|%s|%s|%s", since/10_000, source, category, coin)
		v, _ := naHistCache.get(key, func() (any, error) { return s.db.loadNewsEvents(since, source, category, coin, 5000), nil })
		all, _ = v.([]NewsEvent)
		all = s.naOverlay(all)
	} else {
		all = s.naMemEvents(since, source, category, coin)
	}
	total := len(all)
	pages := max((total+size-1)/size, 1)
	page = min(max(page, 1), pages)
	start, end := min((page-1)*size, total), min(page*size, total)
	return NewsEventsPage{Rows: append([]NewsEvent{}, all[start:end]...), Total: total, Pages: pages, Page: page, Size: size}
}

// naOverlay 用記憶體裡較新的版本(AI 判斷、回填結果)覆蓋 DB 快取讀到的同一則。
func (s *Store) naOverlay(rows []NewsEvent) []NewsEvent {
	if s.news == nil || len(rows) == 0 {
		return rows
	}
	s.news.mu.Lock()
	mem := make(map[string]*NewsEvent, len(s.news.events))
	for _, ev := range s.news.events {
		mem[ev.ID] = ev
	}
	out := make([]NewsEvent, len(rows))
	for i, r := range rows {
		if m := mem[r.ID]; m != nil {
			out[i] = *m
		} else {
			out[i] = r
		}
	}
	s.news.mu.Unlock()
	return out
}

// ── 總覽(來源狀態 / 韓國溢價 / 預測市場)──

// NewsAlphaOverview 是消息雷達頁上方的總覽。
type NewsAlphaOverview struct {
	Sources   []NewsSourceStat `json:"sources"`
	Premium   []NewsPremium    `json:"premium"` // 溢價最高的 15 + 最低的 5
	PremBTC   float64          `json:"prem_btc"`
	PremAvg   float64          `json:"prem_avg"`
	PremN     int              `json:"prem_n"`
	PremAt    string           `json:"prem_at"`
	Poly      []NewsPoly       `json:"poly"`    // 1 小時變化最大的 15 個
	Today     map[string]int   `json:"today"`   // 24 小時內各分類事件數
	AIUsed    int              `json:"ai_used"` // 最近一小時用掉的 AI 次數
	AILimit   int              `json:"ai_limit"`
	ChainHead uint64           `json:"chain_head"`
	UpdatedAt string           `json:"updated_at"`
}

func (s *Store) NewsAlpha() NewsAlphaOverview {
	out := NewsAlphaOverview{Sources: []NewsSourceStat{}, Premium: []NewsPremium{}, Poly: []NewsPoly{}, Today: map[string]int{},
		AILimit: naAIPerHour, UpdatedAt: time.Now().Format(time.RFC3339)}
	n := s.news
	if n == nil {
		return out
	}
	day := time.Now().UnixMilli() - 86400_000
	n.mu.Lock()
	for _, src := range n.srcs {
		out.Sources = append(out.Sources, *n.stat[src.key])
	}
	for _, ev := range n.events {
		if ev.Ts >= day {
			out.Today[ev.Category]++
		}
	}
	now := time.Now().UnixMilli()
	for _, t := range n.aiCalls {
		if now-t < 3600_000 {
			out.AIUsed++
		}
	}
	n.mu.Unlock()
	x := &n.ext
	x.mu.Lock()
	p := x.prem
	if len(p) > 20 {
		p = append(append([]NewsPremium{}, p[:15]...), p[len(p)-5:]...)
	}
	out.Premium = append(out.Premium, p...)
	out.PremBTC, out.PremAvg, out.PremN = x.premBTC, x.premAvg, len(x.prem)
	if !x.premAt.IsZero() {
		out.PremAt = x.premAt.Format(time.RFC3339)
	}
	pl := x.poly
	if len(pl) > 15 {
		pl = pl[:15]
	}
	out.Poly = append(out.Poly, pl...)
	out.ChainHead = x.chainHead
	x.mu.Unlock()
	return out
}

// ── DB ──

func (db *DB) insertNewsEvent(ev *NewsEvent) bool {
	if db.sql == nil {
		return true
	}
	res, err := db.sql.Exec(`INSERT IGNORE INTO news_events(id,ts,pub,source,category,coin,coins,title,title_zh,url,dir,strength,tradable,value,price0,late)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ev.ID, ev.Ts, ev.Pub, ev.Source, ev.Category, ev.Coin, strings.Join(ev.Coins, ","), ev.Title, ev.TitleZH,
		trimRunes(ev.URL, 1000), ev.Dir, ev.Strength, ev.Tradable, ev.Value, ev.Price0, ev.Late)
	if err != nil {
		return true // DB 出錯不擋事件(記憶體 + 推播照常)
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (db *DB) updateNewsAI(ev *NewsEvent) {
	if db.sql == nil {
		return
	}
	db.sql.Exec(`UPDATE news_events SET ai_dir=?, ai_strength=?, ai_reason=?, title_zh=IF(title_zh='' OR title_zh IS NULL, ?, title_zh) WHERE id=?`,
		ev.AIDir, ev.AIStrength, ev.AIReason, ev.TitleZH, ev.ID)
}

func (db *DB) updateNewsReact(ev *NewsEvent) {
	if db.sql == nil {
		return
	}
	db.sql.Exec(`UPDATE news_events SET price0=?, r1m=?, r5m=?, r15m=?, r1h=?, r4h=?, up60=?, dn60=?, filled=?, stage=?, nopx=? WHERE id=?`,
		ev.Price0, nullF(ev.R1m), nullF(ev.R5m), nullF(ev.R15m), nullF(ev.R1h), nullF(ev.R4h), nullF(ev.Up60), nullF(ev.Dn60),
		ev.Filled, ev.Stage, ev.NoPx, ev.ID)
}

func (db *DB) hasNewsEvent(id string) bool {
	if db.sql == nil {
		return false
	}
	var n int
	db.sql.QueryRow(`SELECT COUNT(*) FROM news_events WHERE id=?`, id).Scan(&n)
	return n > 0
}

func (db *DB) deleteNewsEvent(id string) bool {
	if db.sql == nil {
		return false
	}
	res, err := db.sql.Exec(`DELETE FROM news_events WHERE id=?`, id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (db *DB) markNewsTraded(id string) {
	if db.sql == nil {
		return
	}
	db.sql.Exec(`UPDATE news_events SET traded=1 WHERE id=?`, id)
}

func nullF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

const naCols = `id,ts,pub,source,category,coin,coins,title,title_zh,url,dir,strength,tradable,value,ai_dir,ai_strength,ai_reason,price0,r1m,r5m,r15m,r1h,r4h,up60,dn60,filled,traded,late,stage,nopx`

func scanNewsEvent(rows *sql.Rows) (NewsEvent, error) {
	var ev NewsEvent
	var coins, title, zh, url sql.NullString
	var r1m, r5m, r15m, r1h, r4h, up, dn sql.NullFloat64
	err := rows.Scan(&ev.ID, &ev.Ts, &ev.Pub, &ev.Source, &ev.Category, &ev.Coin, &coins, &title, &zh, &url, &ev.Dir,
		&ev.Strength, &ev.Tradable, &ev.Value, &ev.AIDir, &ev.AIStrength, &ev.AIReason, &ev.Price0,
		&r1m, &r5m, &r15m, &r1h, &r4h, &up, &dn, &ev.Filled, &ev.Traded, &ev.Late, &ev.Stage, &ev.NoPx)
	if err != nil {
		return ev, err
	}
	ev.Title, ev.TitleZH, ev.URL = title.String, zh.String, url.String
	if coins.String != "" {
		ev.Coins = strings.Split(coins.String, ",")
	}
	pf := func(v sql.NullFloat64) *float64 {
		if !v.Valid {
			return nil
		}
		x := v.Float64
		return &x
	}
	ev.R1m, ev.R5m, ev.R15m, ev.R1h, ev.R4h, ev.Up60, ev.Dn60 = pf(r1m), pf(r5m), pf(r15m), pf(r1h), pf(r4h), pf(up), pf(dn)
	return ev, nil
}

// loadNewsEvents 依條件撈事件(最新在前);limit = 0 不限。
func (db *DB) loadNewsEvents(since int64, source, category, coin string, limit int) []NewsEvent {
	out := []NewsEvent{}
	if db.sql == nil {
		return out
	}
	q := `SELECT ` + naCols + ` FROM news_events WHERE ts >= ?`
	args := []any{since}
	switch {
	case source == "tg":
		q += ` AND source LIKE 'tg:%'`
	case source != "":
		q += ` AND source = ?`
		args = append(args, source)
	}
	if category != "" {
		q += ` AND category = ?`
		args = append(args, category)
	}
	if coin != "" {
		q += ` AND coin = ?`
		args = append(args, coin)
	}
	q += ` ORDER BY ts DESC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := db.sql.Query(q, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if ev, err := scanNewsEvent(rows); err == nil {
			out = append(out, ev)
		}
	}
	return out
}

// newsReactTodo:有時間點到期、還沒補的事件(最近 naReactDays 天)。完成度低的先(剛出的消息 1m 優先)。
func (db *DB) newsReactTodo(now int64, limit int) []NewsEvent {
	out := []NewsEvent{}
	if db.sql == nil {
		return out
	}
	cond := make([]string, 0, len(naHorizons))
	args := []any{now - naReactDays*86400_000}
	for i, h := range naHorizons {
		cond = append(cond, fmt.Sprintf("(stage = %d AND ts <= ?)", i))
		args = append(args, now-int64(h.Min+1)*60_000)
	}
	args = append(args, limit)
	rows, err := db.sql.Query(`SELECT `+naCols+` FROM news_events WHERE coin <> '' AND ts > ? AND stage < 5 AND (`+
		strings.Join(cond, " OR ")+`) ORDER BY stage ASC, ts ASC LIMIT ?`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if ev, err := scanNewsEvent(rows); err == nil {
			out = append(out, ev)
		}
	}
	return out
}

// loadRecentNews 開機時把最近的事件載回記憶體(消息流、AI、回填都靠它)。
func (db *DB) loadRecentNews(limit int) []*NewsEvent {
	rows := db.loadNewsEvents(0, "", "", "", limit)
	out := make([]*NewsEvent, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}
