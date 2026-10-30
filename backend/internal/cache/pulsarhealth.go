package cache

import "time"

// pulsarhealth.go — 脈衝星v12 的「進場健康度」燈號(只標示、不擋單;serve 時由進場因子算,不存 DB)。
//
// 四個紅旗(進場當下就看得到):
//
//	① 最近 1 小時買盤過熱(1h CVD > 10%)
//	② 24 小時漲幅 < 2%
//	③ 台灣時間 14~19 點進場
//	④ 散戶做多比例 < 64%(抓不到多空比時不算)
//
// 🟢 0~1 個、🟡 2 個、🔴 3 個以上。⚠️ 10/04~10/12 的驗證資料上,「紅旗 ≥3」並沒有比較差
// (只有 ③ 時段一項三段都站得住),燈號僅供參考。
const (
	hfHotCVD1h    = 10.0 // ① 1h CVD 上限(%)
	hfMinChg24    = 2.0  // ② 24h 漲幅下限(%)
	hfMinLongAcct = 0.64 // ④ 散戶做多比例下限
)

var taipei = time.FixedZone("Asia/Taipei", 8*3600)

// pulsarHealthFlags 回傳亮起的紅旗(白話)。chg24 / longAcct 為 0 視為抓不到資料、不算紅旗。
func pulsarHealthFlags(cvd1h, chg24, longAcct float64, at time.Time) []string {
	var out []string
	if cvd1h > hfHotCVD1h {
		out = append(out, "1 小時買盤過熱")
	}
	if chg24 != 0 && chg24 < hfMinChg24 {
		out = append(out, "24 小時漲幅不到 2%")
	}
	if h := at.In(taipei).Hour(); h >= 14 && h <= 19 {
		out = append(out, "下午 14~19 點")
	}
	if longAcct != 0 && longAcct < hfMinLongAcct {
		out = append(out, "散戶做多不到 64%")
	}
	return out
}

// healthBooks:要顯示「進場健康度」燈號的書(目前只有脈衝星v12)。
var healthBooks = map[string]bool{"pulsarv12": true}

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
