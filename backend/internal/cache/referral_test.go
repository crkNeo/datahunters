package cache

import (
	"testing"
	"time"
)

// 兌換規則:每 10 積分解鎖一次額度,額度可換 30U 或周邊(各消耗一次)。
func TestRefTier(t *testing.T) {
	cases := []struct {
		qualified, applied int
		can                bool
		need               int
	}{
		{0, 0, false, 10},  // 什麼都沒有
		{9, 0, false, 1},   // 差一個
		{10, 0, true, 0},   // 第一次額度
		{10, 1, false, 10}, // 用掉了,要再 10 個
		{20, 1, true, 0},   // 20 分 → 兩次額度,還剩一次
		{20, 2, false, 10}, // 兩次都用掉(1×30U + 1 周邊)
		{25, 2, false, 5},  // 25 分還是只有 2 次
		{30, 2, true, 0},
	}
	for _, c := range cases {
		can, _, need := refTier(c.qualified, c.applied)
		if can != c.can || need != c.need {
			t.Errorf("refTier(%d,%d) = (%v,need %d), want (%v,need %d)",
				c.qualified, c.applied, can, need, c.can, c.need)
		}
	}
}

// 月上限的分界點:用本地時區的自然月,不是滾動 30 天。
func TestMonthStartMs(t *testing.T) {
	mar15 := time.Date(2026, 3, 15, 13, 45, 0, 0, time.Local)
	mar1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local)
	if got := monthStartMs(mar15); got != mar1.UnixMilli() {
		t.Errorf("monthStartMs = %d, want %d", got, mar1.UnixMilli())
	}
	// 月初第一秒就該算進新的月份(上個月的兌換不能佔用這個月的額度)
	if monthStartMs(mar1) != mar1.UnixMilli() {
		t.Error("月初當下應該等於自己")
	}
	// 跨月要真的變號
	feb28 := time.Date(2026, 2, 28, 23, 59, 59, 0, time.Local)
	if monthStartMs(feb28) >= monthStartMs(mar1) {
		t.Error("2 月的月起點應該早於 3 月")
	}
}

// 沒有 DB 時所有申請都該被擋掉,不能 panic。
func TestRewardBlockerNoDB(t *testing.T) {
	s := &Store{}
	for _, k := range []string{kindUSDT, kindMerch} {
		if why := s.rewardBlocker("someone", k, 100, 0, time.Now()); why == "" {
			t.Errorf("kind=%s 在沒有 DB 時不該放行", k)
		}
	}
	if _, _, left := s.MerchStock(); left != 0 {
		t.Error("沒有 DB 時庫存該是 0")
	}
	if s.SetMerchStock(5) {
		t.Error("沒有 DB 時不該回報設定成功")
	}
}

func TestApplyRewardRejectsBadKind(t *testing.T) {
	s := &Store{}
	if err := s.ApplyReward("someone", "bitcoin"); err == nil {
		t.Error("不認識的品項應該被拒絕")
	}
}
