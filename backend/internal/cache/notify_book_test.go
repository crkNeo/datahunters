package cache

import (
	"testing"
	"time"
)

// 冥王星與微策略原本開倉/平倉完全不通知(只有 TP)。這裡驗證 book-keyed 的
// 開倉/平倉通知會正確吃「後台通知開關」—— 這是使用者回報「打開通知卻沒收到」的根因。
func TestNotifyBookRespectsToggle(t *testing.T) {
	tr := &PaperTrade{Coin: "AKE", Dir: "short", Entry: 0.0017, TP: 0.0009, SL: 0.0025, Status: "open", OpenTime: time.Now()}

	// 開關全開 → 通過閘門
	on := &Store{stratCfg: map[string]StratCfg{
		"conv": {NotifyOpen: true, NotifyClose: true},
	}}
	if !on.notifyOpenBook("conv", tr) {
		t.Error("NotifyOpen=true 時開倉通知應該通過")
	}
	if !on.notifyCloseBook("conv", tr, time.Now(), false) {
		t.Error("NotifyClose=true 時平倉通知應該通過")
	}

	// 開關全關 → 被閘門擋下(force=false)
	off := &Store{stratCfg: map[string]StratCfg{
		"conv": {NotifyOpen: false, NotifyClose: false},
	}}
	if off.notifyOpenBook("conv", tr) {
		t.Error("NotifyOpen=false 時開倉通知不該發出")
	}
	if off.notifyCloseBook("conv", tr, time.Now(), false) {
		t.Error("NotifyClose=false 時平倉通知不該發出")
	}
	// force=true(手動平倉)無視開關
	if !off.notifyCloseBook("conv", tr, time.Now(), true) {
		t.Error("force=true 應繞過平倉開關")
	}

	// bgv2 家族:傳 bgv2dev/bgv2boll,開關讀的是合併後的 bgv2
	fam := &Store{stratCfg: map[string]StratCfg{
		"bgv2": {NotifyOpen: true, NotifyClose: true},
	}}
	if !fam.notifyOpenBook("bgv2dev", tr) {
		t.Error("bgv2dev 的開關應併回 bgv2 讀取")
	}
	if !fam.notifyCloseBook("bgv2boll", tr, time.Now(), false) {
		t.Error("bgv2boll 的開關應併回 bgv2 讀取")
	}
}
