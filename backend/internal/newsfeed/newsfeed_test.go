package newsfeed

import (
	"math/big"
	"testing"
	"time"
)

func TestParseBinanceBothShapes(t *testing.T) {
	a := `{"code":"000000","data":{"articles":[{"id":1,"code":"abc123","title":"Binance Will List Foo (FOO)","releaseDate":1730000000000}]}}`
	b := `{"data":{"catalogs":[{"catalogId":48,"articles":[{"id":2,"code":"def456","title":"Binance Will Delist BAR","releaseDate":1730000001000}]}]}}`
	seen := map[string]bool{}
	x, err := parseBinance([]byte(a), seen)
	y, err2 := parseBinance([]byte(b), seen)
	if err != nil || err2 != nil || len(x) != 1 || len(y) != 1 {
		t.Fatalf("x=%v y=%v err=%v %v", x, y, err, err2)
	}
	if x[0].ID != "abc123" || x[0].URL != "https://www.binance.com/en/support/announcement/abc123" || x[0].Published.UnixMilli() != 1730000000000 {
		t.Errorf("binance item: %+v", x[0])
	}
	if z, _ := parseBinance([]byte(a), seen); len(z) != 0 {
		t.Error("同一個 code 不該重複")
	}
}

func TestParseOKXBybit(t *testing.T) {
	okx := `{"code":"0","data":[{"details":[{"title":"OKX to list FOO","url":"https://www.okx.com/help/x","pTime":"1730000000000","annType":"announcements-new-listings"}],"totalPage":"1"}],"msg":""}`
	items, err := parseOKX([]byte(okx))
	if err != nil || len(items) != 1 || items[0].URL != "https://www.okx.com/help/x" || items[0].Published.UnixMilli() != 1730000000000 {
		t.Fatalf("okx: %+v %v", items, err)
	}
	if _, err := parseOKX([]byte(`{"code":"50011","msg":"rate limit","data":[]}`)); err == nil {
		t.Error("OKX 非 0 code 應回錯誤")
	}
	by := `{"retCode":0,"retMsg":"OK","result":{"total":1,"list":[{"title":"New Listing: FOOUSDT Perpetual","url":"https://announcements.bybit.com/x","dateTimestamp":1730000000000,"publishTime":1730000005000}]}}`
	items, err = parseBybit([]byte(by))
	if err != nil || len(items) != 1 || items[0].Published.UnixMilli() != 1730000005000 {
		t.Fatalf("bybit: %+v %v", items, err)
	}
}

func TestParseBithumb(t *testing.T) {
	next := `<html><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"noticeList":[{"id":1234,"title":"[마켓 추가] 푸(FOO) 원화 마켓 추가","publicationDateTime":"2026-10-27 10:00:00"}]}}}</script></html>`
	items, err := parseBithumb(next)
	if err != nil || len(items) != 1 || items[0].ID != "1234" || items[0].URL != "https://feed.bithumb.com/notice/1234" {
		t.Fatalf("next: %+v %v", items, err)
	}
	if want := time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC); !items[0].Published.Equal(want) {
		t.Errorf("KST 時間換算錯: %v", items[0].Published)
	}
	anchors := `<ul><li><a class="x" href="/notice/99"><span>[거래] 바(BAR) 거래지원 종료</span></a></li></ul>`
	items, err = parseBithumb(anchors)
	if err != nil || len(items) != 1 || items[0].Title != "[거래] 바(BAR) 거래지원 종료" {
		t.Fatalf("anchors: %+v %v", items, err)
	}
	if _, err := parseBithumb("<html></html>"); err == nil {
		t.Error("沒有公告應回錯誤")
	}
}

func TestParseTelegram(t *testing.T) {
	html := `<div class="tgme_widget_message_wrap js-widget_message_wrap"><div class="tgme_widget_message" data-post="whale_alert_io/100">
<div class="tgme_widget_message_text js-message_text" dir="auto">🚨 1,000 <b>#BTC</b> transferred from unknown wallet to <a href="x">#Binance</a></div>
<a class="tgme_widget_message_date"><time datetime="2026-10-27T01:02:03+00:00" class="time">01:02</time></a></div></div>
<div class="tgme_widget_message_wrap js-widget_message_wrap"><div class="tgme_widget_message" data-post="whale_alert_io/101"><div class="photo"></div>
<a><time datetime="2026-10-27T01:03:00+00:00">x</time></a></div></div>`
	items := parseTelegram(html, "whale_alert_io", "en")
	if len(items) != 1 || items[0].ID != "whale_alert_io/100" || items[0].Source != "tg:whale_alert_io" {
		t.Fatalf("tg: %+v", items)
	}
	if items[0].Title != "🚨 1,000 #BTC transferred from unknown wallet to #Binance" || items[0].Published.Hour() != 1 {
		t.Errorf("tg text/time: %+v", items[0])
	}
}

func TestExtractTickers(t *testing.T) {
	known := func(s string) bool {
		return map[string]bool{"FOO": true, "BTC": true, "ETH": true, "SOL": true, "ON": true}[s]
	}
	cases := map[string][]string{
		"Binance Will List Foo (FOO) and Bar (BAR)": {"FOO"},
		"[마켓 추가] 푸(FOO) 원화 마켓 추가":                   {"FOO"},
		"New Listing: FOOUSDT Perpetual Contract":   {"FOO"},
		"KRW-SOL 거래 유의 종목 지정":                       {"SOL"},
		"BTC and ETH rally as ETF inflows hit ATH":  {"BTC", "ETH"},
		"Fed holds rates ON hold":                   nil, // ON 在停用字
	}
	for in, want := range cases {
		got := ExtractTickers(in, known)
		if len(got) != len(want) {
			t.Errorf("%q → %v want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q → %v want %v", in, got, want)
			}
		}
	}
}

func TestDecodeABIString(t *testing.T) {
	// "USDC" 的 ABI string 回傳值
	abi := "0x0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000004" +
		"5553444300000000000000000000000000000000000000000000000000000000"
	if s := decodeABIString(abi); s != "USDC" {
		t.Errorf("abi string → %q", s)
	}
	// 舊代幣回 bytes32("MKR")
	if s := decodeABIString("0x4d4b520000000000000000000000000000000000000000000000000000000000"); s != "MKR" {
		t.Errorf("bytes32 → %q", s)
	}
	if v := TokenAmount(big.NewInt(1_500_000), 6); v != 1.5 {
		t.Errorf("amount=%v", v)
	}
}

func TestLooseTickers(t *testing.T) {
	got := LooseTickers("Binance Will List Hyperliquid (HYPE) and ZORAUSDT Perpetual; KRW-ABC (USDT)")
	want := []string{"HYPE", "ZORA", "ABC"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
