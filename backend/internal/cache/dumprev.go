package cache

import (
	"datahunter/internal/exchange"
)

// dumprev.go — 流星(爆量崩盤反轉,Dump Reversal)。規格:dumprev_deploy.md v1.0(2026-10-30)。
//
// 事件型「買崩」衛星策略:山寨幣某根 1h 爆出巨量長黑(強平瀑布)後,流動性真空常有反彈。
//
//	觸發(1h 收盤)  該根成交量 ≥ 前 50 根均量 × 10,收陰,且實體跌幅 ≥ 1%
//	進場            觸發那根收盤價買入(做多)
//	出場            進場後 48 小時整,收盤價平倉(純計時)
//	價格止損 / 止盈 無 —— 回測顯示任何價格止損都把反彈接成虧損;獲利在右尾,不能提前停利
//	同幣冷卻        平倉後 24 小時內不再進
//	幣池            銀河 top-80,排除 BTC / ETH / SOL / BNB(主流崩了會續跌,效應不存在)
//	同時持有        最多 3 筆(崩盤日常一次很多幣一起崩)
//
// 回測(規格書):超額報酬 +2~4% / 事件,中位 +0.7%,勝率約 52%,p95 +28.6%、p05 −16.9%、最壞 −76%。
// ⚠️ 無止損:單筆可能大虧,風險只靠倉位與同時持有上限控制。觀察用,observeOnlyBooks 保證不實單。
const (
	drVolX      = 10.0 // 成交量倍數門檻
	drVolLook   = 50   // 均量看前幾根
	drMinDrop   = 1.0  // 實體跌幅門檻 %
	drHoldBars  = 48   // 持有幾根 1h
	drCoolBars  = 24   // 平倉後冷卻幾根 1h
	drMaxOpen   = 3    // 同時持有上限
	drMinBars   = drVolLook + 2
	drKlineSize = 60
)

// drExcluded:主流幣不做(崩了會續跌)。
var drExcluded = map[string]bool{"BTC": true, "ETH": true, "SOL": true, "BNB": true}

func (s *Store) newDumpRevBook() *microBook {
	b := &microBook{name: "dumprev", tf: "1h", barSec: 3600, klimit: drKlineSize, minBars: drMinBars,
		expiry: drHoldBars, cooldown: drCoolBars, keep: 500, signal: dumpRevSignal, signalWhy: dumpRevWhy}
	b.universe = s.dumpRevUniverse
	b.gate = func(string, []exchange.Candle) bool { return dumpRevRoom(b) } // microRun 持 b.mu 時呼叫
	return b
}

// dumpRevUniverse = 銀河 top-80 扣掉主流幣。
func (s *Store) dumpRevUniverse() []string {
	var out []string
	for _, c := range s.emaCoins() {
		if !drExcluded[c] {
			out = append(out, c)
		}
	}
	return out
}

// dumpRevRoom:同時持有未滿 3 筆才能再開。呼叫端已持 b.mu。
func dumpRevRoom(b *microBook) bool {
	n := 0
	for _, tr := range b.trades {
		if tr.Status == "open" {
			n++
		}
	}
	return n < drMaxOpen
}

// dumpRevSignal 看最新一根已收的 1h:爆量 ≥ 10 倍 + 收陰 + 實體跌幅 ≥ 1% → 以收盤價做多。
// 沒有價格止損 / 止盈(SL、TP 給 0),只靠 expiry 48 根計時出場。
func dumpRevSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	if !dumpRevHit(cs) {
		return
	}
	return "long", cs[len(cs)-1].Close, 0, 0, true
}

func dumpRevHit(cs []exchange.Candle) bool {
	n := len(cs)
	if n < drMinBars {
		return false
	}
	last := cs[n-1]
	if last.Open <= 0 || last.Close >= last.Open || (last.Open-last.Close)/last.Open*100 < drMinDrop {
		return false
	}
	base := 0.0
	for _, c := range cs[n-1-drVolLook : n-1] {
		base += c.Volume
	}
	base /= drVolLook
	return base > 0 && last.Volume >= drVolX*base
}

// dumpRevWhy 給 log 的子原因(診斷用)。
func dumpRevWhy(cs []exchange.Candle) string {
	n := len(cs)
	if n < drMinBars {
		return "K線不足"
	}
	last := cs[n-1]
	if last.Close >= last.Open {
		return "非陰線"
	}
	if (last.Open-last.Close)/last.Open*100 < drMinDrop {
		return "跌幅<1%"
	}
	return "量不足10倍"
}
