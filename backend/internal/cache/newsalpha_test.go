package cache

import (
	"strings"
	"testing"
	"time"

	"datahunter/internal/exchange"
	"datahunter/internal/newsfeed"
)

func TestNAClassify(t *testing.T) {
	cases := []struct {
		src, title, preset string
		value              float64
		cat, dir           string
		str                int
		trad               bool
	}{
		{"binance", "Binance Will List Hyperliquid (HYPE) with Seed Tag Applied", "", 0, "listing", "long", 2, true},
		{"binance", "Binance Futures Will Launch USDⓈ-Margined ZORAUSDT Perpetual Contract", "", 0, "listing", "long", 2, true},
		{"binance", "Binance Launches Halloween Trading Campaign", "", 0, "other", "", 0, false},
		{"binance", "Binance Will Delist ABC, DEF on 2026-11-01", "", 0, "delist", "short", 3, true},
		{"binance", "Notice of Removal of Spot Trading Pairs - 2026-11-01", "", 0, "other", "", 0, false},
		{"binance", "Binance Will Extend the Monitoring Tag to Include XYZ", "", 0, "warning", "short", 2, true},
		{"binance", "Binance Will Remove the Monitoring Tag for XYZ", "", 0, "warning_lift", "long", 1, false},
		{"upbit", "[거래] 플로키(FLOKI) KRW 마켓 디지털 자산 추가", "", 0, "listing_kr", "long", 3, true},
		{"upbit", "[거래] 플로키(FLOKI) BTC, USDT 마켓 디지털 자산 추가", "", 0, "listing_kr", "long", 2, true},
		{"upbit", "[거래지원종료] 에이비씨(ABC) 거래지원 종료 안내", "", 0, "delist", "short", 3, true},
		{"bithumb", "[투자유의] 에이비씨(ABC) 투자유의 종목 지정", "", 0, "warning", "short", 2, true},
		{"upbit", "[유의 종목 지정 해제] 에이비씨(ABC)", "", 0, "warning_lift", "long", 1, false},
		{"tg:BWEnews", "币安将上线 XYZ (XYZ)", "", 0, "listing", "long", 1, false},
		{"tg:WatcherGuru", "JUST IN: Protocol XYZ hacked, $50M drained", "", 0, "hack", "short", 2, true},
		{"rss", "SEC approves spot Solana ETF", "", 0, "etf", "long", 1, false},
		{"rss", "Court rejects Grayscale ETF appeal", "", 0, "etf", "short", 1, false},
		{"tg:whale_alert_io", "🚨 1,000 #BTC (67,000,000 USD) transferred from unknown wallet to #Binance", "", 0, "whale", "short", 1, false},
		{"tg:whale_alert_io", "🚨 50,000,000 #USDT (50,000,000 USD) transferred from unknown wallet to #Coinbase", "", 0, "whale", "long", 1, false},
		{"tg:whale_alert_io", "🚨 1,000 #BTC transferred from #Binance to unknown wallet", "", 0, "whale", "long", 1, false},
		{"tg:whale_alert_io", "🚨 1,000 #BTC transferred from #Binance to #Coinbase", "", 0, "whale", "", 1, false},
		{"onchain", "鏈上:1.2M ARB 轉入 Binance 14", "whale", 2e6, "whale", "short", 1, false},
		{"onchain", "鏈上:1.2M ARB 轉出 Binance 14", "whale", -2e6, "whale", "long", 1, false},
		{"robinhood", "Robinhood 上架 Sui(SUI)", "listing_us", 0, "listing_us", "long", 2, true},
		{"premium", "x", "premium", 12, "premium", "long", 1, false},
		{"snapshot", "[AAVE 治理提案] x", "governance", 0, "governance", "", 1, false},
		{"rss", "Bitcoin price consolidates near $100K", "", 0, "other", "", 0, false},
	}
	for _, c := range cases {
		cat, dir, str, trad := naClassify(c.src, c.title, c.preset, c.value)
		if cat != c.cat || dir != c.dir || str != c.str || trad != c.trad {
			t.Errorf("%s %q → %s/%s/%d/%v, want %s/%s/%d/%v", c.src, c.title, cat, dir, str, trad, c.cat, c.dir, c.str, c.trad)
		}
	}
}

