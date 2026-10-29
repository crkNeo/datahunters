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
