package cache

import (
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// blowOffSetup 造「盤整 50 根 → 12 根急漲到 118 → 爆量長上影衝到 123 → 一根小整理 → 最後一根」。
func blowOffSetup(spikeHigh, spikeClose, spikeVol float64, last func(prev exchange.Candle, i int) exchange.Candle) []exchange.Candle {
	cs := quietBars(50, 100)
	px := 100.0
	for j := 0; j < 12; j++ {
		i := len(cs)
		cs = append(cs, bar(i, px, px+1.6, px-0.1, px+1.5, 200))
		px += 1.5
	}
	cs = append(cs, bar(len(cs), 118, spikeHigh, 117.8, spikeClose, spikeVol)) // 爆量根
	cs = append(cs, bar(len(cs), spikeClose, spikeClose+0.5, 117.8, 118.6, 200))
	return append(cs, last(cs[len(cs)-1], len(cs)))
}

// 收黑、跌破前一根低點、收在爆量根實體中點之下。
func breakDown(prev exchange.Candle, i int) exchange.Candle {
	return bar(i, prev.Close, prev.Close+0.2, 116.5, 117, 250)
}

func TestBlowOffShortSignal(t *testing.T) {
	cs := blowOffSetup(123, 119, 900, breakDown)
	dir, e, sl, tp, ok := blowOffShortSignal(cs)
	if !ok || dir != "short" {
		t.Fatal("爆量長上影 + 收黑跌破 → 應做空")
	}
	if !(sl > 123 && tp < e && (sl-e)/e < 0.08) {
		t.Errorf("價位錯: entry=%v sl=%v tp=%v", e, sl, tp)
	}

	// 爆量根收在高點附近(沒有長上影)= 不是出貨 → 不做
	if _, _, _, _, ok := blowOffShortSignal(blowOffSetup(123, 122.8, 900, breakDown)); ok {
		t.Error("沒有長上影不該做空")
	}
	// 爆量根的量跟前面急漲時差不多(不是爆量)→ 不做
	if _, _, _, _, ok := blowOffShortSignal(blowOffSetup(123, 119, 200, breakDown)); ok {
		t.Error("量不夠不該做空")
	}
	// 最後一根沒跌破前兩根的最低點(還沒轉弱)→ 不做
	green := func(prev exchange.Candle, i int) exchange.Candle { return bar(i, 117.2, 119, 117, 118.9, 250) }
	if _, _, _, _, ok := blowOffShortSignal(blowOffSetup(123, 119, 900, green)); ok {
		t.Error("沒確認轉弱不該做空")
	}
	// 跌破爆量根中點、但沒跌破前兩根最低(117.8)→ 不做
	half := func(prev exchange.Candle, i int) exchange.Candle { return bar(i, 118.6, 118.7, 117.9, 118.0, 250) }
	if _, _, _, _, ok := blowOffShortSignal(blowOffSetup(123, 119, 900, half)); ok {
		t.Error("沒跌破前兩根最低不該做空")
	}
	// 沒有前面那段急漲 → 不做
	if _, _, _, _, ok := blowOffShortSignal(quietBars(80, 100)); ok {
		t.Error("沒急漲不該做空")
	}
}

func TestV14Gates(t *testing.T) {
	cs := quietBars(60, 100)
	okLong := EntryTags{Vol24: 8e6, OI12h: 6, CVD6h: 4, Funding: 0.0001, LongAcct: 0.55, Chg24: 10, BTC1h: 0.3}
	if !v14LongOK(okLong, cs) {
		t.Fatal("基準多單應通過")
	}
	for name, mod := range map[string]func(*EntryTags){
		"量太小":         func(x *EntryTags) { x.Vol24 = 1e6 },
		"OI 增加不到 5%":  func(x *EntryTags) { x.OI12h = 3 },
		"CVD 賣方主導":    func(x *EntryTags) { x.CVD6h = -2 },
		"費率擁擠":        func(x *EntryTags) { x.Funding = 0.0008 },
		"散戶做多 ≥60%":   func(x *EntryTags) { x.LongAcct = 0.62 },
		"24h 已漲 ≥25%": func(x *EntryTags) { x.Chg24 = 28 },
		"BTC 急跌":      func(x *EntryTags) { x.BTC1h = -2.5 },
	} {
		x := okLong
		mod(&x)
		if v14LongOK(x, cs) {
			t.Errorf("多單 %s 應擋下", name)
		}
	}
	noCrowdData := EntryTags{Vol24: 8e6, OI12h: 6, CVD6h: 4} // 費率/多空比抓不到(0)不擋
	if !v14LongOK(noCrowdData, cs) {
		t.Error("缺費率/多空比不該擋多單")
	}
	if v14LongOK(EntryTags{Vol24: 8e6, CVD6h: 4}, cs) { // OI 是「錢在進來」的憑證,沒有就不進
		t.Error("缺 OI 資料應擋多單")
	}

	okShort := EntryTags{Vol24: 2e7, Funding: 0.0006, LongAcct: 0.5, CVD1h: -5, OI1h: -1, BTC1h: 0.5}
	if !v14ShortOK(okShort) {
		t.Fatal("基準空單應通過")
	}
	for name, mod := range map[string]func(*EntryTags){
		"量太小":     func(x *EntryTags) { x.Vol24 = 5e6 },
		"不擁擠":     func(x *EntryTags) { x.Funding = 0.0001 },
		"買方還在主導":  func(x *EntryTags) { x.CVD1h = 3 },
		"OI 還在增加": func(x *EntryTags) { x.OI1h = 2 },
		"BTC 急漲":  func(x *EntryTags) { x.BTC1h = 2.5 },
	} {
		x := okShort
		mod(&x)
		if v14ShortOK(x) {
			t.Errorf("空單 %s 應擋下", name)
		}
	}
	crowdOnly := okShort
	crowdOnly.Funding, crowdOnly.LongAcct = 0.0001, 0.7 // 費率不高,但散戶擁擠也算
	if !v14ShortOK(crowdOnly) {
		t.Error("散戶擁擠應算擁擠")
	}
}

func TestRecentBlowOff(t *testing.T) {
	forming := func(prev exchange.Candle, i int) exchange.Candle {
		return bar(i, prev.Close, prev.Close+0.3, prev.Close-0.1, prev.Close+0.2, 100)
	}
	if !recentBlowOff(blowOffSetup(123, 119, 900, forming)) {
		t.Error("最近有長上影爆量根 → 應判定出貨")
	}
	if recentBlowOff(blowOffSetup(123, 122.8, 900, forming)) {
		t.Error("爆量根沒長上影 → 不是出貨")
	}
}

// 同一個幣:空單腿持有時,多單腿不能再開(家族互斥)。
func TestV14FamilyExclusive(t *testing.T) {
	s := &Store{stratCfg: map[string]StratCfg{}}
	books := s.newPulsarV14Books()
	long, short := books[0], books[1]
	short.trades = []*PaperTrade{{Coin: "AERO", Dir: "short", Status: "open"}}
	cs := intraSetup(cleanBar)
	now := time.UnixMilli(cs[len(cs)-1].Ts + 400_000).UTC()
	if s.microIntraOpen(long, "AERO", cs, 400, now) {
		t.Error("空單腿已持有同幣,多單腿不該開")
	}
	if long.strat() != "pulsarv14" || short.strat() != "pulsarv14" || stratKeyOf("pulsarv14s") != "pulsarv14" {
		t.Error("兩腿應共用 pulsarv14 開關")
	}
	if !observeOnlyBooks["pulsarv14"] || !observeOnlyBooks["pulsarv14s"] {
		t.Error("兩腿都必須是只觀察(不被 BITUNIX_BOOKS=all 帶進實盤)")
	}
}
