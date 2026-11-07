package cache

import (
	"fmt"
	"math"

	"datahunter/internal/exchange"
)

// patterns_detect.go — 型態掃描的 8 種看漲型態規則(看跌型態 = 把 K 線上下翻轉後套同一套,見 patterns.go)。
// 每種型態列出幾個分項(0~100 分),加權平均 = 相似度;另外有幾條「硬條件」不符合就直接不算。
// 價差都換成 ATR 倍數,不同價位 / 波動的幣才能放在一起比。

func splitHL(ps []pvt) (hs, ls []pvt) {
	for _, p := range ps {
		if p.hi {
			hs = append(hs, p)
		} else {
			ls = append(ls, p)
		}
	}
	return
}

// orderFrac:相鄰轉折點裡,有幾成是照方向排(down=true:一個比一個低)。
func orderFrac(ps []pvt, down bool) float64 {
	if len(ps) < 2 {
		return 0
	}
	ok := 0
	for i := 1; i < len(ps); i++ {
		if (down && ps[i].p < ps[i-1].p) || (!down && ps[i].p > ps[i-1].p) {
			ok++
		}
	}
	return float64(ok) / float64(len(ps)-1)
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

// finish 把分項的加權幾何平均換回 0~100 分。
func (c *pcand) finish(wsum float64) *pcand {
	if wsum > 0 {
		c.score = 100 * math.Exp(c.score/wsum)
	}
	return c
}

// priorRise:型態起點(第 at 根,價格 p)比 look 根前的收盤「淨」漲了多少(ATR、%)。
// 用淨變化、不用區間最低點:不然型態自己的第一段走勢會被誤當成「前段趨勢」。
func (x *pctx) priorRise(at int, p float64, look int) (atrs, pc float64) {
	base := x.cs[max(0, at-look)].Close
	return (p - base) / x.atr, pct(p-base, base)
}

// priorFall:型態起點比 look 根前的收盤淨跌了多少。
func (x *pctx) priorFall(at int, p float64, look int) (atrs, pc float64) {
	base := x.cs[max(0, at-look)].Close
	return (base - p) / x.atr, pct(base-p, base)
}

// priorLook:看型態起點前多少根判斷前段趨勢(跟型態差不多長)。
func priorLook(length int) int { return clampInt(length, 15, 80) }

// volContract:型態後 1/3 跟前 1/3 比,K 棒振幅與成交量各縮了多少(比值,< 1 = 縮)。
func (x *pctx) volContract(from int) (rangeRatio, volRatio float64) {
	n := x.n - from
	if n < 6 {
		return 1, 1
	}
	third := n / 3
	avg := func(a, b int, f func(exchange.Candle) float64) float64 {
		s := 0.0
		for i := a; i < b; i++ {
			s += f(x.cs[i])
		}
		return s / float64(b-a)
	}
	rg := func(c exchange.Candle) float64 { return c.High - c.Low }
	vol := func(c exchange.Candle) float64 { return c.Volume }
	r1, r3 := avg(from, from+third, rg), avg(x.n-third, x.n, rg)
	v1, v3 := avg(from, from+third, vol), avg(x.n-third, x.n, vol)
	rangeRatio, volRatio = 1, 1
	if r1 > 0 {
		rangeRatio = r3 / r1
	}
	if v1 > 0 {
		volRatio = v3 / v1
	}
	return
}

// ---- 看漲對稱三角 / 看漲下降楔形(兩條收斂的趨勢線)----

func detTriangle(x *pctx, piv []pvt) *pcand { return detConverge(x, piv, false) }
func detWedge(x *pctx, piv []pvt) *pcand    { return detConverge(x, piv, true) }

func detConverge(x *pctx, piv []pvt, wedge bool) *pcand {
	var best *pcand
	e := float64(x.n - 1)
	for m := 4; m <= 8 && m <= len(piv); m++ {
		ps := piv[len(piv)-m:]
		hs, ls := splitHL(ps)
		if len(hs) < 2 || len(ls) < 2 || !ps[0].hi {
			continue // 要從一個高點開始(前段趨勢的終點)
		}
		from, last := ps[0].i, ps[m-1].i
		length := x.n - 1 - from
		if length < ptMinBars || length > ptMaxBars || !x.recent(last, length) {
			continue
		}
		up, lo := fitLine(hs), fitLine(ls)
		L := float64(length)
		du, dl := up.slope*L/x.atr, lo.slope*L/x.atr // 兩條線在整段裡各走了幾個 ATR
		f := float64(from)
		w0, w1 := up.at(f)-lo.at(f), up.at(e)-lo.at(e)
		if w0 < 1.5*x.atr || w1 <= 0 || w1 >= w0 {
			continue // 太窄、已經交叉、或沒有收斂
		}
		if wedge {
			if !(du < 0 && dl < 0 && du < dl) {
				continue // 下降楔形:兩條都往下,上緣比下緣陡(所以收斂)
			}
		} else if !(du < 0 && dl > 0) {
			continue // 對稱三角:上緣往下、下緣往上
		}
		state, ok := x.breakState(last, up.at)
		if !ok || x.c < lo.at(e)-0.5*x.atr {
			continue
		}
		c := &pcand{start: from, piv: ps, state: state, level: up.at(e), target: up.at(e) + w0,
			lines: []PatternLine{segLine(up, f, e), segLine(lo, f, e)}}
		var ws float64
		if !wedge {
			look := priorLook(length)
			a, pc := x.priorRise(hs[0].i, hs[0].p, look)
			c.part(x.w("前段上漲", "前段下跌"), ramp(a, 1.5, 5), 1.2,
				fmt.Sprintf("進入三角前 %d 根%s %.1f%%(%.1f ATR)", look, x.w("漲", "跌"), pc, a), &ws)
		} else {
			tilt := -(du + dl) / 2
			c.part(x.w("整段向下傾斜", "整段向上傾斜"), ramp(tilt, 0.5, 3), 1,
				fmt.Sprintf("上下兩條線都往%s,平均走了 %.1f ATR", x.w("下", "上"), tilt), &ws)
		}
		fh := orderFrac(hs, true)
		c.part(x.w("高點越來越低", "低點越來越高"), 0.5*fh*100+0.5*ramp(-du, 0.3, 2), 1.2,
			fmt.Sprintf("%d 個%s點,%.0f%% 依序%s", len(hs), x.w("高", "低"), fh*100, x.w("降低", "墊高")), &ws)
		if !wedge {
			fl := orderFrac(ls, false)
			c.part(x.w("低點越來越高", "高點越來越低"), 0.5*fl*100+0.5*ramp(dl, 0.3, 2), 1.2,
				fmt.Sprintf("%d 個%s點,%.0f%% 依序%s", len(ls), x.w("低", "高"), fl*100, x.w("墊高", "降低")), &ws)
		} else {
			fl := orderFrac(ls, true)
			c.part(x.w("低點越來越低", "高點越來越高"), 0.5*fl*100+0.5*ramp(-dl, 0.3, 2), 1.2,
				fmt.Sprintf("%d 個%s點,%.0f%% 依序%s", len(ls), x.w("低", "高"), fl*100, x.w("降低", "墊高")), &ws)
		}
		ratio := w1 / w0
		conv := ramp(ratio, 0.9, 0.35)
		if !wedge {
			sym := math.Min(-du, dl) / math.Max(-du, dl)
			conv = (conv + ramp(sym, 0.15, 0.5)) / 2
		}
		apex := f + w0/(lo.slope-up.slope) - e
		c.part("兩線收斂", conv, 1, fmt.Sprintf("寬度縮到起點的 %.0f%%,約 %.0f 根後交會", ratio*100, apex), &ws)
		res := (lineResid(up, hs) + lineResid(lo, ls)) / 2 / x.atr
		fit := ramp(res, 1.0, 0.15)
		if len(hs) < 3 || len(ls) < 3 {
			fit = math.Min(fit, 65) // 一邊只有 2 點,線一定剛好穿過,貼合度看不出來
		}
		c.part("貼合趨勢線", fit, 1, fmt.Sprintf("轉折點平均離線 %.2f ATR", res), &ws)
		c.part("觸線次數", ramp(float64(len(hs)+len(ls)), 3, 6), 0.8, fmt.Sprintf("上緣 %d 個、下緣 %d 個轉折點", len(hs), len(ls)), &ws)
		in, tot := 0, 0
		for i := from; i <= last; i++ {
			fi := float64(i)
			if cl := x.cs[i].Close; cl >= lo.at(fi)-0.3*x.atr && cl <= up.at(fi)+0.3*x.atr {
				in++
			}
			tot++
		}
		c.part("價格在線內", ramp(float64(in)/float64(tot), 0.75, 0.97), 0.8,
			fmt.Sprintf("%.0f%% 的收盤在兩條線之間", float64(in)/float64(tot)*100), &ws)
		rr, vr := x.volContract(from)
		c.part("波動 / 量縮", (ramp(rr, 1.0, 0.5)+ramp(vr, 1.1, 0.6))/2, 0.6,
			fmt.Sprintf("後段 K 棒振幅是前段的 %.0f%%,量是 %.0f%%", rr*100, vr*100), &ws)
		best = better(best, c.finish(ws))
	}
	return best
}

// ---- 雙重底 ----

func detDoubleBottom(x *pctx, piv []pvt) *pcand {
	var best *pcand
	e := float64(x.n - 1)
	for _, j := range []int{len(piv) - 1, len(piv) - 2} {
		if j < 2 {
			continue
		}
		l1, h, l2 := piv[j-2], piv[j-1], piv[j]
		if l1.hi || !h.hi || l2.hi {
			continue
		}
		length, gap := x.n-1-l1.i, l2.i-l1.i
		if length < ptMinBars || length > ptMaxBars || gap < 5 || !x.recent(l2.i, length) {
			continue
		}
		low := math.Min(l1.p, l2.p)
		depth := (h.p - math.Max(l1.p, l2.p)) / x.atr
		diff := math.Abs(l1.p-l2.p) / x.atr
		if depth < 1 || diff > 2 || x.minLow(l2.i+1, x.n) < low-0.3*x.atr {
			continue // 中間沒反彈、兩底差太多、或之後跌破底
		}
		neck := func(float64) float64 { return h.p }
		state, ok := x.breakState(l2.i, neck)
		if !ok {
			continue
		}
		c := &pcand{start: l1.i, piv: []pvt{l1, h, l2}, state: state, level: h.p, target: h.p + (h.p - (l1.p+l2.p)/2),
			lines: []PatternLine{{I1: float64(l1.i), P1: h.p, I2: e, P2: h.p}, {I1: float64(l1.i), P1: l1.p, I2: float64(l2.i), P2: l2.p}}}
		var ws float64
		look := priorLook(length)
		a, pc := x.priorFall(l1.i, l1.p, look)
		c.part(x.w("前段下跌", "前段上漲"), ramp(a, 1.5, 5), 1.2,
			fmt.Sprintf("第一個%s前 %d 根%s %.1f%%(%.1f ATR)", x.w("底", "頂"), look, x.w("跌", "漲"), pc, a), &ws)
		c.part(x.w("兩個底等高", "兩個頂等高"), ramp(diff, 1.5, 0.15), 1.3,
			fmt.Sprintf("相差 %.2f%%(%.1f ATR)", math.Abs(pct(l1.p-l2.p, l1.p)), diff), &ws)
		c.part(x.w("中間反彈高度", "中間回檔深度"), ramp(depth, 1, 3.5), 1,
			fmt.Sprintf("頸線離%s %.1f ATR", x.w("底", "頂"), depth), &ws)
		c.part(x.w("兩底間隔", "兩頂間隔"), ramp(float64(gap), 5, 15), 0.6, fmt.Sprintf("相隔 %d 根", gap), &ws)
		prog := 100.0
		if state == "forming" {
			prog = ramp((x.c-l2.p)/(h.p-l2.p), 0, 0.7)
		}
		c.part(x.w("右側回升", "右側回落"), prog, 0.8,
			fmt.Sprintf("已%s到頸線的 %.0f%%", x.w("回升", "回落"), math.Min(100, (x.c-l2.p)/(h.p-l2.p)*100)), &ws)
		best = better(best, c.finish(ws))
	}
	return best
}

// ---- 三重底 ----

func detTripleBottom(x *pctx, piv []pvt) *pcand {
	var best *pcand
	e := float64(x.n - 1)
	for _, j := range []int{len(piv) - 1, len(piv) - 2} {
		if j < 4 {
			continue
		}
		l1, h1, l2, h2, l3 := piv[j-4], piv[j-3], piv[j-2], piv[j-1], piv[j]
		if l1.hi || !h1.hi || l2.hi || !h2.hi || l3.hi {
			continue
		}
		length, g1, g2 := x.n-1-l1.i, l2.i-l1.i, l3.i-l2.i
		if length < ptMinBars || length > ptMaxBars || g1 < 4 || g2 < 4 || !x.recent(l3.i, length) {
			continue
		}
		lo := math.Min(l1.p, math.Min(l2.p, l3.p))
		hiL := math.Max(l1.p, math.Max(l2.p, l3.p))
		spread := (hiL - lo) / x.atr
		neckP := math.Max(h1.p, h2.p)
		depth := (math.Min(h1.p, h2.p) - hiL) / x.atr
		if spread > 2.5 || depth < 0.8 || x.minLow(l3.i+1, x.n) < lo-0.3*x.atr {
			continue
		}
		state, ok := x.breakState(l3.i, func(float64) float64 { return neckP })
		if !ok {
			continue
		}
		avgL := (l1.p + l2.p + l3.p) / 3
		c := &pcand{start: l1.i, piv: []pvt{l1, h1, l2, h2, l3}, state: state, level: neckP, target: neckP + (neckP - avgL),
			lines: []PatternLine{{I1: float64(l1.i), P1: neckP, I2: e, P2: neckP}, {I1: float64(l1.i), P1: avgL, I2: float64(l3.i), P2: avgL}}}
		var ws float64
		look := priorLook(length)
		a, pc := x.priorFall(l1.i, l1.p, look)
		c.part(x.w("前段下跌", "前段上漲"), ramp(a, 1.5, 5), 1.2,
			fmt.Sprintf("第一個%s前 %d 根%s %.1f%%(%.1f ATR)", x.w("底", "頂"), look, x.w("跌", "漲"), pc, a), &ws)
		c.part(x.w("三個底等高", "三個頂等高"), ramp(spread, 2, 0.2), 1.3, fmt.Sprintf("最高最低差 %.1f ATR", spread), &ws)
		nd := math.Abs(h1.p-h2.p) / x.atr
		c.part("頸線持平", ramp(nd, 2, 0.3), 0.8, fmt.Sprintf("兩個%s差 %.1f ATR", x.w("反彈高點", "回檔低點"), nd), &ws)
		c.part(x.w("反彈高度", "回檔深度"), ramp(depth, 1, 3), 0.8, fmt.Sprintf("頸線離%s %.1f ATR", x.w("底", "頂"), depth), &ws)
		even := float64(min(g1, g2)) / float64(max(g1, g2))
		c.part("間隔平均", ramp(even, 0.3, 0.75), 0.6, fmt.Sprintf("相隔 %d 根、%d 根", g1, g2), &ws)
		prog := 100.0
		if state == "forming" {
			prog = ramp((x.c-l3.p)/(neckP-l3.p), 0, 0.7)
		}
		c.part(x.w("右側回升", "右側回落"), prog, 0.8,
			fmt.Sprintf("已%s到頸線的 %.0f%%", x.w("回升", "回落"), math.Min(100, (x.c-l3.p)/(neckP-l3.p)*100)), &ws)
		best = better(best, c.finish(ws))
	}
	return best
}

// ---- 頭肩底 ----

func detHSBottom(x *pctx, piv []pvt) *pcand {
	var best *pcand
	e := float64(x.n - 1)
	for _, j := range []int{len(piv) - 1, len(piv) - 2} {
		if j < 4 {
			continue
		}
		l1, h1, l2, h2, l3 := piv[j-4], piv[j-3], piv[j-2], piv[j-1], piv[j]
		if l1.hi || !h1.hi || l2.hi || !h2.hi || l3.hi {
			continue
		}
		length, g1, g2 := x.n-1-l1.i, l2.i-l1.i, l3.i-l2.i
		if length < ptMinBars || length > ptMaxBars || g1 < 3 || g2 < 3 || !x.recent(l3.i, length) {
			continue
		}
		hd := (math.Min(l1.p, l3.p) - l2.p) / x.atr
		sd := math.Abs(l1.p-l3.p) / x.atr
		if hd < 0.3 || sd > 2.5 || x.minLow(l3.i+1, x.n) < l2.p {
			continue // 頭沒有比兩肩低、兩肩差太多、或之後跌破頭
		}
		neck := fitLine([]pvt{h1, h2})
		state, ok := x.breakState(l3.i, neck.at)
		if !ok {
			continue
		}
		c := &pcand{start: l1.i, piv: []pvt{l1, h1, l2, h2, l3}, state: state, level: neck.at(e),
			target: neck.at(e) + (neck.at(float64(l2.i)) - l2.p), lines: []PatternLine{segLine(neck, float64(l1.i), e)}}
		var ws float64
		look := priorLook(length)
		a, pc := x.priorFall(l1.i, l1.p, look)
		c.part(x.w("前段下跌", "前段上漲"), ramp(a, 1.5, 5), 1.2,
			fmt.Sprintf("左肩前 %d 根%s %.1f%%(%.1f ATR)", look, x.w("跌", "漲"), pc, a), &ws)
		c.part(x.w("頭比兩肩低", "頭比兩肩高"), ramp(hd, 0.3, 1.5), 1.3, fmt.Sprintf("頭比較%s的肩還%s %.1f ATR", x.w("低", "高"), x.w("低", "高"), hd), &ws)
		c.part("兩肩等高", ramp(sd, 1.5, 0.2), 1, fmt.Sprintf("左右肩差 %.1f ATR", sd), &ws)
		tilt := math.Abs(neck.slope*float64(length)) / x.atr
		c.part("頸線平", ramp(tilt, 2.5, 0.3), 0.7, fmt.Sprintf("頸線整段斜了 %.1f ATR", tilt), &ws)
		even := float64(min(g1, g2)) / float64(max(g1, g2))
		c.part("左右對稱", ramp(even, 0.3, 0.75), 0.6, fmt.Sprintf("左肩到頭 %d 根、頭到右肩 %d 根", g1, g2), &ws)
		nk := neck.at(e)
		prog := 100.0
		if state == "forming" {
			prog = ramp((x.c-l3.p)/(nk-l3.p), 0, 0.7)
		}
		c.part(x.w("右側回升", "右側回落"), prog, 0.8,
			fmt.Sprintf("已%s到頸線的 %.0f%%", x.w("回升", "回落"), math.Min(100, (x.c-l3.p)/(nk-l3.p)*100)), &ws)
		best = better(best, c.finish(ws))
	}
	return best
}

// ---- 牛旗 ----

func detFlag(x *pctx, piv []pvt) *pcand {
	var best *pcand
	e := float64(x.n - 1)
	for k := len(piv) - 1; k >= 1; k-- {
		hp, lp := piv[k], piv[k-1]
		if x.n-1-hp.i > 60 {
			break
		}
		if !hp.hi || lp.hi {
			continue
		}
		pole := (hp.p - lp.p) / x.atr
		poleBars, flagBars := hp.i-lp.i, x.n-1-hp.i
		steep := pole / float64(max(1, poleBars)) // 旗桿每根走幾個 ATR(隨機走勢很難又長又陡)
		if pole < 4 || steep < 0.4 || poleBars < 1 || poleBars > 30 || flagBars < 3 || flagBars > max(poleBars*5, 25) {
			continue
		}
		after := piv[k:]
		fh, fl := splitHL(after)
		if len(fl) == 0 {
			continue
		}
		depth := (hp.p - x.minLow(hp.i+1, x.n)) / (hp.p - lp.p)
		if depth <= 0.05 || depth > 0.65 {
			continue
		}
		var up, lo pline
		fitOK := len(fh) >= 2 && len(fl) >= 2
		switch {
		case fitOK:
			up, lo = fitLine(fh), fitLine(fl)
		case len(fh) >= 2:
			up = fitLine(fh)
			lo = pline{float64(fl[0].i), fl[0].p, up.slope}
		case len(fl) >= 2:
			lo = fitLine(fl)
			up = pline{float64(hp.i), hp.p, lo.slope}
		default:
			up, lo = pline{float64(hp.i), hp.p, 0}, pline{float64(fl[0].i), fl[0].p, 0}
		}
		FB := float64(flagBars)
		du, dl := up.slope*FB/x.atr, lo.slope*FB/x.atr
		if du > 1 || x.c < lo.at(e)-x.atr {
			continue // 往上走的不是旗;跌破旗面下緣就不算
		}
		state, ok := x.breakState(after[len(after)-1].i, up.at)
		if !ok {
			continue
		}
		c := &pcand{start: lp.i, piv: append([]pvt{lp}, after...), state: state, level: up.at(e), target: up.at(e) + (hp.p - lp.p),
			lines: []PatternLine{segLine(up, float64(hp.i), e), segLine(lo, float64(hp.i), e),
				{I1: float64(lp.i), P1: lp.p, I2: float64(hp.i), P2: hp.p}}}
		var ws float64
		c.part("旗桿", (ramp(pole, 4, 8)+ramp(steep, 0.4, 1.0))/2, 1.3,
			fmt.Sprintf("%d 根%s %.1f%%(%.1f ATR)", poleBars, x.w("漲", "跌"), math.Abs(pct(hp.p-lp.p, lp.p)), pole), &ws)
		c.part(x.w("回檔深度", "反彈幅度"), ramp(depth, 0.65, 0.3), 1, fmt.Sprintf("%s旗桿的 %.0f%%", x.w("回檔", "反彈"), depth*100), &ws)
		dir := 100.0 // 旗面逆著旗桿慢慢走(或持平)最好;往同方向走、或逆走超過旗桿的 35% 就扣分
		if du > 0 {
			dir = ramp(du, 1, 0)
		} else if -du > 0.35*pole {
			dir = ramp(-du, 0.8*pole, 0.35*pole)
		}
		c.part(x.w("旗面向下或持平", "旗面向上或持平"), dir, 0.8, fmt.Sprintf("旗面上緣走了 %+.1f ATR", x.w2(du)), &ws)
		pd := math.Abs(du - dl)
		c.part("通道平行", ramp(pd, 2, 0.3), 0.8, fmt.Sprintf("上下緣斜率差 %.1f ATR", pd), &ws)
		fit := 60.0
		note := fmt.Sprintf("旗面轉折點較少(%d 高 %d 低)", len(fh), len(fl))
		if fitOK {
			res := (lineResid(up, fh) + lineResid(lo, fl)) / 2 / x.atr
			fit, note = ramp(res, 1.0, 0.15), fmt.Sprintf("轉折點平均離線 %.2f ATR", res)
		}
		c.part("貼合通道", fit, 0.7, note, &ws)
		c.part("時間比例", ramp(FB/float64(poleBars), 8, 3), 0.6, fmt.Sprintf("旗面 %d 根 / 旗桿 %d 根", flagBars, poleBars), &ws)
		best = better(best, c.finish(ws))
	}
	return best
}

// w2:斜率顯示用(看跌是翻轉後算的,換回原本方向)。
func (x *pctx) w2(v float64) float64 {
	if x.bear {
		return -v
	}
	return v
}

// ---- 弧形(圓弧底、杯柄的杯身)----

// quadFit 對 [a, b] 的收盤擬合拋物線 y = A + B·t + C·t²(t = 0~1),回傳係數和 R²。
func quadFit(cs []exchange.Candle, a, b int) (A, B, C, r2 float64) {
	n := b - a + 1
	if n < 5 {
		return
	}
	var s [5]float64 // Σt^0..Σt^4
	var sy [3]float64
	var my float64
	for i := a; i <= b; i++ {
		t := float64(i-a) / float64(n-1)
		y := cs[i].Close
		tp := 1.0
		for k := 0; k < 5; k++ {
			s[k] += tp
			if k < 3 {
				sy[k] += tp * y
			}
			tp *= t
		}
		my += y
	}
	my /= float64(n)
	// 解 3×3 正規方程(克拉瑪)
	m := [3][3]float64{{s[0], s[1], s[2]}, {s[1], s[2], s[3]}, {s[2], s[3], s[4]}}
	det := func(m [3][3]float64) float64 {
		return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) - m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) + m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	}
	d := det(m)
	if d == 0 {
		return
	}
	sol := [3]float64{}
	for k := 0; k < 3; k++ {
		mk := m
		for r := 0; r < 3; r++ {
			mk[r][k] = sy[r]
		}
		sol[k] = det(mk) / d
	}
	A, B, C = sol[0], sol[1], sol[2]
	var ssr, sst float64
	for i := a; i <= b; i++ {
		t := float64(i-a) / float64(n-1)
		y := cs[i].Close
		f := A + B*t + C*t*t
		ssr += (y - f) * (y - f)
		sst += (y - my) * (y - my)
	}
	if sst > 0 {
		r2 = 1 - ssr/sst
	}
	return
}

