package cache

import (
	"fmt"
	"log"
	"time"

	"datahunter/internal/exchange"
)

// volbrk.go — 波動率突破(只做多版)。規則照回測程式碼:
//
//	每天一輪,以 UTC 日為準(台北早上 8:00 換日)
//	兩條線   上緣 = 今日開盤 + 0.5 × 前一日振幅(高 − 低);下緣 = 今日開盤 − 0.5 × 前一日振幅
//	趨勢過濾 前一日收盤 > 20 日均線(用到前一日為止的收盤)才做多,否則今天不做
//	進場     用 1h K 判斷:碰到上緣就以上緣買進(那根開盤就在上緣之上 → 以開盤價成交);每天最多一筆
//	         同一根 1h 同時碰到上緣和下緣(方向不明)、或先碰到下緣 → 今天不再交易
//	出場     UTC 換日那一刻(當日收盤價)平倉,不留倉過夜;沒有停損、沒有止盈
//
// 實作:每小時收盤後,拿今天已收的 1h K 從頭重播一次,決定今天是「還在等 / 已成交 / 不做了」。
// 伺服器開機那一小時之前就碰到上緣的,當天算「錯過」:不補記成交(那時系統沒在跑,實盤也進不了),
// 也不再顯示待觸發;開機前已經成交、存在 DB 的單照常管到換日平倉。還在等的幣在頁面上顯示成「待觸發」
// (觸發價 = 上緣,只放記憶體、每小時重算,不寫 DB)。觀察用,observeOnlyBooks 保證不會跟實單。
const (
	vbK      = 0.5 // 上 / 下緣 = 開盤 ± 0.5 × 前一日振幅
	vbMA     = 20  // 趨勢過濾:20 日均線
	vbDayMs  = 86400_000
	vbDailyN = 25
)

func (s *Store) newVolBrkBook() *microBook {
	b := &microBook{name: "volbrk", tf: "1d", barSec: 86400, klimit: vbDailyN, keep: 500, wideMark: true}
	b.universe = s.wideUniverse
	return b
}

// vbPlan 是某一天的兩條線。
type vbPlan struct {
	Day          int64 // UTC 日開始(ms)
	Upper, Lower float64
}

// vbPlanOf 由日 K 算出 day 這天的兩條線。ready=false:今天的日 K 還沒生成 / 天數不夠(之後再試);
// trend=false:前一日收盤沒站上 20 日均線,今天不做多。
func vbPlanOf(daily []exchange.Candle, day int64) (p vbPlan, trend, ready bool) {
	d := -1
	for i, c := range daily {
		if c.Ts == day {
			d = i
		}
	}
	if d < vbMA {
		return p, false, false
	}
	prev := daily[d-1]
	sma := 0.0
	for _, c := range daily[d-vbMA : d] {
		sma += c.Close
	}
	sma /= vbMA
	rng := prev.High - prev.Low
	o := daily[d].Open
	p = vbPlan{Day: day, Upper: o + vbK*rng, Lower: o - vbK*rng}
	return p, prev.Close > sma, true
}

const (
	vbWait   = iota // 還沒碰到任何一條線
	vbFilled        // 碰到上緣,已成交
	vbDead          // 同一根碰到兩條線 / 先碰下緣 → 今天不做
)

// vbReplay 拿 day 這天已收的 1h K 依序重播。成交價 = 上緣(那根開盤就在上緣之上 → 開盤價),at = 那根的開盤時間。
func vbReplay(hours []exchange.Candle, p vbPlan) (state int, px float64, at int64) {
	for _, c := range hours {
		if c.Ts < p.Day || c.Ts >= p.Day+vbDayMs {
			continue
		}
		up, down := c.High >= p.Upper, c.Low <= p.Lower
		switch {
		case up && down, down:
			return vbDead, 0, c.Ts
		case up:
			return vbFilled, max(p.Upper, c.Open), c.Ts
		}
	}
	return vbWait, 0, 0
}

// vbDayClose 找 day 那天的收盤價:日 K 有就用日 K,不然用那天最後一根 1h 的收盤。
func vbDayClose(daily, hours []exchange.Candle, day int64) (float64, bool) {
	for _, c := range daily {
		if c.Ts == day && c.Close > 0 {
			return c.Close, true
		}
	}
	for _, c := range hours {
		if c.Ts == day+vbDayMs-dcBarMs && c.Close > 0 {
			return c.Close, true
		}
	}
	return 0, false
}

func (s *Store) volBrkCoins() []string {
	b := s.volBrkBook
	coins := b.universe()
	seen := map[string]bool{}
	for _, c := range coins {
		seen[c] = true
	}
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.Status == "open" && !seen[tr.Coin] { // 掉出宇宙的持倉幣也要收盤平倉
			seen[tr.Coin] = true
			coins = append(coins, tr.Coin)
		}
	}
	b.mu.Unlock()
	return coins
}

