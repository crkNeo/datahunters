package cache

import (
	"math"
	"sync"

	"datahunter/internal/exchange"
)

// pulsarv14.go — 脈衝星v14「雙子星」:照雙子星規劃做的多空觀察策略,兩腿都跟 v12 一樣盤中進場(條件成立就進,不等收 K)。
//
//	選幣:爆量熱名單(爆量 ≥ 3×)
//	多單腿 pulsarv14 :v12(v3 條件)+ 「點火順勢」濾網(新錢在推、還不擁擠、還沒噴完、不是出貨爆量)
//	空單腿 pulsarv14s:「衝高出貨」—— 已收的長上影爆量 K 被現價跌破 + 擁擠/力竭
//
// 兩腿是一個家族:同一個幣同時只會有一個方向、共用一個策略開關(stratKey = pulsarv14)。
// 濾網用的是爆發雷達(OI 堆積)與多空推薦評分器(費率、散戶多空比、OI/CVD)的個別因子,
// 不用評分器總分,也不套流動性打折(改用最低成交額門檻)。
// ⚠️ 門檻沒有歷史數據校準過,是合理起點;進場因子都有記錄(tagEntry),觀察後再調。

const (
	v14MinVolLong  = 5_000_000.0  // 多單:24h 成交額下限(USDT)
	v14MinVolShort = 10_000_000.0 // 空單:24h 成交額下限(空單被軋時要出得掉)
	v14MinOI12h    = 5.0          // 多單:12h OI 增加 ≥ 5%(雷達的 OI 堆積 = 新錢在推)
	v14HotFunding  = 0.0005       // 結算費率 ≥ +0.05% = 多頭擁擠
	v14CrowdLong   = 0.60         // 多單:散戶做多比例 ≥ 60% 不追
	v14CrowdShort  = 0.65         // 空單:散戶做多比例 ≥ 65% 算擁擠
	v14MaxChg24    = 25.0         // 多單:24h 已漲 ≥ 25% 不追
	v14BTCLimit    = 2.0          // BTC 1h 跌 > 2% 不做多、漲 > 2% 不做空
)

// newPulsarV14Books 建立雙子星兩條腿(一個家族)。
func (s *Store) newPulsarV14Books() []*microBook {
	long := &microBook{name: "pulsarv14", stratKey: "pulsarv14", tf: "15m", barSec: 900, klimit: 200, minBars: 40,
		expiry: 16, runnerExpiry: 96, cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeHotCoins,
		signal: surgeV3Signal, intraSignal: surgeV12Signal, gate: s.v14LongGate, tagEntry: true}
	short := &microBook{name: "pulsarv14s", stratKey: "pulsarv14", tf: "15m", barSec: 900, klimit: 200, minBars: 60,
		expiry: 16, runnerExpiry: 48, cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeHotCoins,
		signal: blowOffShortSignal, intraSignal: blowOffShortIntra, gate: s.v14ShortGate, tagEntry: true}
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
	_, _, _, _, pathA := blowOffShortSignal(cs) // 不是 A 就是 B(衝高那根還在跑)
	return v14ShortOK(s.v14Tags(coin, cs), !pathA)
}

