package cache

import (
	"datahunter/internal/exchange"
)

// orderblock.go — SMC 訂單塊 + 市場結構引擎(移植自 LuxAlgo「Smart Money Concepts」)。
//
// 這是「訂單塊策略」的地基。給一段 K 線,重放 swing pivot → 結構破壞(BOS/CHoCH)→
// 訂單塊,回傳「目前仍未被吃掉(mitigated)」的訂單塊。純函式、無狀態(每次從 K 線重算),
// 上層(掛單/斐波/進出場)再持有跨 tick 狀態。
//
// LuxAlgo 對應:leg()/getCurrentStructure() → pivot;displayStructure() → 結構破壞;
// storeOrdeBlock() → 取 pivot→破壞點之間、波動過濾後的極端 K 當訂單塊;deleteOrderBlocks()
// → 價格穿過即失效。內部訂單塊用 size=5、swing 用 size=50。
//
// 兩個常數對齊 LuxAlgo 預設:
const (
	BULLISH = +1 // 需求 / 做多側
	BEARISH = -1 // 供給 / 做空側

	obInternalSize = 5   // 內部結構的 pivot 尺度
	obSwingSize    = 50  // swing 結構的 pivot 尺度
	obATRPeriod    = 200 // 波動過濾用的 ATR 週期(LuxAlgo: ta.atr(200))
)

// OrderBlock 是一個訂單塊:一根 K 棒的高低區間 + 方向。
type OrderBlock struct {
	Top      float64 `json:"top"`
	Bottom   float64 `json:"bottom"`
	Bias     int     `json:"bias"`   // BULLISH(+1,需求)/ BEARISH(-1,供給)
	BarIndex int     `json:"bar_ix"` // 訂單塊那根 K 的索引(在傳入的 cs 裡)
	BarTime  int64   `json:"bar_ts"`
	Internal bool    `json:"internal"` // true=內部(size5)/ false=swing(size50)
}

// obPivot 對應 LuxAlgo 的 pivot UDT(只留引擎需要的欄位)。
type obPivot struct {
	level    float64
	barIndex int
	crossed  bool
	valid    bool
}

// detectOrderBlocks 重放結構、回傳目前仍有效的訂單塊(最近的在前),最多 keep 個。
// internal=true 用 size5、false 用 size50。mitigateByClose:true 用收盤價判失效(LuxAlgo
// 的 CLOSE 模式),false 用高低點(HIGHLOW 模式,LuxAlgo 預設)。
func detectOrderBlocks(cs []exchange.Candle, internal bool, keep int, mitigateByClose bool) []OrderBlock {
	n := len(cs)
	size := obSwingSize
	if internal {
		size = obInternalSize
	}
	if n < size+2 {
		return nil
	}

	// 波動過濾:高波動 K 交換高低(LuxAlgo parsedHigh/Low),讓訂單塊落在「乾淨」的極端 K。
	atr := atrSeries(cs, obATRPeriod)
	parsedHigh := make([]float64, n)
	parsedLow := make([]float64, n)
	for i := 0; i < n; i++ {
		vol := atr[i]
		if vol <= 0 { // 暖機未完成 → 不做交換
			parsedHigh[i], parsedLow[i] = cs[i].High, cs[i].Low
			continue
		}
		if (cs[i].High - cs[i].Low) >= 2*vol { // 高波動 K → 交換
			parsedHigh[i], parsedLow[i] = cs[i].Low, cs[i].High
		} else {
			parsedHigh[i], parsedLow[i] = cs[i].High, cs[i].Low
		}
	}

	// leg 狀態機:leg(size) 判斷 size 根前那根是不是被後面 size 根確認的 swing 高/低。
	// leg 0=BEARISH_LEG、1=BULLISH_LEG;change→pivot。
	leg := 0
	prevLeg := 0
	var swingHigh, swingLow obPivot
	var obs []OrderBlock // 收集(之後倒序 = 最近在前)

	// 滾動高/低:ta.highest(size)/ta.lowest(size) = 最近 size 根的極值(含當前)。
	windowMax := func(hi bool, end int) float64 { // [end-size+1, end]
		lo := end - size + 1
		if lo < 0 {
			lo = 0
		}
		v := cs[lo].High
		if !hi {
			v = cs[lo].Low
		}
		for j := lo + 1; j <= end; j++ {
			if hi {
				if cs[j].High > v {
					v = cs[j].High
				}
			} else {
				if cs[j].Low < v {
					v = cs[j].Low
				}
			}
		}
		return v
	}

	for i := size; i < n; i++ {
		// LuxAlgo: newLegHigh = high[size] > ta.highest(size);newLegLow = low[size] < ta.lowest(size)
		newLegHigh := cs[i-size].High > windowMax(true, i)
		newLegLow := cs[i-size].Low < windowMax(false, i)
		prevLeg = leg
		if newLegHigh {
			leg = 0 // BEARISH_LEG
		} else if newLegLow {
			leg = 1 // BULLISH_LEG
		}

		// change → pivot 落在 i-size
		if leg != prevLeg {
			if leg == 1 { // 起漲(bullish leg)→ size 前那根是 pivot LOW
				swingLow = obPivot{level: cs[i-size].Low, barIndex: i - size, crossed: false, valid: true}
			} else { // 起跌(bearish leg)→ pivot HIGH
				swingHigh = obPivot{level: cs[i-size].High, barIndex: i - size, crossed: false, valid: true}
			}
		}

		// 結構破壞:收盤 crossover pivotHigh → bullish 破壞 → 存 bullish OB(需求)
		if swingHigh.valid && !swingHigh.crossed && cs[i].Close > swingHigh.level && cs[i-1].Close <= swingHigh.level {
			swingHigh.crossed = true
			if ob, ok := extractOB(cs, parsedHigh, parsedLow, swingHigh.barIndex, i, BULLISH, internal); ok {
				obs = append(obs, ob)
			}
		}
		// 收盤 crossunder pivotLow → bearish 破壞 → 存 bearish OB(供給)
		if swingLow.valid && !swingLow.crossed && cs[i].Close < swingLow.level && cs[i-1].Close >= swingLow.level {
			swingLow.crossed = true
			if ob, ok := extractOB(cs, parsedHigh, parsedLow, swingLow.barIndex, i, BEARISH, internal); ok {
				obs = append(obs, ob)
			}
		}
	}

	// mitigation:被價格穿過的訂單塊移除(用整段 K 線之後的走勢判定)。
	active := obs[:0]
	for _, ob := range obs {
		mit := false
		for j := ob.BarIndex + 1; j < n; j++ {
			if ob.Bias == BULLISH { // 需求塊:跌破底部即失效
				src := cs[j].Low
				if mitigateByClose {
					src = cs[j].Close
				}
				if src < ob.Bottom {
					mit = true
					break
				}
			} else { // 供給塊:突破頂部即失效
				src := cs[j].High
				if mitigateByClose {
					src = cs[j].Close
				}
				if src > ob.Top {
					mit = true
					break
				}
			}
		}
		if !mit {
			active = append(active, ob)
		}
	}

	// 最近的在前,截到 keep 個。
	out := make([]OrderBlock, 0, len(active))
	for i := len(active) - 1; i >= 0; i-- {
		out = append(out, active[i])
		if keep > 0 && len(out) >= keep {
			break
		}
	}
	return out
}