// VolBrkTick 每分鐘由 main 呼叫:整點收盤後處理換日平倉、今天的觸價進場與待觸發計畫。
func (s *Store) VolBrkTick() {
	b := s.volBrkBook
	if b == nil {
		return
	}
	now := time.Now().UTC()
	coins := s.vbEng.due(now, s.volBrkCoins)
	day := now.UnixMilli() / vbDayMs * vbDayMs
	enabled := s.StrategyEnabled(b.strat())
	opened, closedN := 0, 0
	for _, coin := range coins {
		hours, _, ok := s.hourBars(coin, now)
		if !ok {
			continue
		}
		v, err := s.candleCache.get(fmt.Sprintf("%s|1d|%d", coin, vbDailyN), func() (any, error) {
			return s.ex.BinanceKlines(coin+"USDT", "1d", vbDailyN)
		})
		if err != nil {
			continue
		}
		daily := v.([]exchange.Candle)
		plan, trend, ready := vbPlanOf(daily, day)

		var exits, entries, dirty []*PaperTrade
		b.mu.Lock()
		kept := b.trades[:0]
		for _, tr := range b.trades {
			if tr.Coin == coin && tr.Status == "pending" {
				continue // 待觸發計畫每小時重算
			}
			if tr.Coin == coin && tr.Status == "open" {
				d := tr.OpenTime.UnixMilli() / vbDayMs * vbDayMs
				var dayBars []exchange.Candle
				for _, c := range hours {
					if c.Ts >= d && c.Ts < d+vbDayMs {
						dayBars = append(dayBars, c)
					}
				}
				before := tr.MaxGain
				barMaxGain(tr, dayBars, dcBarMs)
				if d < day { // 換日了 → 用那天的收盤價平倉
					if px, ok := vbDayClose(daily, hours, d); ok {
						closeTrade(tr, roundPx(px), "eod", time.UnixMilli(d+vbDayMs).UTC())
						exits = append(exits, tr)
					}
				} else if tr.MaxGain != before {
					dirty = append(dirty, tr)
				}
			}
			kept = append(kept, tr)
		}
		b.trades = kept
		if ready && trend && enabled {
			id := fmt.Sprintf("volbrk|%s|%d", coin, day)
			exists := false
			for _, tr := range b.trades {
				if tr.ID == id {
					exists = true
					break
				}
			}
			if !exists {
				state, px, at := vbReplay(hours, plan)
				if state == vbFilled && at < s.vbEng.start/dcBarMs*dcBarMs {
					state = vbDead // 開機前就觸價了 → 錯過,不補記
				}
				switch state {
				case vbFilled:
					tr := &PaperTrade{ID: id, Coin: coin, Dir: "long", Entry: roundPx(px), Cur: roundPx(px),
						Status: "open", OpenTime: time.UnixMilli(at).UTC()}
					barMaxGain(tr, hours, dcBarMs)
					b.trades = append(b.trades, tr)
					entries = append(entries, tr)
				case vbWait:
					cur := plan.Upper
					if n := len(hours); n > 0 {
						cur = hours[n-1].Close
					}
					b.trades = append(b.trades, &PaperTrade{ID: id + "|plan", Coin: coin, Dir: "long",
						Entry: roundPx(plan.Upper), Cur: roundPx(cur), Status: "pending", OpenTime: time.UnixMilli(day).UTC()})
				}
			}
		}
		if len(exits) > 0 {
			microTrim(b)
		}
		b.mu.Unlock()
		if ready {
			delete(s.vbEng.pend, coin) // 今天的日 K 還沒生成 → 留著,下一分鐘再試(換日平倉已做的不會重做)
		}
		for _, tr := range append(append(exits, entries...), dirty...) {
			if s.db != nil {
				s.db.upsertTrade(b.name, tr)
			}
		}
		for _, tr := range exits {
			s.notifyCloseBook(b.name, tr, now, false)
		}
		latest := int64(0)
		if n := len(hours); n > 0 {
			latest = hours[n-1].Ts
		}
		for _, tr := range entries {
			if !s.vbEng.boot && tr.OpenTime.UnixMilli() == latest { // 補進來的舊成交不推播
				s.notifyOpenBook(b.name, tr)
			}
		}
		opened += len(entries)
		closedN += len(exits)
		time.Sleep(25 * time.Millisecond)
	}
	if opened+closedN > 0 {
		log.Printf("策略[volbrk] 進場 %d 筆、換日平倉 %d 筆", opened, closedN)
	}
}

// VolBrkMarkTick 每 20 秒:用即時價更新持倉的現價 / 最大獲利,和待觸發計畫的現價(不出場)。
func (s *Store) VolBrkMarkTick() {
	b := s.volBrkBook
	if b == nil {
		return
	}
	px := s.fillOpenPx(s.livePrices(), b)
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tr := range b.trades {
		p := px[tr.Coin]
		if p <= 0 || (tr.Status != "open" && tr.Status != "pending") {
			continue
		}
		tr.Cur = roundPx(p)
		if tr.Status == "open" {
			if g := pnl(tr.Dir, tr.Entry, p); g > tr.MaxGain {
				tr.MaxGain = round2(g)
			}
		}
	}
}
