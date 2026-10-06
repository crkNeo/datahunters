package cache

import (
	"testing"
	"time"
)

// 銀河(holdBE):TP1 後停損到保本,TP2 後停損「維持保本」,不上移到 TP1。
// 對照 tpMomentum:TP2 後停損會上移到 TP1。
func TestHoldBEStop(t *testing.T) {
	now := time.Now()
	mk := func(p *tpPlan) *PaperTrade {
		tr := &PaperTrade{Dir: "long", Entry: 100, SL: 99, TP: 101, Cur: 100, Status: "open"}
		setupTP(tr, p)
		if tr.TP1 == 0 || tr.TP2 == 0 {
			t.Fatalf("分段沒生效:TP1=%v TP2=%v", tr.TP1, tr.TP2)
		}
		return tr
	}

	// holdBE
	tr := mk(tpEMAHoldBE)
	stepTP(tr, tr.TP1, tpEMAHoldBE, true, now) // 觸 TP1 → 停損到保本
	if tr.Legs != 1 {
		t.Fatalf("TP1 後 legs=%d, want 1", tr.Legs)
	}
	beSL := tr.SL
	if beSL <= tr.Entry*0.999 || beSL >= tr.Entry*1.01 {
		t.Fatalf("TP1 後停損應在開倉附近(保本),實際 %.4f", beSL)
	}
	stepTP(tr, tr.TP2, tpEMAHoldBE, true, now) // 觸 TP2 → 停損應維持保本
	if tr.Legs != 2 {
		t.Fatalf("TP2 後 legs=%d, want 2", tr.Legs)
	}
	if tr.SL != beSL {
		t.Errorf("holdBE:TP2 後停損應維持保本 %.4f,實際 %.4f", beSL, tr.SL)
	}
	if tr.SL == tr.TP1 {
		t.Error("holdBE:停損不該上移到 TP1")
	}

	// 對照組:tpMomentum(非 holdBE)TP2 後停損上移到 TP1
	tr2 := mk(tpMomentum)
	stepTP(tr2, tr2.TP1, tpMomentum, true, now)
	stepTP(tr2, tr2.TP2, tpMomentum, true, now)
	if tr2.SL != tr2.TP1 {
		t.Errorf("tpMomentum:TP2 後停損應移到 TP1 %.4f,實際 %.4f", tr2.TP1, tr2.SL)
	}
}