// extractOB 取 [from,to] 之間 parsed 極端的那根 K 當訂單塊。
// BULLISH:最低 parsedLow 那根(需求);BEARISH:最高 parsedHigh 那根(供給)。
func extractOB(cs []exchange.Candle, parsedHigh, parsedLow []float64, from, to, bias int, internal bool) (OrderBlock, bool) {
	if from < 0 || from >= to || to >= len(cs) {
		return OrderBlock{}, false
	}
	idx := from
	if bias == BULLISH {
		best := parsedLow[from]
		for j := from; j <= to; j++ {
			if parsedLow[j] < best {
				best, idx = parsedLow[j], j
			}
		}
	} else {
		best := parsedHigh[from]
		for j := from; j <= to; j++ {
			if parsedHigh[j] > best {
				best, idx = parsedHigh[j], j
			}
		}
	}
	return OrderBlock{
		Top:      parsedHigh[idx],
		Bottom:   parsedLow[idx],
		Bias:     bias,
		BarIndex: idx,
		BarTime:  cs[idx].Ts,
		Internal: internal,
	}, true
}

// ── 訂單塊斐波策略:進場訊號 ──────────────────────────────────────────────
//
// 邏輯(使用者定義):用 LuxAlgo 訂單塊拉斐波,回撤 0.142–0.382 區間 + 頭槌/射擊之星進場。
//
//	多:fib0 = 訂單塊下緣、fib1 = 訂單塊之後的最高高點(自動 re-anchor:新高就把 fib1 移過去,
//	     fib0 不動)。進場區 = [fib0+0.142r, fib0+0.382r](靠近訂單塊)。最後一根是「頭槌」且
//	     其低點落在區間 → 收盤進場。止損 = fib-0.13(fib0-0.13r);最終 TP = fib2.0。
//	空:對稱 —— fib0 = 訂單塊上緣、fib1 = 之後的最低低點;射擊之星;止損 = fib0+0.13r;TP = fib0-2r。
//
// 內部(size5)與 swing(size50)訂單塊都用;同時挑「方向相符、最近(BarIndex 最大)」的那個。
//
// 這是無狀態訊號:每根收盤都從 K 線重算 fib(含 re-anchor),接 microrev 掛單框架 —— 位置到 +
// 型態成立就進場。分批四段止盈交給 smcFibTPLevels + stepTP。
const (
	smcFibEntryLo     = 0.142 // 訂單塊(v1)進場區下界(靠訂單塊那側)
	smcFibEntryHi     = 0.382 // 訂單塊(v1)進場區上界
	smcFibV2EntryLo   = 0.0   // 訂單塊v2 進場區下界(更深,貼近訂單塊邊緣)
	smcFibV2EntryHi   = 0.236 // 訂單塊v2 進場區上界
	smcFibSL          = 0.13  // 止損:fib-0.13
	smcFibFinalTP     = 1.618 // 最終目標:fib1.618(三段止盈 0.618 / 1.13 / 1.618)
	smcFibMaxRangePct = 10.0  // 斐波 0→1 距離(r/fib0)上限%;超過代表波段過大、目標過度延伸 → 不進場
)

