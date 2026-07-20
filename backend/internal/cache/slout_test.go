package cache

import (
	"testing"
	"time"
)

// 螢幕截圖那筆:做多 AVAAI,進場 0.008036,出場 0.008040(= 進場 ×1.0005,保本價),
// 損益 +4.19%。修正前這種單子會被標成「止損 SL」。
func TestProfitableStopIsNotLabeledSL(t *testing.T) {
	now := time.Now()

	t.Run("分批:TP1後保本被打回 → tp1sl", func(t *testing.T) {
		tr := &PaperTrade{Dir: "long", Entry: 0.008036, TP: 0.0089, SL: 0.0077, Status: "open"}
		setupTP(tr, tpMomentum)
		if tr.TP1 <= 0 {
			t.Fatal("此單應該有分批")
		}
		stepTP(tr, tr.TP1, tpMomentum, true, now) // 摸到 TP1 → 停損上移到保本
		if tr.Legs != 1 {
			t.Fatalf("Legs=%d, want 1", tr.Legs)
		}
		stepTP(tr, tr.SL, tpMomentum, true, now) // 回落打到保本停損
		if tr.Outcome != "tp1sl" {
			t.Errorf("Outcome=%q, want tp1sl", tr.Outcome)
		}
		if tr.PnLPct <= 0 {
			t.Errorf("PnL=%v,獲利出場不該是負的", tr.PnLPct)
		}
	})

	t.Run("單段/保本模式:停損已上調到成本價之上 → besl 而非 sl", func(t *testing.T) {
		tr := &PaperTrade{Dir: "long", Entry: 0.008036, TP: 0.0089, Status: "open"}
		tr.SL = roundPx(tr.Entry * 1.0005) // 保本機制把停損拉到成本價之上
		closeTrade(tr, tr.SL, "sl", now)   // 就算呼叫端寫死 "sl" 也要兜住
		if tr.Outcome != "besl" {
			t.Errorf("Outcome=%q, want besl(保本出場)", tr.Outcome)
		}
		if tr.PnLPct <= 0 {
			t.Errorf("PnL=%v, want >0", tr.PnLPct)
		}
	})

	t.Run("做空同理", func(t *testing.T) {
		tr := &PaperTrade{Dir: "short", Entry: 100, TP: 90, Status: "open"}
		tr.SL = roundPx(tr.Entry * 0.9995)
		closeTrade(tr, tr.SL, "sl", now)
		if tr.Outcome != "besl" {
			t.Errorf("Outcome=%q, want besl", tr.Outcome)
		}
	})

	t.Run("真的虧損出場仍然是 sl", func(t *testing.T) {
		tr := &PaperTrade{Dir: "long", Entry: 100, TP: 110, SL: 95, Status: "open"}
		closeTrade(tr, tr.SL, "sl", now)
		if tr.Outcome != "sl" {
			t.Errorf("Outcome=%q, want sl", tr.Outcome)
		}
		if tr.PnLPct >= 0 {
			t.Errorf("PnL=%v, want <0", tr.PnLPct)
		}
	})
}

// 手動平倉對外一律顯示成「逾時平倉」(用戶要求:客戶端不揭露人工介入)。
// 內部代碼仍是 manual/momdead,只有顯示層對應成逾時。
func TestManualOutcomeLabel(t *testing.T) {
	if got := outcomeCN("manual"); got != "逾時平倉" {
		t.Errorf("outcomeCN(manual) = %q, want 逾時平倉", got)
	}
	// 舊資料:momdead 只有手動出場這一個來源,一樣對外顯示逾時
	if got := outcomeCN("momdead"); got != "逾時平倉" {
		t.Errorf("outcomeCN(momdead) = %q, want 逾時平倉", got)
	}
	// 真逾時本來就是逾時平倉,兩者對外一致(這正是用戶要的效果)
	if got := outcomeCN("expired"); got != "逾時平倉" {
		t.Errorf("outcomeCN(expired) = %q, want 逾時平倉", got)
	}
}
