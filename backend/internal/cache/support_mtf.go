package cache

import (
	"fmt"
	"math"
	"time"

	"datahunter/internal/exchange"
)

// support_mtf.go: 錘子/流星(插針)訊號監控 —— 同時盯 1H 與 4H,每根收盤檢查最新一根
// 是否為錘子(做多)或流星(做空),命中就發通知。純提示,不下單、無止盈止損。
//
// 資料全部來自 WS feed 的 1h 緩衝,4H 由 1h 就地聚合(to4h)→ 零 REST。
// (分頁對外 key 沿用 "srmtf"、方法名沿用 SRMTF*,是為了不動已驗證的路由/白名單;
//  實際邏輯是插針偵測,與支撐壓力無關。)
//
// 型態定義(定版 2026-07-25):形狀 + 位置脈絡。
//   錘頭線(hammer, 做多):實體「非常小」+ 下影線遠大於實體、絕對主導上影線
//                          + 當根 low < 前 10 根最低(出現在局部低點)。
//   射擊星(star,   做空):實體「非常小」+ 上影線遠大於實體、絕對主導下影線
//                          + 當根 high > 前 10 根最高(出現在局部高點)。
//   反向可帶一點點影線,只要主影線絕對主導仍算數。顏色不要求。

const (
	pinBodyMax    = 0.3 // 實體 ≤ N × 全棒範圍(實體「非常小」)
	pinWickBody   = 2.0 // 主影線 ≥ N × 實體(明顯較長、遠大於實體)
	pinWickDom    = 2.0 // 主影線 ≥ N × 反向影線(絕對主導)
	pinCtxBars    = 10  // 位置脈絡:與前 N 根比高低(局部低/高點)
	pinDisplayMax = 60  // 分頁顯示最近 N 筆
)

// PinHit 是一次插針命中。
type PinHit struct {
	Coin  string  `json:"coin"`
	TF    string  `json:"tf"`   // 1H | 4H
	Kind  string  `json:"kind"` // hammer(做多) | star(做空)
	Price float64 `json:"price"`
	Time  int64   `json:"time"` // 命中棒的開盤時戳(ms)
}

// PinData 是分頁 payload(最近命中,新到舊)。
type PinData struct {
	Hits      []PinHit `json:"hits"`
	UpdatedAt string   `json:"updated_at"`
}

// pinKind 判定 cs 最後一根是否為錘頭線/射擊星,回 "hammer" / "star" / ""。
// 形狀(實體很小、主影線遠大於實體且絕對主導反向影線)+ 位置脈絡(局部低/高點)。
func pinKind(cs []exchange.Candle) string {
	n := len(cs)
	if n < pinCtxBars+1 {
		return ""
	}
	c := cs[n-1]
	rng := c.High - c.Low
	if rng <= 0 {
		return "" // 一字線,無形狀可言
	}
	body := math.Abs(c.Close - c.Open)
	if body > pinBodyMax*rng {
		return "" // 實體不夠小
	}
	upper := c.High - math.Max(c.Open, c.Close)
	lower := math.Min(c.Open, c.Close) - c.Low
	// 位置脈絡:前 pinCtxBars 根的最高 / 最低
	hi, lo := cs[n-2].High, cs[n-2].Low
	for i := n - pinCtxBars - 1; i < n-1; i++ {
		if cs[i].High > hi {
			hi = cs[i].High
		}
		if cs[i].Low < lo {
			lo = cs[i].Low
		}
	}
	switch {
	case lower >= pinWickBody*body && lower >= pinWickDom*upper && c.Low < lo:
		return "hammer" // 小實體 + 長下影絕對主導 + 局部低點 → 做多
	case upper >= pinWickBody*body && upper >= pinWickDom*lower && c.High > hi:
		return "star" // 小實體 + 長上影絕對主導 + 局部高點 → 做空
	}
	return ""
}