// v14LongOK 多單濾網(點火順勢)。「錢在進來」要有憑證(OI/CVD 抓不到就不進);
// 費率、散戶多空比抓不到(值為 0)則不擋。
//
//	成交額 ≥ 500 萬 ｜ 錢在進來:12h OI 增加 ≥ 5% 且 6h CVD 買方主導 ｜ 還不擁擠:費率 < +0.05%、散戶做多 < 60%
//	還沒噴完:24h 漲幅 < 25% ｜ 最近的爆量根不是長上影(出貨爆量)｜ BTC 1h 沒跌超過 2%
func v14LongOK(t EntryTags, cs []exchange.Candle) bool {
	switch {
	case t.Vol24 < v14MinVolLong:
		return false
	case t.OI12h < v14MinOI12h || t.CVD6h <= 0:
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
//
// forming = 路徑 B(衝高那根還在跑):那根裡大多是衝高時的買盤,1h CVD/OI 幾乎不可能已轉弱,
// 所以 B 的「力竭」改由價格本身證明(爆量上影 ≥ 實體、已從高點回落過半,見 blowOffFormingShort),
// 這裡只看擁擠、成交額與 BTC。
func v14ShortOK(t EntryTags, forming bool) bool {
	crowded := t.Funding >= v14HotFunding || t.LongAcct >= v14CrowdShort
	exhausted := forming || (t.CVD1h < 0 && t.OI1h <= 0)
	return t.Vol24 >= v14MinVolShort && crowded && exhausted && t.BTC1h <= v14BTCLimit
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

// 空單共用常數。
const (
	boRunupBars    = 48 // 12h(15m × 48)
	boMinRunup12h  = 0.15
	boMinRunup24h  = 0.25
	boSpikeVolMult = 2.5
	boSlBufATR     = 0.2
	boSlMinATR     = 0.8
	boMaxSLPct     = 0.08
	boDrawback     = 0.5 // 激進路徑:現價從本根高點回落 ≥ 本根全距一半
)

// blowOffShortIntra 是空單腿的盤中進場(每分鐘呼叫,最後一根是還在跑的 K,它的 Close = 現價)。
// 兩條路誰先成立就進:
//
//	A. 爆量長上影那根已經收完(2~6 根前),現價跌破它的實體中點與前兩根最低(blowOffShortSignal)
//	B. 最激進:衝高的那根還在跑就判斷 —— 這根到目前為止已爆量、做出 12h 高點、上影已 ≥ 實體、
//	   現價已從本根高點回落 ≥ 本根全距一半(blowOffFormingShort)。收盤前可能被拉回去,假訊號較多。
func blowOffShortIntra(cs []exchange.Candle, elapsedSec int64) (dir string, entry, sl, tp float64, ok bool) {
	if dir, entry, sl, tp, ok = blowOffShortSignal(cs); ok {
		return
	}
	return blowOffFormingShort(cs)
}

// blowOffRunup 檢查「剛急漲」(最高點比 12h 前收盤高 ≥ 15%,或比 24h 前高 ≥ 25%),
// 成立時回傳近 12h 最高點、截尾 ATR 與截尾基線量。
func blowOffRunup(cs []exchange.Candle) (hi, atr, base float64, ok bool) {
	n := len(cs)
	if n < 60 {
		return
	}
	atr = robustATR(cs, 14, 2)
	base = trimmedBaseVol(cs)
	if atr <= 0 || base <= 0 {
		return
	}
	for i := n - boRunupBars; i < n; i++ {
		hi = math.Max(hi, cs[i].High)
	}
	ago12 := cs[n-1-boRunupBars].Close
	ok = ago12 > 0 && hi/ago12-1 >= boMinRunup12h
	if n > 2*boRunupBars { // 有 24h 資料才看 24h
		if ago24 := cs[n-1-2*boRunupBars].Close; ago24 > 0 && hi/ago24-1 >= boMinRunup24h {
			ok = true
		}
	}
	return
}

// blowOffShortAt 在 price 進空:止損 = 這段最高點 + 0.2×ATR(至少 0.8×ATR),超過 8% 不做;tp = 5R 佔位。
func blowOffShortAt(price, hi, atr float64) (dir string, entry, sl, tp float64, ok bool) {
	stopDist := hi + boSlBufATR*atr - price
	if stopDist < boSlMinATR*atr {
		stopDist = boSlMinATR * atr
	}
	if price <= 0 || stopDist <= 0 || stopDist/price > boMaxSLPct {
		return
	}
	return "short", roundPx(price), roundPx(price + stopDist), roundPx(price - 5*stopDist), true
}

// isBlowOffBar:爆量(≥ 2.5× 基線)、做出這段高點(差 0.5% 內)、上影 ≥ 實體。
func isBlowOffBar(c exchange.Candle, hi, base float64) bool {
	upper := c.High - math.Max(c.Open, c.Close)
	return c.Volume >= boSpikeVolMult*base && c.High >= hi*0.995 && upper >= math.Abs(c.Close-c.Open)
}

// blowOffShortSignal — 雙子星空單腿(路徑 A)。抓 CTR / AIO 那種「爆量衝高被砸」之後的回落。
// 盤中時最後一根是形成中的 K,「本根收盤」= 現價。
//
//	① 剛急漲:最高點比 12h 前的收盤高 ≥ 15%,或比 24h 前高 ≥ 25%
//	② 出貨爆量:2~6 根前(已收)有一根爆量長上影 K,而且它就是做出這段高點的那根
//	③ 確認轉弱:本根收盤(現價)跌破爆量根實體中點,也跌破前兩根的最低點
func blowOffShortSignal(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	hi, atr, base, up := blowOffRunup(cs)
	if !up {
		return
	}
	n := len(cs)
	k := -1
	for i := n - 6; i <= n-2; i++ {
		if isBlowOffBar(cs[i], hi, base) && (k < 0 || cs[i].Volume > cs[k].Volume) {
			k = i
		}
	}
	if k < 0 {
		return
	}
	last := cs[n-1]
	mid := (cs[k].Open + cs[k].Close) / 2
	if !(last.Close < mid && last.Close < math.Min(cs[n-2].Low, cs[n-3].Low)) {
		return
	}
	return blowOffShortAt(last.Close, hi, atr)
}

// blowOffFormingShort — 雙子星空單腿(路徑 B,最激進):衝高的那根還沒收完就判斷。
// 這根到目前為止的實際成交量 ≥ 2.5× 基線、做出 12h 高點、上影已 ≥ 實體、現價已從本根高點回落 ≥ 本根全距一半。
func blowOffFormingShort(cs []exchange.Candle) (dir string, entry, sl, tp float64, ok bool) {
	hi, atr, base, up := blowOffRunup(cs)
	if !up {
		return
	}
	c := cs[len(cs)-1]
	rng := c.High - c.Low
	if rng <= 0 || !isBlowOffBar(c, hi, base) || c.High-c.Close < boDrawback*rng {
		return
	}
	return blowOffShortAt(c.Close, hi, atr)
}
