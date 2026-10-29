package cache

import (
	"time"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
)

// pulsarv16.go — 脈衝星v16:= v12(盤中、v3 條件,成立就進)+「進場健康度」:紅旗 ≥ 3 個不進。
//
// 「不健康」= 進場後幾乎沒漲(< 0.25R)就直接打止損。用 v3 / v12 有進場因子的單(前段 10/22 以前、
// 後段 10/23 之後,兩本都看)找出四個進場當下就看得到的紅旗,紅旗越多越容易一買就跌:
//
//	① 最近 1 小時買盤過熱(1h CVD > 10%)       已經有人猛買一輪,容易追在最後一棒
//	② 24 小時漲幅 < 2%                          幣本身沒趨勢,只是突然一根爆量
//	③ 台灣時間 14~19 點進場                      這段時間的單最常一買就跌
//	④ 散戶做多比例 < 64%                         (抓不到多空比時不算)
//
// 紅旗 ≥ 3:四組資料一致最差(不健康 23~35%、勝率 22~35%)。v16 只擋這一組;0~2 個照常進場,
// 並把燈號(🟢 0~1 / 🟡 2)記在單上,之後可以分燈號驗證。其餘(選幣、出場、逾時、冷卻)同 v12。
// ⚠️ 樣本只有約 1500 筆、只能少踩雷,不保證轉正,屬觀察用。
const (
	v16MaxFlags    = 2    // 紅旗超過這個數就不進
	v16HotCVD1h    = 10.0 // ① 1h CVD 上限(%)
	v16MinChg24    = 2.0  // ② 24h 漲幅下限(%)
	v16MinLongAcct = 0.64 // ④ 散戶做多比例下限
)

var taipei = time.FixedZone("Asia/Taipei", 8*3600)

func (s *Store) newPulsarV16Book() *microBook {
	return &microBook{name: "pulsarv16", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, runnerExpiry: 96,
		cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeBoardCoins, signal: surgeV3Signal,
		intraSignal: surgeV12Signal, gate: s.v16Gate, tagEntry: true}
}

// v16Gate 在 v12 訊號成立後才呼叫(在書的鎖內):CVD / 24h 漲幅由手上的 15m K 線算,多空比走快取。
func (s *Store) v16Gate(coin string, cs []exchange.Candle) bool {
	chg24 := 0.0
	if n := len(cs); n >= 97 {
		chg24 = indicator.PctChange(cs[n-97].Close, cs[n-1].Close)
	}
	flags := pulsarHealthFlags(indicator.CVDFromKlines(cs, 4), chg24, s.longShortCached(coin).LongAccount, time.Now())
	return len(flags) <= v16MaxFlags
}

// pulsarHealthFlags 回傳亮起的紅旗(白話)。chg24 / longAcct 為 0 視為抓不到資料、不算紅旗。
func pulsarHealthFlags(cvd1h, chg24, longAcct float64, at time.Time) []string {
	var out []string
	if cvd1h > v16HotCVD1h {
		out = append(out, "1 小時買盤過熱")
	}
	if chg24 != 0 && chg24 < v16MinChg24 {
		out = append(out, "24 小時漲幅不到 2%")
	}
	if h := at.In(taipei).Hour(); h >= 14 && h <= 19 {
		out = append(out, "下午 14~19 點")
	}
	if longAcct != 0 && longAcct < v16MinLongAcct {
		out = append(out, "散戶做多不到 64%")
	}
	return out
}

// healthBooks:要顯示「進場健康度」燈號的書。
var healthBooks = map[string]bool{"pulsarv12": true, "pulsarv16": true}

// annotateHealth 依單上記錄的進場因子補上燈號(serve 時算,不存 DB)。沒有因子的舊單不標。
func annotateHealth(book string, trs []*PaperTrade) {
	if !healthBooks[book] {
		return
	}
	for _, tr := range trs {
		if tr.CVD1h == 0 && tr.Chg24 == 0 && tr.LongAcct == 0 {
			continue
		}
		tr.HealthFlags = pulsarHealthFlags(tr.CVD1h, tr.Chg24, tr.LongAcct, tr.OpenTime)
		tr.Health = healthLight(len(tr.HealthFlags))
	}
}

// healthLight:🟢 0~1 個紅旗、🟡 2 個、🔴 3 個以上。
func healthLight(n int) string {
	switch {
	case n >= 3:
		return "red"
	case n == 2:
		return "yellow"
	}
	return "green"
}

// healthNote 給開倉推播用的一句話(非燈號書回空字串)。
func healthNote(book string, tr *PaperTrade) string {
	if !healthBooks[book] {
		return ""
	}
	f := pulsarHealthFlags(tr.CVD1h, tr.Chg24, tr.LongAcct, tr.OpenTime)
	switch healthLight(len(f)) {
	case "red":
		return " · 🔴 不健康"
	case "yellow":
		return " · 🟡 謹慎"
	}
	return " · 🟢 健康"
}