// SRMTFTick 每根新收的 1H / 4H 棒對幣種池掃一次插針。零 REST(讀 feed 緩衝)。
func (s *Store) SRMTFTick() {
	btc1h := s.closed1h("BTC", 1)
	if len(btc1h) == 0 {
		return
	}
	bar1h := btc1h[0].Ts
	var bar4h int64
	if c4 := to4h(s.closed1h("BTC", pinCtxBars*4+8)); len(c4) > 0 {
		bar4h = c4[len(c4)-1].Ts
	}

	s.srmMu.Lock()
	new1h := bar1h != 0 && bar1h != s.srmBar1h
	new4h := bar4h != 0 && bar4h != s.srmBar4h
	first := s.srmBar1h == 0
	if !new1h && !new4h {
		s.srmMu.Unlock()
		return
	}
	s.srmBar1h = bar1h
	s.srmBar4h = bar4h
	s.srmMu.Unlock()

	if first {
		return // 開機只建基準,不對歷史發通知
	}

	need := pinCtxBars + 2
	var fresh []PinHit
	for _, coin := range s.coins {
		if new1h {
			cs := s.closed1h(coin, need)
			if k := pinKind(cs); k != "" {
				fresh = append(fresh, PinHit{coin, "1H", k, roundPx(cs[len(cs)-1].Close), cs[len(cs)-1].Ts})
			}
		}
		if new4h {
			cs := to4h(s.closed1h(coin, need*4+4))
			if k := pinKind(cs); k != "" {
				fresh = append(fresh, PinHit{coin, "4H", k, roundPx(cs[len(cs)-1].Close), cs[len(cs)-1].Ts})
			}
		}
	}
	if len(fresh) == 0 {
		return
	}

	// 持久化(重啟不清除)+ 通知。
	for _, h := range fresh {
		if s.db != nil {
			s.db.pinInsert(h)
		}
		s.notifyPin(h)
	}
}

// ---- 持久化(pin_hits 表)----

// pinInsert 寫入一筆命中;UNIQUE(coin,tf,ts) → 同一根棒重複命中會被忽略。
func (db *DB) pinInsert(h PinHit) {
	db.sql.Exec(`INSERT IGNORE INTO pin_hits(coin,tf,kind,price,ts,created) VALUES(?,?,?,?,?,?)`,
		h.Coin, h.TF, h.Kind, h.Price, h.Time, time.Now().UnixMilli())
}

// pinRecent 讀最近 limit 筆命中(新到舊)。
func (db *DB) pinRecent(limit int) []PinHit {
	rows, err := db.sql.Query(`SELECT coin,tf,kind,price,ts FROM pin_hits ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []PinHit{}
	for rows.Next() {
		var h PinHit
		if rows.Scan(&h.Coin, &h.TF, &h.Kind, &h.Price, &h.Time) == nil {
			out = append(out, h)
		}
	}
	return out
}

// SRMTF 回傳最近的插針命中(新到舊)。分頁用。從 DB 讀 → 重啟後紀錄仍在。
func (s *Store) SRMTF() PinData {
	hits := []PinHit{}
	if s.db != nil {
		if l := s.db.pinRecent(pinDisplayMax); l != nil {
			hits = l
		}
	}
	return PinData{Hits: hits, UpdatedAt: time.Now().Format(time.RFC3339)}
}

// notifyPin 發插針通知(純提示,無下單)。推播對象依分頁權限(同 notifyCloseBook)。
func (s *Store) notifyPin(h PinHit) {
	var emoji, title, body string
	if h.Kind == "hammer" {
		emoji, title = "🔨", h.Coin+" "+h.TF+" 錘頭線(做多)"
		body = fmt.Sprintf("%s %s 收盤 $%s 出現錘頭線:小實體 + 長下影線 + 局部低點", h.Coin, h.TF, fmtPx(h.Price))
	} else {
		emoji, title = "☄️", h.Coin+" "+h.TF+" 射擊星(做空)"
		body = fmt.Sprintf("%s %s 收盤 $%s 出現射擊星:小實體 + 長上影線 + 局部高點", h.Coin, h.TF, fmtPx(h.Price))
	}
	url := "/?tab=srmtf"
	if s.TabRole("srmtf") == "admin" {
		if s.db != nil && s.pushMgr != nil {
			if subs := s.db.adminSubs(); len(subs) > 0 {
				go s.pushMgr.SendTo(subs, emoji+" "+title, body, url)
			}
		}
	} else {
		s.PushSend(emoji+" "+title, body, url)
	}
	if s.notifier.Enabled() {
		go s.notifier.Send(fmt.Sprintf("%s <b>[插針訊號] %s</b>\n%s", emoji, title, body))
	}
}

// to4h 把 1h 收盤棒聚合成 4h(對齊 UTC 0/4/8/12/16/20),只回已完結(湊滿 4 根)的。
func to4h(cs1h []exchange.Candle) []exchange.Candle {
	const p = int64(4 * 3600 * 1000)
	groups := map[int64][]exchange.Candle{}
	var order []int64
	for _, c := range cs1h {
		b := (c.Ts / p) * p
		if _, ok := groups[b]; !ok {
			order = append(order, b)
		}
		groups[b] = append(groups[b], c)
	}
	out := make([]exchange.Candle, 0, len(order))
	for _, b := range order {
		g := groups[b]
		if len(g) != 4 {
			continue
		}
		hi, lo := g[0].High, g[0].Low
		for _, c := range g {
			if c.High > hi {
				hi = c.High
			}
			if c.Low < lo {
				lo = c.Low
			}
		}
		out = append(out, exchange.Candle{Ts: b, Open: g[0].Open, High: hi, Low: lo, Close: g[3].Close})
	}
	return out
}