// arcDev:收盤離拋物線最遠多少(價格)—— W 形、中間有大反彈的會很大,真正的圓弧很小。
func arcDev(cs []exchange.Candle, a, b int, A, B, C float64) float64 {
	m := 0.0
	for i := a; i <= b; i++ {
		t := float64(i-a) / float64(b-a)
		m = math.Max(m, math.Abs(cs[i].Close-(A+B*t+C*t*t)))
	}
	return m
}

// bottomShare:[a, b] 裡收盤落在「最低 25% 深度」以內的比例。U 形約一半,V 形只有約四分之一。
func bottomShare(cs []exchange.Candle, a, b int, top, low float64) float64 {
	lim := low + 0.25*(top-low)
	in := 0
	for i := a; i <= b; i++ {
		if cs[i].Close <= lim {
			in++
		}
	}
	return float64(in) / float64(b-a+1)
}

// arcLines 把擬合出來的拋物線畫成幾段折線。
func arcLines(A, B, C float64, a, b int) []PatternLine {
	var out []PatternLine
	const seg = 12
	at := func(k int) (float64, float64) {
		t := float64(k) / seg
		return float64(a) + t*float64(b-a), A + B*t + C*t*t
	}
	for k := 0; k < seg; k++ {
		i1, p1 := at(k)
		i2, p2 := at(k + 1)
		out = append(out, PatternLine{I1: i1, P1: p1, I2: i2, P2: p2})
	}
	return out
}