func TestNAEventID(t *testing.T) {
	if id := naEventID("binance", "abc"); id != "binance|abc" {
		t.Fatal(id)
	}
	long := naEventID("rss", "https://example.com/"+strings.Repeat("x", 300))
	if len(long) > 191 || !strings.HasPrefix(long, "rss|h:") {
		t.Fatalf("long id not hashed: %d %s", len(long), long)
	}
	if naEventID("rss", "https://example.com/"+strings.Repeat("x", 300)) != long {
		t.Fatal("hash not stable")
	}
}

func TestFillNAReact(t *testing.T) {
	cs := make([]exchange.Candle, 240)
	for i := range cs {
		p := 100 + float64(i)*0.1 // 每分鐘 +0.1
		cs[i] = exchange.Candle{Open: p, Close: p + 0.1, High: p + 0.5, Low: p - 0.5}
	}
	cs[10].Low = 90 // 60 分鐘內最深的插針
	ev := NewsEvent{Price0: 100, Filled: 2}
	fillNAReact(&ev, cs)
	chk := func(name string, got *float64, want float64) {
		t.Helper()
		if got == nil || *got != want {
			if got == nil {
				t.Errorf("%s = nil, want %v", name, want)
				return
			}
			t.Errorf("%s = %v, want %v", name, *got, want)
		}
	}
	chk("r1m", ev.R1m, 0.1) // cs[0].Close = 100.1
	chk("r5m", ev.R5m, 0.5) // cs[4].Close = 100.5
	chk("r1h", ev.R1h, 6)   // cs[59].Close = 106
	chk("r4h", ev.R4h, 24)  // cs[239].Close = 124
	chk("dn60", ev.Dn60, -10)
	chk("up60", ev.Up60, 6.4) // cs[59].High = 105.9+0.5

	// 只到 1h 階段:不填 4h;K 線不夠 15 根就不填 15m 以後
	ev2 := NewsEvent{Filled: 1}
	fillNAReact(&ev2, cs[:10])
	if ev2.Price0 != 100 || ev2.R5m == nil || ev2.R15m != nil || ev2.R4h != nil {
		t.Errorf("partial fill wrong: %+v", ev2)
	}
}

func TestComputeNewsStats(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	evs := []NewsEvent{
		{Source: "binance", Category: "listing", Coin: "A", Dir: "long", R1h: f(5), Up60: f(8), Dn60: f(-1), AIDir: "long", Filled: 1},
		{Source: "binance", Category: "listing", Coin: "B", Dir: "long", R1h: f(-1), Up60: f(2), Dn60: f(-3), AIDir: "short", Filled: 1},
		{Source: "binance", Category: "delist", Coin: "C", Dir: "short", R1h: f(-4), Filled: 1},
		{Source: "rss", Category: "other"},
	}
	st := computeNewsStats(evs, 0)
	if st.Total != 4 || st.Filled != 3 {
		t.Fatalf("total/filled = %d/%d", st.Total, st.Filled)
	}
	var listing, delist NewsStatRow
	for _, r := range st.ByCategory {
		switch r.Key {
		case "listing":
			listing = r
		case "delist":
			delist = r
		}
	}
	if listing.N != 2 || listing.Cnt[3] != 2 || listing.Avg[3] != 2 || listing.Edge[3] != 2 || listing.Hit[3] != 50 {
		t.Errorf("listing row wrong: %+v", listing)
	}
	if listing.AIN != 2 || listing.AIHit != 100 || listing.Up60 != 5 || listing.Dn60 != -2 || listing.Best != "1h" {
		t.Errorf("listing ai/updn wrong: %+v", listing)
	}
	if delist.Edge[3] != 4 || delist.Hit[3] != 100 { // 做空:跌 4% = 照方向賺 4%
		t.Errorf("delist row wrong: %+v", delist)
	}
	if len(st.BySource) != 2 || st.BySource[0].Key != "binance" {
		t.Errorf("by source wrong: %+v", st.BySource)
	}
}

