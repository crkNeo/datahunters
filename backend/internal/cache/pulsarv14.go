package cache

import (
	"math"
	"sync"

	"datahunter/internal/exchange"
)

// pulsarv14.go — 脈衝星v14「雙子星」:以 v12(盤中、v3 條件)為基底,多空都做的觀察策略。
//
//	多單腿 pulsarv14 :v12 的盤中進場 + 品質濾網(錢還在進來、還不擁擠、還沒噴完、不是出貨爆量)
//	空單腿 pulsarv14s:爆量急漲後出現「衝高被砸」的長上影爆量 K,下一根確認轉弱 + 擁擠/力竭 → 15m 收盤做空
//
// 兩腿是一個家族:同一個幣同時只會有一個方向、共用一個策略開關(stratKey = pulsarv14)。
// 濾網用的是爆發雷達(OI 堆積)與多空推薦評分器(費率、散戶多空比、OI/CVD)的個別因子,
// 不用評分器總分,也不套流動性打折(改用最低成交額門檻)。
// ⚠️ 門檻沒有歷史數據校準過,是合理起點;進場因子都有記錄(tagEntry),觀察後再調。

const (
	v14MinVolLong  = 5_000_000.0  // 多單:24h 成交額下限(USDT)
	v14MinVolShort = 10_000_000.0 // 空單:24h 成交額下限(空單被軋時要出得掉)
	v14HotFunding  = 0.0005       // 結算費率 ≥ +0.05% = 多頭擁擠
	v14CrowdLong   = 0.70         // 多單:散戶做多比例 ≥ 70% 不追
	v14CrowdShort  = 0.65         // 空單:散戶做多比例 ≥ 65% 算擁擠
	v14MaxChg24    = 30.0         // 多單:24h 已漲 ≥ 30% 不追
	v14BTCLimit    = 2.0          // BTC 1h 跌 > 2% 不做多、漲 > 2% 不做空
)

// newPulsarV14Books 建立雙子星兩條腿(一個家族)。
func (s *Store) newPulsarV14Books() []*microBook {
	long := &microBook{name: "pulsarv14", stratKey: "pulsarv14", tf: "15m", barSec: 900, klimit: 200, minBars: 40,
		expiry: 16, runnerExpiry: 96, cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeBoardCoins,
		signal: surgeV3Signal, intraSignal: surgeV12Signal, gate: s.v14LongGate, tagEntry: true}
	short := &microBook{name: "pulsarv14s", stratKey: "pulsarv14", tf: "15m", barSec: 900, klimit: 200, minBars: 60,
		expiry: 16, runnerExpiry: 48, cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeBoardCoins,
		signal: blowOffShortSignal, gate: s.v14ShortGate, tagEntry: true}
	books := []*microBook{long, short}
	fam := &sync.Mutex{}
	for _, b := range books {
		b.famMu, b.family = fam, books
	}
	return books
}

// v14Tags 抓進場判斷用的因子(與 tagEntry 同一套)。只在訊號已成立後才呼叫,REST 量很小。
func (s *Store) v14Tags(coin string, cs []exchange.Candle) EntryTags {
	btc, _ := s.btc1hRet()
	return ComputeEntryTags(cs, s.oiHist1h(coin, 13), s.lastFunding(coin), s.longShortCached(coin).LongAccount, btc*100, s.surgeXOf(coin))
}

func (s *Store) v14LongGate(coin string, cs []exchange.Candle) bool {
	return v14LongOK(s.v14Tags(coin, cs), cs)
}

func (s *Store) v14ShortGate(coin string, cs []exchange.Candle) bool {
	return v14ShortOK(s.v14Tags(coin, cs))
}

// v14LongOK 多單濾網。抓不到的資料(值為 0)一律不擋,避免外部 API 出問題時整本停擺。
//
//	成交額夠 ｜ 12h OI 沒在減少(錢沒在跑)｜ 費率 < +0.05% ｜ 散戶做多 < 70%
//	24h 漲幅 < 30% ｜ 最近的爆量根不是長上影(出貨爆量)｜ BTC 1h 沒跌超過 2%
func v14LongOK(t EntryTags, cs []exchange.Candle) bool {
	switch {
	case t.Vol24 < v14MinVolLong:
		return false
	case t.OI12h < 0:
		return false
	case t.Funding >= v14HotFunding:
		return false
	case t.LongAcct >= v14CrowdLong:
		return false
	case t.Chg24 >= v14MaxChg24:
		return false
	case t.BTC1h < -v14BTCLimit:
		return false
	case recentBlowOff(cs):
		return false
	}
	return true
}

