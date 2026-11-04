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
