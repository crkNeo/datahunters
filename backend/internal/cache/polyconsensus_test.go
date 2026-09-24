package cache

import (
	"testing"

	"datahunter/internal/polymarket"
)

func TestParsePolyAI(t *testing.T) {
	reps := []polymarket.Report{
		{Wallet: "0xaaa1", Name: "alice", Verdict: "長期穩定", AllPnL: 1000, MonthPnL: 300,
			Positions: []polymarket.Position{{Title: "BTC 90k", Outcome: "No", CurValue: 500}}},
		{Wallet: "0xbbb2", Name: "bob", Verdict: "短期爆發", AllPnL: 50, MonthPnL: 900},
	}
	// AI 回覆刻意包 ```json 圍欄 + 前後贅字,測 extractJSON 是否挖得出來;views 故意少一個。
	raw := "這是分析結果:\n```json\n" +
		`{"overall":{"lean":"區間","confidence":"中","summary":"多數看區間盤整。"},` +
		`"views":[{"i":1,"lean":"區間","view":"賭 BTC 不破 90k"}]}` +
		"\n```\n以上。"

	got, err := parsePolyAI(raw, reps)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if got.Lean != "區間" || got.Confidence != "中" || got.Summary == "" {
		t.Errorf("overall 解析錯: %+v", got)
	}
	if len(got.Views) != 2 {
		t.Fatalf("views 應對回 2 人,得 %d", len(got.Views))
	}
	// 第 1 人:AI 有給看法,且倉位/名字要保留
	if got.Views[0].View != "賭 BTC 不破 90k" || got.Views[0].Lean != "區間" {
		t.Errorf("view#1 AI 欄位沒對上: %+v", got.Views[0])
	}
	if got.Views[0].Name != "alice" || len(got.Views[0].Positions) != 1 {
		t.Errorf("view#1 未保留 reps 資料: %+v", got.Views[0])
	}
	// 第 2 人:AI 漏給 → 看法留白,但其餘資料仍在
	if got.Views[1].View != "" || got.Views[1].Name != "bob" {
		t.Errorf("view#2 漏給應留白但保資料: %+v", got.Views[1])
	}
}

func TestParsePolyAIBadJSON(t *testing.T) {
	if _, err := parsePolyAI("完全不是 JSON", nil); err == nil {
		t.Error("非 JSON 應回錯")
	}
}