// v14ShortOK 空單濾網:要「擁擠」也要「力竭」。
//
//	成交額夠 ｜ 擁擠:費率 ≥ +0.05% 或 散戶做多 ≥ 65% ｜ 力竭:近 1h 賣方主導(CVD < 0)且 1h OI 沒再增加
//	BTC 1h 沒漲超過 2%
func v14ShortOK(t EntryTags) bool {
	crowded := t.Funding >= v14HotFunding || t.LongAcct >= v14CrowdShort
	return t.Vol24 >= v14MinVolShort && crowded && t.CVD1h < 0 && t.OI1h <= 0 && t.BTC1h <= v14BTCLimit
}

// recentBlowOff:最後一根之前的 6 根裡,量最大的那根若是爆量(≥ 2.5× 截尾基線)且上影 ≥ 實體
// → 這波是衝高被砸的出貨量,多單不追。(盤中進場時最後一根是還沒收完的,所以不看它。)
func recentBlowOff(cs []exchange.Candle) bool {
	n := len(cs)
	base := trimmedBaseVol(cs)
	if n < 8 || base <= 0 {
		return false
	}
	k := -1
	for i := n - 7; i < n-1; i++ {
		if k < 0 || cs[i].Volume > cs[k].Volume {
			k = i
		}
	}
	c := cs[k]
	upper := c.High - math.Max(c.Open, c.Close)
	return c.Volume >= 2.5*base && upper >= math.Abs(c.Close-c.Open)
}

// blowOffShortSignal — 雙子星空單腿(15m 收盤判斷)。抓 CTR / AIO 那種「爆量衝高被砸」之後的回落。
//
//	① 剛急漲:近 12h(48 根)高低差 ≥ 15%,而且高點出現在最近 8 根內
//	② 出貨爆量:2~6 根前有一根量 ≥ 2.5× 截尾基線、上影 ≥ 實體,而且它就是做出這段高點的那根(差 0.5% 內)
//	③ 確認轉弱:本根收黑、收在爆量根實體中點之下、跌破前一根最低
//	止損:這段最高點 + 0.2×ATR(至少 0.8×ATR);止損超過 8% 不做 ｜ tp = 5R 佔位(1R/2R 分批 + 追蹤)
func blowOffShortSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	const (
		runupBars    = 48
		minRunup     = 0.15
		peakWithin   = 8
		spikeVolMult = 2.5
		spikeFrom    = 6
		spikeTo      = 2
		slBufATR     = 0.2
		slMinATR     = 0.8
		maxSLPct     = 0.08
	)
	n := len(cs)
	if n < 60 {
		return
	}
	atr := robustATR(cs, 14, 2)
	base := trimmedBaseVol(cs)
	if atr <= 0 || base <= 0 {
		return
	}
	hi, lo, hiIdx := cs[n-runupBars].High, cs[n-runupBars].Low, n-runupBars
	for i := n - runupBars; i < n; i++ {
		if cs[i].High >= hi {
			hi, hiIdx = cs[i].High, i
		}
		if cs[i].Low < lo {
			lo = cs[i].Low
		}
	}
	if lo <= 0 || (hi-lo)/lo < minRunup || hiIdx < n-peakWithin {
		return
	}
	k := -1
	for i := n - spikeFrom; i <= n-spikeTo; i++ {
		c := cs[i]
		upper := c.High - math.Max(c.Open, c.Close)
		if c.Volume >= spikeVolMult*base && upper >= math.Abs(c.Close-c.Open) && c.High >= hi*0.995 &&
			(k < 0 || c.Volume > cs[k].Volume) {
			k = i
		}
	}
	if k < 0 {
		return
	}
	last, prev := cs[n-1], cs[n-2]
	mid := (cs[k].Open + cs[k].Close) / 2
	if !(last.Close < last.Open && last.Close < mid && last.Close < prev.Low) {
		return
	}
	price := last.Close
	stopDist := hi + slBufATR*atr - price
	if stopDist < slMinATR*atr {
		stopDist = slMinATR * atr
	}
	if stopDist <= 0 || stopDist/price > maxSLPct {
		return
	}
	return "short", roundPx(price), roundPx(price + stopDist), roundPx(price - 5*stopDist), true
}
