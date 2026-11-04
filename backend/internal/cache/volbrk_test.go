package cache

import (
	"fmt"
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// vbDaily 造 n 根日 K(收盤 100),最後一根 = day 那天(開盤 100)。
func vbDaily(day int64, n int) []exchange.Candle {
	cs := make([]exchange.Candle, n)
	for i := range cs {
		cs[i] = exchange.Candle{Ts: day - int64(n-1-i)*vbDayMs, Open: 100, High: 102, Low: 98, Close: 100}
	}
	return cs
}

func TestVolBrkPlan(t *testing.T) {
	day := int64(100) * vbDayMs
	d := vbDaily(day, 25)
	d[23] = exchange.Candle{Ts: d[23].Ts, Open: 100, High: 110, Low: 100, Close: 105} // 前一日:振幅 10、收在均線上
	d[24].Open = 106
	p, trend, ready := vbPlanOf(d, day)
	if !ready || !trend || p.Upper != 111 || p.Lower != 101 {
		t.Fatalf("plan: %+v trend=%v ready=%v", p, trend, ready)
	}
	d[23].Close = 99 // 收在 20 日均線(約 99.95)之下 → 今天不做多
	if _, trend, _ := vbPlanOf(d, day); trend {
		t.Fatal("below MA20 → no long today")
	}
	if _, _, ready := vbPlanOf(d, day+vbDayMs); ready {
		t.Fatal("today's daily bar missing → not ready")
	}
	if _, _, ready := vbPlanOf(d[5:], day); ready {
		t.Fatal("needs 20 prior days")
	}
}

func TestVolBrkReplay(t *testing.T) {
	p := vbPlan{Day: 0, Upper: 110, Lower: 90}
	bar := func(i int, o, h, l float64) exchange.Candle {
		return exchange.Candle{Ts: int64(i) * dcBarMs, Open: o, High: h, Low: l, Close: o}
	}
	cases := []struct {
		name  string
		hours []exchange.Candle
		state int
		px    float64
	}{
		{"還在等", []exchange.Candle{bar(0, 100, 105, 95), bar(1, 100, 109, 91)}, vbWait, 0},
		{"碰上緣", []exchange.Candle{bar(0, 100, 105, 95), bar(1, 105, 112, 104)}, vbFilled, 110},
		{"跳空在上緣之上 → 開盤價", []exchange.Candle{bar(0, 100, 105, 95), bar(1, 113, 115, 112)}, vbFilled, 113},
		{"同一根碰兩條線", []exchange.Candle{bar(0, 100, 111, 89)}, vbDead, 0},
		{"先碰下緣", []exchange.Candle{bar(0, 100, 105, 89), bar(1, 100, 120, 99)}, vbDead, 0},
		{"隔天的 K 不算", []exchange.Candle{bar(24, 100, 120, 99)}, vbWait, 0},
	}
	for _, c := range cases {
		st, px, _ := vbReplay(c.hours, p)
		if st != c.state || px != c.px {
			t.Fatalf("%s: state=%d px=%v", c.name, st, px)
		}
	}
}

// vbStore:coin 今天的 1h K(結尾是這個小時還在走的那根)+ 日 K 放進快取。
func vbStore(t *testing.T, coin string, mod func(hours, daily []exchange.Candle)) (*Store, int64) {
	t.Helper()
	s := &Store{candleCache: newTTLCache(time.Hour), coins: []string{coin},
		symTypes: map[string]string{}, symTime: time.Now()}
	now := time.Now().UnixMilli()
	day := now / vbDayMs * vbDayMs
	cur := now / dcBarMs * dcBarMs
	hours := flatBars(cur-int64(dcKlines-1)*dcBarMs, dcKlines)
	for i := range hours {
		hours[i].Open, hours[i].High, hours[i].Low, hours[i].Close = 100, 100.5, 99.5, 100
	}
	daily := vbDaily(day, vbDailyN)
	daily[vbDailyN-2] = exchange.Candle{Ts: day - vbDayMs, Open: 100, High: 104, Low: 100, Close: 103} // 振幅 4 → 上緣 102、下緣 98
	mod(hours, daily)
	s.candleCache.get(fmt.Sprintf("%s|1h|%d", coin, dcKlines), func() (any, error) { return hours, nil })
	s.candleCache.get(fmt.Sprintf("%s|1d|%d", coin, vbDailyN), func() (any, error) { return daily, nil })
	s.volBrkBook = s.newVolBrkBook()
	s.vbEng = &hourEngine{seeded: true}
	return s, day
}

func TestVolBrkTickPendingThenFill(t *testing.T) {
	s, day := vbStore(t, "AAA", func(_, _ []exchange.Candle) {})
	b := s.volBrkBook
	s.VolBrkTick()
	if len(b.trades) != 1 || b.trades[0].Status != "pending" || b.trades[0].Entry != 102 {
		t.Fatalf("no touch yet → one pending plan at the upper line: %+v", b.trades)
	}
	if time.Now().UnixMilli()-day < dcBarMs {
		t.Skip("UTC 第一個小時內今天還沒有已收的 1h K,觸價那段測不到")
	}
	// 今天最新一根已收的 1h 碰到上緣 → 成交、待觸發的計畫拿掉
	s2, _ := vbStore(t, "AAA", func(hours, _ []exchange.Candle) { hours[len(hours)-2].High = 102.5 })
	s2.VolBrkTick()
	tr := s2.volBrkBook.trades
	if len(tr) != 1 || tr[0].Status != "open" || tr[0].Entry != 102 || tr[0].ID != fmt.Sprintf("volbrk|AAA|%d", day) {
		t.Fatalf("fill at the upper line: %+v", tr)
	}
	s2.vbEng.bucket = 0
	s2.VolBrkTick() // 每天最多一筆
	if len(s2.volBrkBook.trades) != 1 {
		t.Fatal("one trade per day")
	}
}

func TestVolBrkTickTrendOffAndEOD(t *testing.T) {
	s, day := vbStore(t, "AAA", func(_, daily []exchange.Candle) { daily[vbDailyN-2].Close = 99 })
	b := s.volBrkBook
	// 昨天的持倉 → 換日用昨天的收盤平倉
	b.trades = []*PaperTrade{{ID: "y", Coin: "AAA", Dir: "long", Entry: 100, Status: "open", OpenTime: time.UnixMilli(day - 5*dcBarMs)}}
	s.VolBrkTick()
	if len(b.trades) != 1 {
		t.Fatalf("trend off: no plan for today, got %d", len(b.trades))
	}
	if tr := b.trades[0]; tr.Status != "closed" || tr.Outcome != "eod" || tr.Cur != 99 || tr.CloseTime.UnixMilli() != day {
		t.Fatalf("yesterday's trade should close at yesterday's close: %+v", tr)
	}
}
