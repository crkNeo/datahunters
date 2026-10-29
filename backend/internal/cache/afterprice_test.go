package cache

import (
	"testing"
	"time"
)

func TestAfterSlotAndSkip(t *testing.T) {
	tr := &PaperTrade{ID: "x", Coin: "ABC", Entry: 10, Status: "closed", OpenTime: time.Now().Add(-3 * time.Hour)}
	v := 1.5
	*afterSlot(tr, 2) = &v
	if tr.After4h == nil || *tr.After4h != 1.5 || tr.After1h != nil {
		t.Fatal("slot mapping wrong")
	}
	// 只有 1H、2H 到期(3 小時前進場);4H 已有值也不重補。用不到網路:沒有 book 時直接結束。
	s := &Store{}
	s.AfterPriceTick()
}

func TestAfterJobs(t *testing.T) {
	now := time.Now().UnixMilli()
	open := now - int64(3*time.Hour/time.Millisecond) // 3 小時前進場:1H、2H 到期,4H、8H 還沒
	if _, ok := newAfterJob("a", "ABC", 10, open, 0, now); !ok {
		t.Fatal("1H should be due")
	}
	if _, ok := newAfterJob("a", "ABC", 10, open, 2, now); ok {
		t.Fatal("4H should not be due yet")
	}
	// 沒有資料庫:從記憶體找,已經有值的不重抓,新的單先
	b := &microBook{name: "pulsarv12"}
	v := 1.0
	old := &PaperTrade{ID: "old", Coin: "OLD", Entry: 1, Status: "closed", OpenTime: time.UnixMilli(open - 86400_000)}
	cur := &PaperTrade{ID: "new", Coin: "NEW", Entry: 1, Status: "open", OpenTime: time.UnixMilli(open), After1h: &v}
	b.trades = []*PaperTrade{old, cur}
	s := &Store{pulsarV12Book: b}
	jobs := s.afterMemTodo(now)
	if len(jobs) != 5 || jobs[0].id != "new" || jobs[0].i != 1 {
		t.Fatalf("jobs = %+v", jobs)
	}
	// 抓不到超過上限 → 列入放棄名單
	afterFails.Lock()
	afterFails.m["old|8h"] = afterMaxTries
	afterFails.Unlock()
	if d := afterDead(); len(d) != 1 || d[0] != "old" {
		t.Fatalf("dead = %v", d)
	}
	if _, ok := newAfterJob("old", "OLD", 1, open-86400_000, 3, now); ok {
		t.Fatal("given-up slot should be skipped")
	}
}
