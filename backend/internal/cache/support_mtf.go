package cache

import (
	"fmt"
	"math"
	"sort"
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
// 型態定義(定版,見使用者提供的表):
//   錘子(hammer, 做多):下影 ≥ 2×實體、上影 ≤ 0.5×實體、當根 low < 前 5 根最低
//   流星(star,   做空):上影 ≥ 2×實體、下影 ≤ 0.5×實體、當根 high > 前 5 根最高
//   顏色不要求。

const (
	pinShadowMul  = 2.0 // 長影線 ≥ N×實體
	pinOppMul     = 0.5 // 另一側影線 ≤ N×實體
	pinCtxBars    = 5   // 位置脈絡:與前 N 根比高低
	pinKeep       = 200 // 保留最近 N 筆命中
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

// pinKind 判定 cs 最後一根是否為錘子/流星,回 "hammer" / "star" / ""。
func pinKind(cs []exchange.Candle) string {
	n := len(cs)
	if n < pinCtxBars+1 {
		return ""
	}
	c := cs[n-1]
	body := math.Abs(c.Close - c.Open)
	if body <= 0 {
		return "" // 純十字星:實體為 0,「N×實體」無意義,跳過
	}
	upper := c.High - math.Max(c.Open, c.Close)
	lower := math.Min(c.Open, c.Close) - c.Low
	// 位置脈絡:前 5 根的最高 / 最低
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
	case lower >= pinShadowMul*body && upper <= pinOppMul*body && c.Low < lo:
		return "hammer" // 下插針被買回、出現在局部低點 → 做多
	case upper >= pinShadowMul*body && lower <= pinOppMul*body && c.High > hi:
		return "star" // 上插針被賣回、出現在局部高點 → 做空
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

	s.srmMu.Lock()
	s.srmHits = append(s.srmHits, fresh...)
	if len(s.srmHits) > pinKeep {
		s.srmHits = s.srmHits[len(s.srmHits)-pinKeep:]
	}
	s.srmMu.Unlock()

	for _, h := range fresh {
		s.notifyPin(h)
	}
}

// SRMTF 回傳最近的插針命中(新到舊)。分頁用。
func (s *Store) SRMTF() PinData {
	s.srmMu.Lock()
	defer s.srmMu.Unlock()
	hits := make([]PinHit, len(s.srmHits))
	copy(hits, s.srmHits)
	sort.Slice(hits, func(i, j int) bool { return hits[i].Time > hits[j].Time })
	if len(hits) > pinDisplayMax {
		hits = hits[:pinDisplayMax]
	}
	return PinData{Hits: hits, UpdatedAt: time.Now().Format(time.RFC3339)}
}

// notifyPin 發插針通知(純提示,無下單)。推播對象依分頁權限(同 notifyCloseBook)。
func (s *Store) notifyPin(h PinHit) {
	var emoji, title, body string
	if h.Kind == "hammer" {
		emoji, title = "🔨", h.Coin+" "+h.TF+" 錘子(做多)"
		body = fmt.Sprintf("%s %s 收盤 $%s 出現錘子:下插針被買回、局部低點", h.Coin, h.TF, fmtPx(h.Price))
	} else {
		emoji, title = "☄️", h.Coin+" "+h.TF+" 流星(做空)"
		body = fmt.Sprintf("%s %s 收盤 $%s 出現流星:上插針被賣回、局部高點", h.Coin, h.TF, fmtPx(h.Price))
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
