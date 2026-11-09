package cache

import (
	"fmt"
	"math"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// flatBars 造 n 根 1h K(10 附近、全距 0.2),第一根從 start 開始。
func flatBars(start int64, n int) []exchange.Candle {
	cs := make([]exchange.Candle, n)
	for i := range cs {
		cs[i] = exchange.Candle{Ts: start + int64(i)*dcBarMs, Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 100}
	}
	return cs
}

func TestDonchianSignal(t *testing.T) {
	cs := flatBars(0, 180)
	sig := &cs[len(cs)-1]
	sig.High, sig.Close = 11, 11 // 收盤 > 前 168 根最高 10.1
	forming := &exchange.Candle{Ts: sig.Ts + dcBarMs, Open: 11.2}
	dir, entry, sl, ok := dcSignal(cs, forming)
	atr := (23*0.2 + 1.1) / 24 // 訊號那根 TR = 高 11 − 低 9.9
	if !ok || dir != "long" || entry != 11.2 || math.Abs(sl-(11.2-3*atr)) > 0.01 {
		t.Fatalf("long breakout: %v %v %v %v (atr %.4f)", dir, entry, sl, ok, atr)
	}
	sig.High, sig.Close = 10.1, 10.1 // 只碰到、沒有收在上面 → 不算
	if _, _, _, ok := dcSignal(cs, forming); ok {
		t.Fatal("close must be strictly above the 168-bar high")
	}
	sig.High, sig.Low, sig.Close = 10, 9, 9.5 // 收盤 < 前 168 根最低 9.9 → 做空
	dir, _, sl, ok = dcSignal(cs, forming)
	if !ok || dir != "short" || sl <= 11.2 {
		t.Fatalf("short breakout: %v %v %v", dir, sl, ok)
	}
	// 盤中突破但收回 → 不算(只看收盤)
	sig.High, sig.Low, sig.Close = 12, 9.95, 10
	if _, _, _, ok := dcSignal(cs, forming); ok {
		t.Fatal("intrabar spike that closes back inside is not a signal")
	}
	if _, _, _, ok := dcSignal(cs[:168], forming); ok {
		t.Fatal("needs 168 prior bars")
	}
}

func TestDonchianExitScan(t *testing.T) {
	cs := flatBars(0, 40)
	tr := &PaperTrade{Dir: "long", Entry: 10, SL: 9, Status: "open", OpenTime: time.UnixMilli(cs[20].Ts)}
	if _, _, _, upto, hit := dcExitScan(tr, cs, nil); hit || upto != 40 {
		t.Fatal("flat market: no exit")
	}
	// 通道出場:第 30 根收盤 < 前 12 根最低 9.9 → 第 31 根開盤平倉
	cs[30].Low, cs[30].Close = 9.5, 9.8
	cs[31].Open = 9.7
	px, outcome, at, upto, hit := dcExitScan(tr, cs, nil)
	if !hit || outcome != "channel" || px != 9.7 || at.UnixMilli() != cs[31].Ts || upto != 31 {
		t.Fatalf("channel exit: %v %v %v %v", px, outcome, at, hit)
	}
	// 同一根先碰停損 → 停損優先(盤中觸價)
	cs[30].Low = 8.9
	px, outcome, _, _, hit = dcExitScan(tr, cs, nil)
	if !hit || outcome != "sl" || px != 9 {
		t.Fatalf("stop first: %v %v", px, outcome)
	}
	// 跳空越過停損 → 開盤價成交
	cs[30].Open = 8.5
	if px, _, _, _, _ := dcExitScan(tr, cs, nil); px != 8.5 {
		t.Fatalf("gap through stop fills at open, got %v", px)
	}
	// 最新一根才觸發通道出場 → 用還在走那根的開盤
	cs2 := flatBars(0, 40)
	cs2[39].Low, cs2[39].Close = 9.5, 9.8
	px, outcome, _, _, hit = dcExitScan(tr, cs2, &exchange.Candle{Ts: cs2[39].Ts + dcBarMs, Open: 9.75})
	if !hit || outcome != "channel" || px != 9.75 {
		t.Fatalf("exit at forming open: %v %v", px, outcome)
	}
	// 空單:收盤 > 前 12 根最高 → 出場
	sh := &PaperTrade{Dir: "short", Entry: 10, SL: 11, Status: "open", OpenTime: time.UnixMilli(cs2[20].Ts)}
	cs3 := flatBars(0, 40)
	cs3[35].High, cs3[35].Close = 10.4, 10.3
	if _, outcome, _, _, hit := dcExitScan(sh, cs3, nil); !hit || outcome != "channel" {
		t.Fatal("short channel exit")
	}
}

// dcStore 把 coin 的 1h K(結尾是「這個小時還在走的那根」)放進快取。
func dcStore(t *testing.T, coin string, mod func(cs []exchange.Candle)) (*Store, []exchange.Candle) {
	t.Helper()
	s := &Store{candleCache: newTTLCache(time.Hour), coins: []string{coin},
		symTypes: map[string]string{}, symTime: time.Now()}
	cur := time.Now().UnixMilli() / dcBarMs * dcBarMs
	cs := flatBars(cur-int64(dcKlines-1)*dcBarMs, dcKlines)
	mod(cs)
	s.candleCache.get(fmt.Sprintf("%s|1h|%d", coin, dcKlines), func() (any, error) { return cs, nil })
	s.donchianBook = s.newDonchianBook()
	s.dcEng = &hourEngine{}
	return s, cs
}

func TestDonchianTick(t *testing.T) {
	s, cs := dcStore(t, "AAA", func(cs []exchange.Candle) {
		sig := &cs[len(cs)-2]
		sig.High, sig.Close = 11, 11
		cs[len(cs)-1].Open = 11.1
	})
	b := s.donchianBook
	s.DonchianTick() // 開機第一輪:只管持倉、不進場
	if len(b.trades) != 0 {
		t.Fatal("boot pass must not open")
	}
	s.dcEng.bucket = 0 // 模擬下一個小時
	s.DonchianTick()
	if len(b.trades) != 1 {
		t.Fatalf("should open on breakout, got %d", len(b.trades))
	}
	tr := b.trades[0]
	if tr.Dir != "long" || tr.Entry != 11.1 || tr.SL >= 11.1 || tr.OpenTime.UnixMilli() != cs[len(cs)-1].Ts {
		t.Fatalf("bad trade: %+v", tr)
	}
	if want := roundPx((23*0.2 + 1.1) / 24); tr.ATR != want {
		t.Fatalf("entry ATR = %v, want %v", tr.ATR, want)
	}
	tr.ATR = 0
	s.dcEng.bucket = 0
	s.DonchianTick() // 持倉中:每小時更新目前的 ATR
	if tr.ATR == 0 {
		t.Fatal("open position should get the current ATR each hour")
	}
	s.dcEng.bucket = 0
	s.DonchianTick() // 同一根已持有 → 不加碼
	if len(b.trades) != 1 {
		t.Fatal("no pyramiding")
	}
	// 停用:不進新單,但持倉照樣管
	tr.Status = "closed"
	s.stratOff = map[string]bool{"donchian": true}
	s.dcEng.bucket = 0
	s.DonchianTick()
	if len(b.trades) != 1 {
		t.Fatal("disabled: no new entries")
	}
}

func TestDonchianReverse(t *testing.T) {
	// 空單持倉中,最新一根收盤同時突破 12 根出場通道與 168 根進場通道 → 平空、同一根反手做多
	s, cs := dcStore(t, "AAA", func(cs []exchange.Candle) {
		sig := &cs[len(cs)-2]
		sig.High, sig.Close = 11, 11
		cs[len(cs)-1].Open = 11.1
	})
	b := s.donchianBook
	b.trades = []*PaperTrade{{ID: "x", Coin: "AAA", Dir: "short", Entry: 10, SL: 12, Status: "open",
		OpenTime: time.UnixMilli(cs[len(cs)-30].Ts)}}
	s.dcEng.seeded = true
	s.DonchianTick()
	if len(b.trades) != 2 {
		t.Fatalf("should close short and open long, got %d trades", len(b.trades))
	}
	var short, long *PaperTrade
	for _, tr := range b.trades {
		if tr.Dir == "short" {
			short = tr
		} else {
			long = tr
		}
	}
	if short.Status != "closed" || short.Outcome != "channel" || short.Cur != 11.1 || long == nil || long.Status != "open" {
		t.Fatalf("reverse: short %+v long %+v", short, long)
	}
}

func TestDonchianMarkStop(t *testing.T) {
	s, _ := dcStore(t, "AAA", func([]exchange.Candle) {})
	b := s.donchianBook
	b.trades = []*PaperTrade{{ID: "x", Coin: "AAA", Dir: "long", Entry: 10, SL: 9, Status: "open", OpenTime: time.Now()}}
	s.dcMark(map[string]float64{"AAA": 9.4}, time.Now())
	if tr := b.trades[0]; tr.Status != "open" || tr.Cur != 9.4 {
		t.Fatalf("above stop: stays open with live price: %+v", tr)
	}
	s.dcMark(map[string]float64{"AAA": 8.9}, time.Now())
	if tr := b.trades[0]; tr.Status != "closed" || tr.Outcome != "sl" || tr.Cur != 9 {
		t.Fatalf("live stop should fill at the stop price: %+v", tr)
	}
}

func TestDonchianExcursion(t *testing.T) {
	cs := flatBars(0, 10)
	tr := &PaperTrade{Dir: "long", Entry: 10, SL: 9, OpenTime: time.UnixMilli(cs[3].Ts)}
	cs[2].High, cs[2].Low = 20, 1 // 進場前:不算
	cs[3].Low = 9.5               // 進場那根(在開盤進場):算 → −5%
	cs[6].High = 11.2             // 高點在第 7 根 → +12%
	cs[8].High = 11.2             // 一樣高:時間不改成較晚那根
	dcExcursion(tr, cs)
	if tr.MaxGain != 12 || tr.PeakMs != cs[6].Ts || tr.MaxDD != -5 {
		t.Fatalf("long excursion: gain %v at %v, dd %v", tr.MaxGain, tr.PeakMs, tr.MaxDD)
	}
	sh := &PaperTrade{Dir: "short", Entry: 10, SL: 11, OpenTime: time.UnixMilli(cs[3].Ts)}
	cs2 := flatBars(0, 10)
	cs2[5].Low, cs2[7].High = 9, 10.4 // 空單:低點有利 +10%、高點不利 −4%
	dcExcursion(sh, cs2)
	if sh.MaxGain != 10 || sh.PeakMs != cs2[5].Ts || sh.MaxDD != -4 {
		t.Fatalf("short excursion: gain %v at %v, dd %v", sh.MaxGain, sh.PeakMs, sh.MaxDD)
	}
}

func TestDonchianChanLevel(t *testing.T) {
	cs := flatBars(0, 30)
	cs[20].Low, cs[25].High = 9.2, 10.9
	cs[10].Low = 5 // 超過 12 根前:不算
	if lv := dcChanLevel(cs, "long"); lv != 9.2 {
		t.Fatalf("long channel exit = %v, want 9.2", lv)
	}
	if lv := dcChanLevel(cs, "short"); lv != 10.9 {
		t.Fatalf("short channel exit = %v, want 10.9", lv)
	}
	// 跟出場規則一致:下一根收盤 < 這個價 → dcExitScan 判通道出場
	tr := &PaperTrade{Dir: "long", Entry: 10, SL: 8, Status: "open", OpenTime: time.UnixMilli(cs[15].Ts)}
	next := append(append([]exchange.Candle{}, cs...), exchange.Candle{Ts: cs[29].Ts + dcBarMs, Open: 9.5, High: 9.5, Low: 9.1, Close: 9.15})
	if _, outcome, _, _, hit := dcExitScan(tr, next, nil); !hit || outcome != "channel" {
		t.Fatal("closing below the shown channel level must trigger the channel exit")
	}
}

func TestDonchianTickRecordsExcursion(t *testing.T) {
	s, cs := dcStore(t, "AAA", func(cs []exchange.Candle) {
		n := len(cs)
		cs[n-5].Low = 9.6   // 進場後跌到 −4%
		cs[n-3].High = 10.8 // 進場後最高 +8%
	})
	b := s.donchianBook
	tr := &PaperTrade{ID: "x", Coin: "AAA", Dir: "long", Entry: 10, SL: 9, Status: "open", OpenTime: time.UnixMilli(cs[len(cs)-8].Ts)}
	b.trades = []*PaperTrade{tr}
	s.dcEng.seeded = true
	s.DonchianTick()
	if tr.Status != "open" || tr.MaxDD != -4 || tr.MaxGain != 8 || tr.PeakMs != cs[len(cs)-3].Ts || tr.ChanExit != 9.6 || tr.ATR == 0 {
		t.Fatalf("tick should record excursion / channel exit: %+v", tr)
	}
	// 盤中即時價創新高 → 時間記現在;跌更深 → 回撤更新
	now := time.Now()
	s.dcMark(map[string]float64{"AAA": 11}, now)
	if tr.MaxGain != 10 || tr.PeakMs != now.UnixMilli() {
		t.Fatalf("live new high: %+v", tr)
	}
	s.dcMark(map[string]float64{"AAA": 9.5}, now)
	if tr.MaxDD != -5 || tr.Status != "open" {
		t.Fatalf("live drawdown: %+v", tr)
	}
}

// trendBars:前 20 根平盤在 10,第 20~30 根收盤每根 +0.3 漲到 13,之後平盤在 13(K 線全距 ±0.1、開盤 = 前一根收盤)。
func trendBars(n int) []exchange.Candle {
	cs := make([]exchange.Candle, n)
	px := 10.0
	for i := range cs {
		o := px
		if i >= 20 && i <= 30 {
			px = 10 + float64(i-20)*0.3
		}
		cs[i] = exchange.Candle{Ts: int64(i) * dcBarMs, Open: o, High: math.Max(o, px) + 0.1, Low: math.Min(o, px) - 0.1, Close: px, Volume: 100}
	}
	return cs
}

func TestDonchianChandelier(t *testing.T) {
	tr := &PaperTrade{Dir: "long", Entry: 10, SL: 8, Status: "open", OpenTime: time.UnixMilli(20 * dcBarMs)}
	cs := trendBars(40)
	if _, _, _, _, hit := dcExitScanV(tr, cs, nil, true); hit {
		t.Fatal("holding near the high: no chandelier exit")
	}
	lv := dcChandLevel(tr, cs)
	if want := roundPx(13 - dcATRK*dcATR(cs)); lv != want || lv <= 10 {
		t.Fatalf("chandelier level %v, want %v (above entry = locked profit)", lv, want)
	}
	// 第 36 根收盤跌到最高收盤 − 3 ATR 之下(但還在 12 根通道內)→ 第 37 根開盤出場,記 chandelier
	cs[36].Close, cs[36].Low = 11.5, 11.4
	cs[37].Open = 11.45
	px, outcome, at, upto, hit := dcExitScanV(tr, cs, nil, true)
	if !hit || outcome != "chandelier" || px != 11.45 || at.UnixMilli() != cs[37].Ts || upto != 37 {
		t.Fatalf("chandelier exit: %v %v %v %v %v", px, outcome, at, upto, hit)
	}
	if lo := dcChanLevel(cs[:36], "long"); cs[36].Close < lo {
		t.Fatalf("test setup: should still be inside the 12-bar channel (%v vs %v)", cs[36].Close, lo)
	}
	// v1(沒有吊燈停利)同一段走勢不出場
	if _, _, _, _, hit := dcExitScanV(tr, cs, nil, false); hit {
		t.Fatal("v1 has no chandelier exit")
	}
	// 只看收盤:盤中低點跌破、收盤收回 → 不出場
	cs2 := trendBars(40)
	cs2[36].Low = 11
	if _, _, _, _, hit := dcExitScanV(tr, cs2, nil, true); hit {
		t.Fatal("intrabar wick below the line must not trigger (close-based)")
	}
	// 同一根也跌破 12 根通道 → 記 channel
	cs3 := trendBars(40)
	cs3[34].Close, cs3[34].Low = 9.5, 9.4
	if _, outcome, _, _, hit := dcExitScanV(tr, cs3, nil, true); !hit || outcome != "channel" {
		t.Fatalf("both on the same bar → channel, got %v", outcome)
	}
	// 初始停損照樣優先(盤中觸價)
	cs4 := trendBars(40)
	cs4[25].Low = 7.9
	if _, outcome, _, _, hit := dcExitScanV(tr, cs4, nil, true); !hit || outcome != "sl" {
		t.Fatalf("initial stop still first, got %v", outcome)
	}
}

func TestDonchianChandelierShort(t *testing.T) {
	// 鏡像:價格上下翻轉(20 − 價)
	cs := trendBars(40)
	for i := range cs {
		c := &cs[i]
		c.Open, c.Close, c.High, c.Low = 20-c.Open, 20-c.Close, 20-c.Low, 20-c.High
	}
	tr := &PaperTrade{Dir: "short", Entry: 10, SL: 12, Status: "open", OpenTime: time.UnixMilli(20 * dcBarMs)}
	if lv := dcChandLevel(tr, cs); lv != roundPx(7+dcATRK*dcATR(cs)) {
		t.Fatalf("short level = %v", lv)
	}
	cs[36].Close, cs[36].High = 8.5, 8.6
	if _, outcome, _, _, hit := dcExitScanV(tr, cs, nil, true); !hit || outcome != "chandelier" {
		t.Fatalf("short chandelier exit, got %v", outcome)
	}
}

func TestDonchianV2Tick(t *testing.T) {
	s, cs := dcStore(t, "AAA", func(cs []exchange.Candle) {
		sig := &cs[len(cs)-2]
		sig.High, sig.Close = 11, 11
		cs[len(cs)-1].Open = 11.1
	})
	s.donchianV2Book = s.newDonchianV2Book()
	s.dcEng2 = &hourEngine{seeded: true}
	s.dcEng.seeded = true
	s.DonchianTick()
	v1, v2 := s.donchianBook.trades, s.donchianV2Book.trades
	if len(v1) != 1 || len(v2) != 1 {
		t.Fatalf("both books enter on the same breakout: v1 %d v2 %d", len(v1), len(v2))
	}
	if want := fmt.Sprintf("donchianv2|AAA|%d", cs[len(cs)-1].Ts); v2[0].ID != want || v2[0].Entry != 11.1 || v2[0].ChanExit == 0 {
		t.Fatalf("v2 trade: %+v", v2[0])
	}
	if v1[0].ChandExit != 0 {
		t.Fatal("v1 has no chandelier level")
	}
	// 持倉中:吊燈停利價 = 進場後最高收盤 − 3 × 最新 ATR
	tr := v2[0]
	tr.OpenTime, tr.SL = time.UnixMilli(cs[len(cs)-3].Ts), 5 // 往前挪:進場後收盤 10 → 11
	s.dcEng2.bucket = 0
	s.DonchianTick()
	if tr.Status != "open" || tr.ChandExit != roundPx(11-dcATRK*dcATR(cs[:len(cs)-1])) {
		t.Fatalf("v2 chandelier level: %+v", tr)
	}
}

// v3Series:共 total 根(最後一根 = 還在走的那根),前面振幅 ±wide、最後 narrowN 根(含)振幅 ±0.5,
// 最新一根已收的 K 收盤 brk(突破)。Ts 從 start 開始。
func v3Series(start int64, total, narrowN int, wide, brk float64) []exchange.Candle {
	cs := make([]exchange.Candle, total)
	for i := range cs {
		a := wide
		if i >= total-1-narrowN {
			a = 0.5
		}
		cs[i] = exchange.Candle{Ts: start + int64(i)*dcBarMs, Open: 100, High: 100 + a, Low: 100 - a, Close: 100, Volume: 100}
	}
	sig := &cs[total-2]
	sig.Close, sig.High = brk, brk+0.1
	cs[total-1].Open = brk
	return cs
}

func TestDonchianV3Width(t *testing.T) {
	cs := v3Series(0, 2400, 400, 5, 102)
	closed := cs[:len(cs)-1]
	w, med, ok := dcV3Width(closed)
	if !ok || math.Abs(w-0.01) > 1e-9 || math.Abs(med-0.10) > 1e-9 {
		t.Fatalf("narrow channel before breakout: w=%v med=%v ok=%v", w, med, ok)
	}
	if dir, entry, sl, ok := dcSignalV3(closed, &cs[len(cs)-1]); !ok || dir != "long" || entry != 102 || sl >= entry {
		t.Fatalf("narrow + breakout → long: %v %v %v %v", dir, entry, sl, ok)
	}
	// 通道沒有比平常窄(全部 ±5)→ 突破也不進
	cs2 := v3Series(0, 2400, 0, 5, 106)
	if _, _, _, ok := dcSignalV3(cs2[:len(cs2)-1], &cs2[len(cs2)-1]); ok {
		t.Fatal("channel not narrower than the 2000-bar median → no entry")
	}
	if _, _, _, ok := dcSignal(cs2[:len(cs2)-1], &cs2[len(cs2)-1]); !ok {
		t.Fatal("test setup: it is a 168-bar breakout")
	}
	// 歷史不夠 2000 根寬度 → 不進
	if _, _, ok := dcV3Width(closed[len(closed)-2000:]); ok {
		t.Fatal("needs 336 + 2000 bars")
	}
	// 突破的是 336 根:只破 168 根、沒破 336 根 → 不進
	cs3 := v3Series(0, 2400, 400, 5, 102)
	cs3[len(cs3)-300].High = 103
	if _, _, _, ok := dcSignalV3(cs3[:len(cs3)-1], &cs3[len(cs3)-1]); ok {
		t.Fatal("must break the 336-bar high")
	}
}

func TestDonchianV3StopCooldown(t *testing.T) {
	at := int64(1000) * dcBarMs
	mk := func(dir, outcome string, hoursAgo int64) *PaperTrade {
		return &PaperTrade{Coin: "AAA", Dir: dir, Status: "closed", Outcome: outcome, CloseTime: tptr(time.UnixMilli(at - hoursAgo*dcBarMs))}
	}
	cases := []struct {
		name string
		tr   *PaperTrade
		want bool
	}{
		{"同方向 10h 前停損", mk("long", "sl", 10), true},
		{"同方向 47h 前停損", mk("long", "sl", 47), true},
		{"同方向 49h 前停損", mk("long", "sl", 49), false},
		{"反方向停損", mk("short", "sl", 10), false},
		{"吊燈停利出場不算", mk("long", "chandelier", 10), false},
		{"通道出場不算", mk("long", "channel", 10), false},
		{"沒有平倉時間", &PaperTrade{Coin: "AAA", Dir: "long", Status: "closed", Outcome: "sl"}, false},
	}
	for _, c := range cases {
		if got := dcStoppedRecently([]*PaperTrade{c.tr}, "AAA", "long", at); got != c.want {
			t.Fatalf("%s: got %v", c.name, got)
		}
	}
}

func TestDonchianV3Exit(t *testing.T) {
	// 跌到 3 ATR 以下、但還在 4 ATR 以內 → v2 會出、v3 不出
	tr := &PaperTrade{Dir: "long", Entry: 10, SL: 8, Status: "open", OpenTime: time.UnixMilli(20 * dcBarMs)}
	cs := trendBars(40)
	cs[36].Close, cs[36].Low = 11.7, 11.6
	atr := dcATR(cs[:37])
	if !(11.7 < 13-3*atr && 11.7 > 13-4*atr) {
		t.Fatalf("test setup: atr %v", atr)
	}
	if _, outcome, _, _, hit := dcExitScanP(tr, cs, nil, dcExitN, dcATRK); !hit || outcome != "chandelier" {
		t.Fatal("3×ATR should exit")
	}
	if _, _, _, _, hit := dcExitScanP(tr, cs, nil, dcV3ExitN, dcV3ChandK); hit {
		t.Fatal("4×ATR should hold")
	}
	// 24 根通道:跌破 12 根低點但沒破 24 根低點 → v3 不出
	cs2 := trendBars(60)
	cs2[50].Close, cs2[50].Low = 12.9, 12.85
	for i := 40; i < 50; i++ {
		cs2[i].Low = 12.95
	}
	if lv12, lv24 := dcChanLevelN(cs2[:50], "long", 12), dcChanLevelN(cs2[:50], "long", 24); lv24 >= lv12 {
		t.Fatalf("24-bar channel is wider: %v vs %v", lv24, lv12)
	}
}

func TestDonchianV3Tick(t *testing.T) {
	cur := time.Now().UnixMilli() / dcBarMs * dcBarMs
	all := v3Series(cur-2399*dcBarMs, 2400, 400, 5, 102)
	s, _ := dcStore(t, "AAA", func(cs []exchange.Candle) { copy(cs, all[2400-dcKlines:]) })
	s.donchianV3Book = s.newDonchianV3Book()
	s.dcEng3 = &hourEngine{seeded: true}
	s.dcEng.seeded = true
	s.dcV3Hist = map[string][]exchange.Candle{"AAA": append([]exchange.Candle(nil), all[:2300]...)}
	b := s.donchianV3Book
	// 同方向 10 小時前剛被停損 → 不進
	b.trades = []*PaperTrade{{ID: "old", Coin: "AAA", Dir: "long", Status: "closed", Outcome: "sl", CloseTime: tptr(time.UnixMilli(cur - 10*dcBarMs))}}
	s.DonchianTick()
	if len(b.trades) != 1 {
		t.Fatalf("48h stop cooldown should block, got %d trades", len(b.trades))
	}
	if h := s.dcV3Hist["AAA"]; len(h) != 2399 || h[len(h)-1].Ts != cur-dcBarMs {
		t.Fatalf("history should be stitched up to the last closed bar: %d", len(h))
	}
	b.trades = nil
	s.dcEng3.bucket = 0
	s.DonchianTick()
	if len(b.trades) != 1 {
		t.Fatalf("v3 should enter, got %d", len(b.trades))
	}
	tr := b.trades[0]
	if tr.ID != fmt.Sprintf("donchianv3|AAA|%d", cur) || tr.Dir != "long" || tr.Entry != 102 || tr.ChanExit != dcChanLevelN(all[:2399], "long", 24) {
		t.Fatalf("v3 trade: %+v", tr)
	}
	// 歷史接不上又抓不到(沒有交易所)、也沒持倉 → 這一輪跳過
	s.dcV3Hist = map[string][]exchange.Candle{}
	b.trades = nil
	s.dcEng3.bucket = 0
	s.DonchianTick()
	if len(b.trades) != 0 || !s.dcEng3.pend["AAA"] {
		t.Fatal("no history → retry next minute, no entry")
	}
}

func tptr(t time.Time) *time.Time { return &t }
