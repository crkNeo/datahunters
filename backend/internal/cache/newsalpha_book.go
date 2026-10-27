package cache

import (
	"time"

	"datahunter/internal/exchange"
)

// newsalpha_book.go — 消息雷達模擬單(觀察書,observeOnlyBooks 保證永遠不實單)。
//
// 只有「官方來源 + 可交易類別 + 有幣安永續」的消息才開單,以看到消息當下的永續價進場:
//
//	韓所上幣      做多  止損 5%  止盈 15%
//	交易所上幣    做多  止損 4%  止盈 10%
//	美國券商上架  做多  止損 3%  止盈 8%
//	下架          做空  止損 5%  止盈 15%
//	警示 / 監控   做空  止損 4%  止盈 10%
//	被駭 / 漏洞   做空  止損 4%  止盈 10%
//
// 最多持有 1 小時(消息的反應窗口),同幣平倉後 1 小時內不再開。來源發布時間早於 15 分鐘
// 才被看到的消息(資訊落差已經沒了)只記錄、不開單。這本書是拿來驗證「照規則追消息」到底賺不賺,
// 文件步驟 7 統計出有優勢的類別,才考慮步驟 8 的小倉位實單。

var naTradePlans = map[string]struct{ sl, tp float64 }{
	"listing_kr": {5, 15}, "listing": {4, 10}, "listing_us": {3, 8},
	"delist": {5, 15}, "warning": {4, 10}, "hack": {4, 10},
}

const (
	naHold     = time.Hour
	naCooldown = time.Hour
	naMaxCoins = 3 // 一則公告最多開幾個幣(例如一次下架多個幣)
)

func newNewsAlphaBook() *microBook {
	return &microBook{name: "newsalpha", tf: "5m", barSec: 300, keep: 500,
		signal: func([]exchange.Candle) (string, float64, float64, float64, bool) { return "", 0, 0, 0, false }}
}

// naTrade 依消息開模擬單。pub = 來源發布時間(零值 = 不明,不擋)。
func (s *Store) naTrade(ev *NewsEvent, pub time.Time) {
	b := s.newsBook
	plan, ok := naTradePlans[ev.Category]
	if b == nil || !ok || (ev.Dir != "long" && ev.Dir != "short") || !s.StrategyEnabled(b.strat()) {
		return
	}
	if !pub.IsZero() && time.Since(pub) > naStaleTrade {
		return
	}
	px, err := s.ex.BinanceAllPrices() // 開單一定用當下最新價
	if err != nil {
		return
	}
	now := time.Now().UTC()
	var opened []*PaperTrade
	b.mu.Lock()
	for _, coin := range ev.Coins {
		if len(opened) >= naMaxCoins {
			break
		}
		entry := px[coin+"USDT"]
		if entry <= 0 || naBusy(b, coin, now) {
			continue
		}
		sl, tp := entry*(1-plan.sl/100), entry*(1+plan.tp/100)
		if ev.Dir == "short" {
			sl, tp = entry*(1+plan.sl/100), entry*(1-plan.tp/100)
		}
		opened = append(opened, s.openMicroTrade(b, coin, ev.Dir, roundPx(entry), roundPx(sl), roundPx(tp), now, now))
	}
	b.mu.Unlock()
	if len(opened) == 0 {
		return
	}
	s.news.mu.Lock()
	ev.Traded = true
	s.news.mu.Unlock()
	if s.db != nil {
		s.db.markNewsTraded(ev.ID)
	}
	for _, tr := range opened {
		if s.db != nil {
			s.db.upsertTrade(b.name, tr)
		}
		s.notifyOpenBook(b.name, tr)
	}
}

// naBusy:這個幣已有部位,或平倉不到 1 小時。呼叫端持 b.mu。
func naBusy(b *microBook, coin string, now time.Time) bool {
	for _, tr := range b.trades {
		if tr.Coin != coin {
			continue
		}
		if tr.Status == "open" || (tr.CloseTime != nil && now.Sub(*tr.CloseTime) < naCooldown) {
			return true
		}
	}
	return false
}

// NewsAlphaBookTick 每 15 秒:用幣安全市場價(不限 WS 訂閱的幣)檢查止盈 / 止損 / 逾時。
func (s *Store) NewsAlphaBookTick() {
	b := s.newsBook
	if b == nil {
		return
	}
	b.mu.Lock()
	has := false
	for _, tr := range b.trades {
		if tr.Status == "open" {
			has = true
			break
		}
	}
	b.mu.Unlock()
	if !has {
		return
	}
	px, err := s.ex.BinanceAllPrices()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	var dirty, closed []*PaperTrade
	b.mu.Lock()
	for _, tr := range b.trades {
		if tr.Status != "open" {
			continue
		}
		p := px[tr.Coin+"USDT"]
		if p <= 0 {
			continue
		}
		done := stepTP(tr, p, nil, false, now)
		if !done && now.Sub(tr.OpenTime) >= naHold {
			closeTrade(tr, p, "expired", now)
			done = true
		}
		if done {
			closed = append(closed, tr)
			dirty = append(dirty, tr)
			continue
		}
		tr.Cur = roundPx(p)
		tr.PnLPct = round2(settledPnL(tr, p))
	}
	b.mu.Unlock()
	for _, tr := range dirty {
		if s.db != nil {
			s.db.upsertTrade(b.name, tr)
		}
	}
	for _, tr := range closed {
		s.notifyCloseBook(b.name, tr, now, false)
	}
}

// NewsAlphaBookState 是模擬單分頁的資料(現價由 NewsAlphaBookTick 每 15 秒更新)。
func (s *Store) NewsAlphaBookState() PaperState { return s.microState(s.newsBook) }