func detRound(x *pctx, piv []pvt) *pcand {
	var best *pcand
	e := x.n - 1
	for _, s := range piv {
		length := e - s.i
		if !s.hi || length < 20 || length > ptMaxBars {
			continue
		}
		bi := s.i
		for i := s.i; i <= e; i++ {
			if x.cs[i].Low < x.cs[bi].Low {
				bi = i
			}
		}
		low := x.cs[bi].Low
		depth := (s.p - low) / x.atr
		posFrac := float64(bi-s.i) / float64(length)
		recov := (x.c - low) / (s.p - low)
		if depth < 2 || posFrac < 0.25 || posFrac > 0.85 || recov < 0.3 {
			continue
		}
		A, B, C, r2 := quadFit(x.cs, s.i, e)
		if C <= 0 {
			continue // 不是 U 形
		}
		rim := s.p
		state, ok := x.breakState(bi, func(float64) float64 { return rim })
		if !ok {
			continue
		}
		vtx := -B / (2 * C)
		c := &pcand{start: s.i, piv: []pvt{s, {i: bi, p: low}}, state: state, level: rim, target: rim + (rim - low),
			lines: append(arcLines(A, B, C, s.i, e), PatternLine{I1: float64(s.i), P1: rim, I2: float64(e), P2: rim})}
		var ws float64
		c.part("弧形貼合", ramp(r2, 0.4, 0.88), 1.4, fmt.Sprintf("收盤跟弧線的吻合度 R² %.2f", r2), &ws)
		dev := arcDev(x.cs, s.i, e, A, B, C) / x.atr
		c.part("弧線平滑", ramp(dev, 3, 1.0), 1.2, fmt.Sprintf("收盤離弧線最遠 %.1f ATR", dev), &ws)
		bs := bottomShare(x.cs, s.i, e, s.p, low)
		c.part(x.w("底部圓滑(U 不是 V)", "頂部圓滑(∩ 不是 Λ)"), ramp(bs, 0.2, 0.42), 1, fmt.Sprintf("%.0f%% 的時間在最%s那一段", bs*100, x.w("低", "高")), &ws)
		c.part(x.w("底部深度", "頂部高度"), ramp(depth, 2, 5), 0.8, fmt.Sprintf("離起點 %.1f%%(%.1f ATR)", math.Abs(pct(s.p-low, s.p)), depth), &ws)
		c.part(x.w("底在中間", "頂在中間"), ramp(math.Abs(vtx-0.5), 0.35, 0.1), 0.8,
			fmt.Sprintf("弧線最%s點在整段的 %.0f%% 位置", x.w("低", "高"), vtx*100), &ws)
		prog := 100.0
		if state == "forming" {
			prog = ramp(recov, 0.3, 0.9)
		}
		c.part(x.w("右側回升", "右側回落"), prog, 1, fmt.Sprintf("已%s %.0f%% 的深度", x.w("回升", "回落"), math.Min(100, recov*100)), &ws)
		best = better(best, c.finish(ws))
	}
	return best
}