func TestParseNAAI(t *testing.T) {
	dir, str, reason, zh, ok := parseNAAI("```json\n{\"dir\":\"short\",\"strength\":5,\"reason\":\"下架利空\",\"zh\":\"幣安將下架 ABC\"}\n```")
	if !ok || dir != "short" || str != 3 || reason != "下架利空" || zh != "幣安將下架 ABC" {
		t.Fatalf("got %s %d %s %s %v", dir, str, reason, zh, ok)
	}
	if _, _, _, _, ok := parseNAAI("無法判斷"); ok {
		t.Fatal("expected failure")
	}
	if dir, _, _, _, _ := parseNAAI(`{"dir":"maybe","strength":1}`); dir != "neutral" {
		t.Fatal(dir)
	}
}

func TestNABusy(t *testing.T) {
	b := newNewsAlphaBook()
	s := &Store{}
	now := mustTime("2026-10-27T10:00:00Z")
	b.mu.Lock()
	tr := s.openMicroTrade(b, "ABC", "long", 1, 0.95, 1.1, now, now)
	if !naBusy(b, "ABC", now) || naBusy(b, "XYZ", now) {
		t.Fatal("open trade should block same coin only")
	}
	closeTrade(tr, 1.1, "tp3", now)
	if !naBusy(b, "ABC", now.Add(30*60e9)) || naBusy(b, "ABC", now.Add(61*60e9)) {
		t.Fatal("cooldown should be 1h")
	}
	b.mu.Unlock()
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNAIngestSeedAndDedupe(t *testing.T) {
	s := &Store{news: newNewsAlpha()}
	n := s.news
	n.pxAt = time.Now().Add(time.Hour) // 不要真的去抓價格
	n.perp = map[string]float64{"BTC": 100000, "1000PEPE": 0.01, "HYPE": 40}
	n.spot = map[string]float64{"PEPE": 0.00001}
	src := &naSource{key: "binance", label: "幣安公告", seed: true}
	n.register(src)
	mk := func(id, title string) naIn {
		return naIn{Item: newsfeedItem("binance", id, title)}
	}
	s.naIngest(src, []naIn{mk("a", "Binance Will List Hyperliquid (HYPE)")})
	if len(n.events) != 0 {
		t.Fatalf("seeding fetch must not emit, got %d", len(n.events))
	}
	s.naIngest(src, []naIn{mk("a", "Binance Will List Hyperliquid (HYPE)"), mk("b", "Binance Will Delist PEPE and NEWCOIN (NEWCOIN)")})
	if len(n.events) != 1 {
		t.Fatalf("want 1 new event, got %d", len(n.events))
	}
	ev := n.events[0]
	if ev.Category != "delist" || ev.Dir != "short" || ev.Coin != "1000PEPE" || ev.Price0 != 0.01 || !ev.Tradable {
		t.Fatalf("bad event: %+v", ev)
	}
	if n.stat["binance"].Events != 1 {
		t.Fatalf("stat events = %d", n.stat["binance"].Events)
	}
	// 新幣還沒行情:官方公告放寬比對括號代號
	s.naIngest(src, []naIn{mk("c", "Binance Will List Zora (ZORA)")})
	if ev := n.events[0]; ev.Coin != "ZORA" || ev.Tradable || ev.Price0 != 0 {
		t.Fatalf("new coin event wrong: %+v", ev)
	}
}

func newsfeedItem(src, id, title string) newsfeed.Item {
	return newsfeed.Item{Source: src, ID: id, Title: title, Lang: "en"}
}
