package cache

import (
	"testing"
	"time"
)

func TestHistFromMs(t *testing.T) {
	tpe, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		t.Skip("no tzdata")
	}
	cases := []struct{ now, want time.Time }{
		{time.Date(2026, 11, 4, 12, 0, 0, 0, tpe), time.Date(2026, 9, 1, 0, 0, 0, 0, tpe)},
		{time.Date(2026, 1, 15, 0, 0, 0, 0, tpe), time.Date(2025, 11, 1, 0, 0, 0, 0, tpe)}, // 跨年
		{time.Date(2026, 3, 31, 23, 59, 0, 0, tpe), time.Date(2026, 1, 1, 0, 0, 0, 0, tpe)},
		// UTC 10/31 20:00 = 台北 11/1 04:00 → 已經是 11 月,從 9/1 起
		{time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 0, 0, 0, 0, tpe)},
	}
	for _, c := range cases {
		if got := histFromMs(c.now); got != c.want.UnixMilli() {
			t.Errorf("histFromMs(%v) = %v, want %v", c.now, time.UnixMilli(got).In(tpe), c.want)
		}
	}
}
