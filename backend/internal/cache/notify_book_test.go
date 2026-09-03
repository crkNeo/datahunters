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

	// 訂單塊 多週期:傳 orderblock_1h/orderblock_4h,開關讀的是合併後的 orderblock
	fam := &Store{stratCfg: map[string]StratCfg{
		"orderblock": {NotifyOpen: true, NotifyClose: true},
	}}
	if !fam.notifyOpenBook("orderblock_1h", tr) {
		t.Error("orderblock_1h 的開關應併回 orderblock 讀取")
	}
	if !fam.notifyCloseBook("orderblock_4h", tr, time.Now(), false) {
		t.Error("orderblock_4h 的開關應併回 orderblock 讀取")
	}
}
