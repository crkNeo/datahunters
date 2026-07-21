package cache

import "testing"

// 後台「最大止損%」必須對每個策略都生效。銀河(emaonly)與冥王星(conv)原本
// 完全沒讀這個設定 —— 後台設 10% 卻開出 46% 止損的單(AKE 實例)。
func TestSLWithinCap(t *testing.T) {
	s := &Store{stratCfg: map[string]StratCfg{}}

	// 沒設定 → 不限制,一律通過
	if !s.slWithinCap("emaonly", 0.0017191, 0.0025142) {
		t.Error("未設定上限時不該擋單")
	}

	// 設 10% 上限
	s.stratCfg["emaonly"] = StratCfg{MaxSLPct: 10}

	// 螢幕上那筆 AKE 做空:進場 0.0017191、止損 0.0025142 → 46.25%,必須被擋
	if s.slWithinCap("emaonly", 0.0017191, 0.0025142) {
		t.Error("46.25% 的止損距離超過 10% 上限,應該被擋掉")
	}
	// 做多方向同理(止損在下方)
	if s.slWithinCap("emaonly", 100, 50) {
		t.Error("做多 50% 止損距離應該被擋掉")
	}
	// 剛好在界內要放行
	if !s.slWithinCap("emaonly", 100, 91) {
		t.Error("9% 的止損距離在 10% 上限內,不該被擋")
	}
	// 邊界值:剛好等於上限視為通過
	if !s.slWithinCap("emaonly", 100, 90) {
		t.Error("剛好 10% 應該通過")
	}
	// entry<=0 算不出百分比 → 不擋(交給後續的價格檢查)
	if !s.slWithinCap("emaonly", 0, 5) {
		t.Error("entry=0 時不該在這裡擋單")
	}
	// 其他策略沒設定就不受影響
	if !s.slWithinCap("conv", 100, 50) {
		t.Error("conv 未設定上限,不該被 emaonly 的設定影響")
	}
}
