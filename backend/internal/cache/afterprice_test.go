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

func TestAfterDumpRevHours(t *testing.T) {
	now := time.Now().UnixMilli()
	open := now - int64(20*time.Hour/time.Millisecond) // 20 小時前進場:1~16H 到期,24H 還沒
	v12 := &microBook{name: "pulsarv12"}
	dr := &microBook{name: "dumprev"}
	v12.trades = []*PaperTrade{{ID: "p", Coin: "PPP", Entry: 1, Status: "closed", OpenTime: time.UnixMilli(open)}}
	dr.trades = []*PaperTrade{{ID: "d", Coin: "DDD", Entry: 1, Status: "open", OpenTime: time.UnixMilli(open)}}
	s := &Store{pulsarV12Book: v12, dumpRevBook: dr}
	n := map[string]int{}
	for _, j := range s.afterMemTodo(now) {
		n[j.id]++
	}
	if n["p"] != 4 || n["d"] != 5 { // v12 只補到 8H;流星補到 16H,24H 還沒到
		t.Fatalf("jobs = %v", n)
	}
	x := 2.0
	*afterSlot(dr.trades[0], 5) = &x
	if dr.trades[0].After24h == nil || *dr.trades[0].After24h != 2 {
		t.Fatal("24h slot mapping wrong")
	}
}
