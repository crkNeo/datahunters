package cache

import (
	"fmt"
	"log"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

// pulsarv17.go — 脈衝星v17:v12 的進場 + 進場濾網(規格:pulsar_v13_filters.md,2026-10-31,
// 依 10/04~10/31 v12 實盤 2,246 筆分析)。
//
// 做法是 v12 的「影子」:v12 每開一筆,就拿那筆進場當下記下的因子過濾網,全部通過才在 v17
// 開一筆一模一樣的單(同幣、同時間、同進場價 / 止損 / 止盈,出場規則也跟 v12 一樣)。
// 所以 v17 永遠是 v12 的子集合,兩本直接對照就知道濾網砍掉的那些單到底好不好。
//
//	A  1h CVD > +13%            不進(主動買盤已經爆完,進去是最後一個買家)
//	B  散戶做多帳戶比 < 0.61     不進(只做被持續做多的幣)
//	C  24h 漲幅 < +3%           不進(只接當天已經走強的幣,不接下跌幣的反抽)
//	D  幣價在 4H EMA200 之下     不進
//	F  BTC 對上一根 15m 收盤 > +0.07%   不進(BTC 剛噴,追山寨 = 追 beta,會回吐)
//	G  BTC 對上一個整點收盤 > +0.2%     不進
//
// 規格書寫 D 是「15m EMA200」,但它分析用的 ema200_dist 欄是系統記的「離 4H EMA200 的 %」
// (regime.go),為了跟分析口徑一致,這裡用 4H。抓不到資料的因子:散戶做多比 = 0 → B 不過
// (保守不進);EMA200 = 0(抓不到)→ D 視為通過(跟分析時一樣)。
// 觀察用,observeOnlyBooks 保證不會跟實單。
const (
	v17MaxCVD1h   = 13.0 // A
	v17MinLong    = 0.61 // B
	v17MinChg24   = 3.0  // C
	v17MinEMA200  = 0.0  // D
	v17MaxBTC15m  = 0.07 // F(%)
	v17MaxBTC1h   = 0.2  // G(%)
	v17Tf         = "15m"
	v17KlineLimit = 200
)

func (s *Store) newPulsarV17Book() *microBook {
	return &microBook{name: "pulsarv17", tf: v17Tf, barSec: 900, klimit: v17KlineLimit, minBars: 40,
		expiry: 16, runnerExpiry: 96, cooldown: 16, keep: 500, plan: tpPulsarV3,
		// 自己不找進場:宇宙給空的、訊號永遠不成立,收盤只管理持倉(逾時 / 收盤止盈止損)。
		universe: func() []string { return nil }, signal: noSignal}
}

func noSignal([]exchange.Candle) (dir string, entry, sl, tp float64, ok bool) { return }

// v17Block 回傳這筆 v12 單被哪幾條濾網擋下(空 = 全部通過)。
func v17Block(t *PaperTrade) []string {
	var why []string
	if t.CVD1h > v17MaxCVD1h {
		why = append(why, fmt.Sprintf("A 1h CVD %.1f%% > %.0f%%", t.CVD1h, v17MaxCVD1h))
	}
	if t.LongAcct < v17MinLong {
		why = append(why, fmt.Sprintf("B 散戶做多 %.2f < %.2f", t.LongAcct, v17MinLong))
	}
	if t.Chg24 < v17MinChg24 {
		why = append(why, fmt.Sprintf("C 24h %.1f%% < %.0f%%", t.Chg24, v17MinChg24))
	}
	if t.Ema200Dist < v17MinEMA200 {
		why = append(why, fmt.Sprintf("D 低於 4H EMA200 %.1f%%", t.Ema200Dist))
	}
	if t.BTC15m > v17MaxBTC15m {
		why = append(why, fmt.Sprintf("F BTC 15m +%.2f%%", t.BTC15m))
	}
	if t.BTC1h > v17MaxBTC1h {
		why = append(why, fmt.Sprintf("G BTC 1h +%.2f%%", t.BTC1h))
	}
	return why
}

// pulsarV17Follow 在 v12 開單(因子已記好)後呼叫:濾網全過就在 v17 開一筆一樣的單。
func (s *Store) pulsarV17Follow(src *PaperTrade) {
	b, v12 := s.pulsarV17Book, s.pulsarV12Book
	if b == nil || v12 == nil || src == nil || !s.StrategyEnabled(b.strat()) {
		return
	}
	v12.mu.Lock()
	c := *src // 因子由 tagEntry 在 v12 的鎖下寫入,這裡在同一把鎖下拷貝
	v12.mu.Unlock()
	if why := v17Block(&c); len(why) > 0 {
		log.Printf("策略[pulsarv17] %s 沒跟 v12:%s", c.Coin, strings.Join(why, "、"))
		return
	}
	now := time.Now().UTC()
	b.mu.Lock()
	for _, t := range b.trades {
		if t.Coin == c.Coin && t.Status == "open" {
			b.mu.Unlock()
			return
		}
	}
	tr := s.openMicroTrade(b, c.Coin, c.Dir, c.Entry, c.SL, c.TP, c.OpenTime, now)
	// 進場因子沿用 v12 那筆(同一時刻、同一組資料)
	tr.OI, tr.CVD, tr.Funding = c.OI, c.CVD, c.Funding
	tr.OI1h, tr.CVD1h, tr.LongAcct = c.OI1h, c.CVD1h, c.LongAcct
	tr.Chg24, tr.SurgeX, tr.BTC1h, tr.Vol24 = c.Chg24, c.SurgeX, c.BTC1h, c.Vol24
	tr.WickPct, tr.EMADist, tr.BarATR, tr.SpikeX, tr.BTC15m = c.WickPct, c.EMADist, c.BarATR, c.SpikeX, c.BTC15m
	tr.Ema200Dist, tr.BtcEma200, tr.Breadth = c.Ema200Dist, c.BtcEma200, c.Breadth
	b.mu.Unlock()
	if s.db != nil {
		s.db.upsertTrade(b.name, tr)
	}
	s.notifyOpenBook(b.name, tr)
}