// ---- 看漲杯柄 ----

func detCup(x *pctx, piv []pvt) *pcand {
	var best *pcand
	e := x.n - 1
	hs, _ := splitHL(piv)
	for ai, r1 := range hs {
		for _, r2 := range hs[ai+1:] {
			cupLen, handle := r2.i-r1.i, e-r2.i
			if cupLen < 15 || cupLen > 140 || handle < 2 || handle > max(5, cupLen/2) {
				continue
			}
			rim := math.Max(r1.p, r2.p)
			rimDiff := math.Abs(r1.p-r2.p) / x.atr
			cupLow := x.minLow(r1.i, r2.i+1)
			depth := (math.Min(r1.p, r2.p) - cupLow) / x.atr
			if rimDiff > 2 || depth < 2 || x.maxHigh(r1.i+1, r2.i) > rim+0.3*x.atr {
				continue // 杯緣差太多、杯太淺、或杯子中間比杯緣還高
			}
			hDepth := (r2.p - x.minLow(r2.i+1, x.n)) / (rim - cupLow)
			if hDepth < 0.05 || hDepth > 0.6 {
				continue // 沒有杯柄回檔,或回檔太深
			}
			A, B, C, r2fit := quadFit(x.cs, r1.i, r2.i)
			if C <= 0 {
				continue
			}
			state, ok := x.breakState(r2.i, func(float64) float64 { return rim })
			if !ok {
				continue
			}
			c := &pcand{start: r1.i, piv: []pvt{r1, r2}, state: state, level: rim, target: rim + (rim - cupLow),
				lines: append(arcLines(A, B, C, r1.i, r2.i), PatternLine{I1: float64(r1.i), P1: rim, I2: float64(e), P2: rim})}
			var ws float64
			look := priorLook(cupLen)
			a, pc := x.priorRise(r1.i, r1.p, look)
			c.part(x.w("前段上漲", "前段下跌"), ramp(a, 1.5, 5), 0.8,
				fmt.Sprintf("左杯緣前 %d 根%s %.1f%%(%.1f ATR)", look, x.w("漲", "跌"), pc, a), &ws)
			c.part("杯身弧形", ramp(r2fit, 0.4, 0.88), 1.3, fmt.Sprintf("杯身收盤跟弧線的吻合度 R² %.2f", r2fit), &ws)
			dev := arcDev(x.cs, r1.i, r2.i, A, B, C) / x.atr
			c.part("杯身平滑", ramp(dev, 3, 1.0), 1, fmt.Sprintf("杯身收盤離弧線最遠 %.1f ATR", dev), &ws)
			bs := bottomShare(x.cs, r1.i, r2.i, math.Min(r1.p, r2.p), cupLow)
			c.part("杯底圓滑", ramp(bs, 0.2, 0.42), 0.8, fmt.Sprintf("%.0f%% 的時間在杯底那一段", bs*100), &ws)
			c.part("兩側杯緣等高", ramp(rimDiff, 2, 0.2), 1, fmt.Sprintf("左右杯緣差 %.1f ATR", rimDiff), &ws)
			c.part("杯深", ramp(depth, 2, 5), 0.7, fmt.Sprintf("杯深 %.1f%%(%.1f ATR)", math.Abs(pct(rim-cupLow, rim)), depth), &ws)
			hScore := 100.0
			if hDepth > 0.35 {
				hScore = ramp(hDepth, 0.6, 0.35)
			}
			c.part(x.w("杯柄回檔", "杯柄反彈"), hScore, 1, fmt.Sprintf("%s杯深的 %.0f%%", x.w("回檔", "反彈"), hDepth*100), &ws)
			c.part("杯柄長度", ramp(float64(handle)/float64(cupLen), 0.6, 0.25), 0.6, fmt.Sprintf("杯柄 %d 根 / 杯身 %d 根", handle, cupLen), &ws)
			best = better(best, c.finish(ws))
		}
	}
	return best
}