// smcFibSignal(v1)進場區 0.142–0.382;smcFibSignalV2 進場區 0–0.236(更深),其餘完全相同
// (同樣要影線落在區間 + 頭槌/射擊之星反轉型態、同 TP/SL)。
func smcFibSignal(cs []exchange.Candle) (string, float64, float64, float64, bool) {
	return smcFibSignalZone(cs, smcFibEntryLo, smcFibEntryHi)
}

func smcFibSignalV2(cs []exchange.Candle) (string, float64, float64, float64, bool) {
	return smcFibSignalZone(cs, smcFibV2EntryLo, smcFibV2EntryHi)
}

func smcFibSignalZone(cs []exchange.Candle, entryLo, entryHi float64) (dir string, entry, sl, tp float64, ok bool) {
	n := len(cs)
	if n < obSwingSize+5 {
		return
	}
	kind := pinKind(cs) // 最後一根:hammer(做多)/ star(做空)/ ""
	if kind == "" {
		return
	}
	last := cs[n-1]

	wantBias := BULLISH
	if kind == "star" {
		wantBias = BEARISH
	}
	// 內部 + swing 訂單塊,挑方向相符、最近的那個
	obs := detectOrderBlocks(cs, true, 5, false)
	obs = append(obs, detectOrderBlocks(cs, false, 5, false)...)
	best := -1
	for i := range obs {
		if obs[i].Bias != wantBias {
			continue
		}
		if best < 0 || obs[i].BarIndex > obs[best].BarIndex {
			best = i
		}
	}
	if best < 0 {
		return
	}
	ob := obs[best]

	if wantBias == BULLISH {
		fib0 := ob.Bottom // 訂單塊下緣
		fib1 := ob.Top
		for j := ob.BarIndex; j < n; j++ { // fib1 = 訂單塊之後的最高高點(自動 re-anchor)
			if cs[j].High > fib1 {
				fib1 = cs[j].High
			}
		}
		r := fib1 - fib0
		if r <= 0 {
			return
		}
		if r/fib0*100 > smcFibMaxRangePct { // 斐波 0→1 距離 >10% → 波段過大,不進場
			return
		}
		zoneLo := fib0 + entryLo*r
		zoneHi := fib0 + entryHi*r
		if last.Low < zoneLo || last.Low > zoneHi { // 頭槌低點要落在回撤區
			return
		}
		entry = roundPx(last.Close)
		sl = roundPx(fib0 - smcFibSL*r)
		tp = roundPx(fib0 + smcFibFinalTP*r)
		if entry <= sl || tp <= entry {
			return
		}
		return "long", entry, sl, tp, true
	}

	// 做空(對稱)
	fib0 := ob.Top // 訂單塊上緣
	fib1 := ob.Bottom
	for j := ob.BarIndex; j < n; j++ { // fib1 = 之後的最低低點
		if cs[j].Low < fib1 {
			fib1 = cs[j].Low
		}
	}
	r := fib0 - fib1
	if r <= 0 {
		return
	}
	if r/fib0*100 > smcFibMaxRangePct { // 斐波 0→1 距離 >10% → 波段過大,不進場
		return
	}
	zoneHi := fib0 - entryLo*r
	zoneLo := fib0 - entryHi*r
	if last.High > zoneHi || last.High < zoneLo { // 射擊之星高點要落在回撤區
		return
	}
	entry = roundPx(last.Close)
	sl = roundPx(fib0 + smcFibSL*r)
	tp = roundPx(fib0 - smcFibFinalTP*r)
	if entry >= sl || tp >= entry {
		return
	}
	return "short", entry, sl, tp, true
}

// smcFibTPLevels 從 sl(fib-0.13)與 finalTP(fib1.618)還原斐波格,回傳分批位
// TP1=fib0.618、TP2=fib1.13、TP3=0(不用第三段,tr.TP=finalTP=fib1.618 就是最終段)。
// 三段止盈 0.618 / 1.13 / 1.618,分批 40/30/30 由 stratDefaults 驅動。
func smcFibTPLevels(entry, sl, finalTP float64) (tp1, tp2, tp3 float64) {
	if finalTP > entry { // 多:sl=fib0-0.13r、finalTP=fib0+1.618r → finalTP-sl=1.748r
		r := (finalTP - sl) / (smcFibFinalTP + smcFibSL)
		fib0 := sl + smcFibSL*r
		return roundPx(fib0 + 0.618*r), roundPx(fib0 + 1.13*r), 0
	}
	// 空:sl=fib0+0.13r、finalTP=fib0-1.618r → sl-finalTP=1.748r
	r := (sl - finalTP) / (smcFibFinalTP + smcFibSL)
	fib0 := sl - smcFibSL*r
	return roundPx(fib0 - 0.618*r), roundPx(fib0 - 1.13*r), 0
}
