package cache

import (
	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
)

// pulsarv15.go — 脈衝星v15:= v12(盤中、v3 條件,成立就進)+ 兩道進場濾網。
//
// 依據 v3 / v12 實盤紀錄(10/13–10/22 有進場因子的單,前後兩段分開驗證,兩段、兩本都一致):
//
//	① 進場前 1 小時 CVD > 10%(最近 1 小時買盤已經非常猛)→ 追在最後一棒,兩段都大虧 → 不進
//	② 散戶做多比例 < 60% → 兩段都是最差的一組 → 不進(抓不到多空比時不擋)
//
// 其餘(選幣、出場、逾時、冷卻)完全同 v12。⚠️ 濾網後樣本只有約 200 筆,仍屬觀察用。
const (
	v15MaxCVD1h   = 10.0 // 進場前 1h CVD 上限(%)
	v15MinLongAcc = 0.60 // 散戶做多比例下限
)

func (s *Store) newPulsarV15Book() *microBook {
	return &microBook{name: "pulsarv15", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, runnerExpiry: 96,
		cooldown: 16, keep: 500, plan: tpPulsarV3, universe: s.surgeBoardCoins, signal: surgeV3Signal,
		intraSignal: surgeV12Signal, gate: s.v15Gate, tagEntry: true}
}

// v15Gate 在 v12 訊號成立後才呼叫(在書的鎖內),CVD 由手上的 K 線算、多空比走 oiCache 快取。
func (s *Store) v15Gate(coin string, cs []exchange.Candle) bool {
	return v15OK(indicator.CVDFromKlines(cs, 4), s.longShortCached(coin).LongAccount)
}

// v15OK:1h CVD ≤ 10%,且散戶做多 ≥ 60%(longAcct 為 0 = 抓不到資料,不擋)。
func v15OK(cvd1h, longAcct float64) bool {
	return cvd1h <= v15MaxCVD1h && (longAcct == 0 || longAcct >= v15MinLongAcc)
}
