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
// 每則有幣種的事件,過了 1 小時先回填 1m / 5m / 15m / 1h 報酬與 60 分鐘內最大漲跌,
// 過了 4 小時再補 4h。價格用 Binance 1 分 K(永續優先、沒有就現貨),從「看到消息那一分鐘」起算。

var naHorizons = []struct {
	Key string
	Min int
}{{"1m", 1}, {"5m", 5}, {"15m", 15}, {"1h", 60}, {"4h", 240}}

const (
	naReactBatch = 15 // 每輪最多回填幾則
	naReactPace  = 150 * time.Millisecond
)

// NewsAlphaReactTick 每 2 分鐘由 main 呼叫:回填到期事件的價格反應。
func (s *Store) NewsAlphaReactTick() {
	n := s.news
	if n == nil {
		return
	}
	now := time.Now().UnixMilli()
	cut1, cut2 := now-62*60_000, now-242*60_000
	var todo []NewsEvent
	if s.db != nil {
		todo = s.db.newsReactTodo(cut1, cut2, naReactBatch)
	} else {
		n.mu.Lock()
		for _, ev := range n.events {
			if ev.Coin != "" && ((ev.Filled == 0 && ev.Ts < cut1) || (ev.Filled == 1 && ev.Ts < cut2)) {
				todo = append(todo, *ev)
			}
		}
		n.mu.Unlock()
		if len(todo) > naReactBatch {
			todo = todo[:naReactBatch]
		}
	}
	if len(todo) == 0 {
		return
	}
	s.naPrices(0)
	for _, ev := range todo {
		r := s.naReact(ev, now)
		n.mu.Lock()
		for _, m := range n.events {
			if m.ID == ev.ID {
				m.R1m, m.R5m, m.R15m, m.R1h, m.R4h, m.Up60, m.Dn60, m.Filled = r.R1m, r.R5m, r.R15m, r.R1h, r.R4h, r.Up60, r.Dn60, r.Filled
				if m.Price0 == 0 {
					m.Price0 = r.Price0
				}
			}
		}
		n.mu.Unlock()
		if s.db != nil {
			s.db.updateNewsReact(&r)
		}
		time.Sleep(naReactPace)
	}
}

// naReact 抓事件之後的 1 分 K,算出各時間點的報酬;回傳更新後的事件(Filled 一定前進,抓不到也不重試)。
func (s *Store) naReact(ev NewsEvent, now int64) NewsEvent {
	start := ev.Ts / 60_000 * 60_000
	full := now-ev.Ts >= 242*60_000
	limit := 62
	if full {
		limit = 242
	}
	var cs []exchange.Candle
	var err error
	if s.news.hasPerp(ev.Coin) {
		cs, err = s.ex.BinanceKlinesRange(ev.Coin+"USDT", "1m", start, 0, limit)
	} else if s.news.known(ev.Coin) {
		cs, err = s.ex.BinanceSpotKlinesRange(ev.Coin+"USDT", "1m", start, 0, limit)
	}
	ev.Filled = 1
	if full {
		ev.Filled = 2
	}
	if err != nil || len(cs) == 0 {
		return ev
	}
	fillNAReact(&ev, cs)
	return ev
}

// fillNAReact 由「看到消息那一分鐘」起的 1 分 K 算各時間點報酬(%):N 分鐘後 = 第 N 根的收盤。
func fillNAReact(ev *NewsEvent, cs []exchange.Candle) {
	p0 := ev.Price0
	if p0 <= 0 {
		p0 = cs[0].Open
		ev.Price0 = p0
	}
	if p0 <= 0 {
		return
	}
	ret := func(px float64) *float64 {
		v := math.Round((px-p0)/p0*100*100) / 100 // 四捨五入到 0.01%(round2 是無條件捨去)
		return &v
	}
	at := func(min int) *float64 {
		if len(cs) < min {
			return nil
		}
		return ret(cs[min-1].Close)
	}
	ev.R1m, ev.R5m, ev.R15m, ev.R1h = at(1), at(5), at(15), at(60)
	if ev.Filled >= 2 {
		ev.R4h = at(240)
	}
	hi, lo := cs[0].High, cs[0].Low
	for i := 0; i < len(cs) && i < 60; i++ {
		hi, lo = math.Max(hi, cs[i].High), math.Min(lo, cs[i].Low)
	}
	ev.Up60, ev.Dn60 = ret(hi), ret(lo)
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
		if ev.Filled > 0 {
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
	res, err := db.sql.Exec(`INSERT IGNORE INTO news_events(id,ts,pub,source,category,coin,coins,title,title_zh,url,dir,strength,tradable,value,price0)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ev.ID, ev.Ts, ev.Pub, ev.Source, ev.Category, ev.Coin, strings.Join(ev.Coins, ","), ev.Title, ev.TitleZH,
		trimRunes(ev.URL, 1000), ev.Dir, ev.Strength, ev.Tradable, ev.Value, ev.Price0)
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
	db.sql.Exec(`UPDATE news_events SET price0=?, r1m=?, r5m=?, r15m=?, r1h=?, r4h=?, up60=?, dn60=?, filled=? WHERE id=?`,
		ev.Price0, nullF(ev.R1m), nullF(ev.R5m), nullF(ev.R15m), nullF(ev.R1h), nullF(ev.R4h), nullF(ev.Up60), nullF(ev.Dn60), ev.Filled, ev.ID)
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

const naCols = `id,ts,pub,source,category,coin,coins,title,title_zh,url,dir,strength,tradable,value,ai_dir,ai_strength,ai_reason,price0,r1m,r5m,r15m,r1h,r4h,up60,dn60,filled,traded`

func scanNewsEvent(rows *sql.Rows) (NewsEvent, error) {
	var ev NewsEvent
	var coins, title, zh, url sql.NullString
	var r1m, r5m, r15m, r1h, r4h, up, dn sql.NullFloat64
	err := rows.Scan(&ev.ID, &ev.Ts, &ev.Pub, &ev.Source, &ev.Category, &ev.Coin, &coins, &title, &zh, &url, &ev.Dir,
		&ev.Strength, &ev.Tradable, &ev.Value, &ev.AIDir, &ev.AIStrength, &ev.AIReason, &ev.Price0,
		&r1m, &r5m, &r15m, &r1h, &r4h, &up, &dn, &ev.Filled, &ev.Traded)
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

// newsReactTodo:需要回填的事件(1h 階段 / 4h 階段),最舊的先。只看最近 3 天,太舊的就算了。
func (db *DB) newsReactTodo(cut1, cut2 int64, limit int) []NewsEvent {
	out := []NewsEvent{}
	if db.sql == nil {
		return out
	}
	rows, err := db.sql.Query(`SELECT `+naCols+` FROM news_events WHERE coin <> '' AND ts > ? AND
		((filled = 0 AND ts < ?) OR (filled = 1 AND ts < ?)) ORDER BY ts ASC LIMIT ?`,
		time.Now().UnixMilli()-3*86400_000, cut1, cut2, limit)
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
