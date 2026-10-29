package cache

import (
	"testing"
	"time"
)

func TestPulsarHealthFlags(t *testing.T) {
	morning := time.Date(2026, 10, 29, 2, 0, 0, 0, time.UTC)   // 台北 10:00
	afternoon := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC) // 台北 15:00
	if f := pulsarHealthFlags(5, 6, 0.7, morning); len(f) != 0 {
		t.Fatalf("healthy entry flagged: %v", f)
	}
	if f := pulsarHealthFlags(12, 1, 0.5, afternoon); len(f) != 4 || healthLight(len(f)) != "red" {
		t.Fatalf("want 4 flags, got %v", f)
	}
	// 抓不到 24h 漲幅 / 多空比(0)不算紅旗
	if f := pulsarHealthFlags(12, 0, 0, afternoon); len(f) != 2 || healthLight(len(f)) != "yellow" {
		t.Fatalf("missing data should not count: %v", f)
	}
	trs := []*PaperTrade{{CVD1h: 12, Chg24: 1, LongAcct: 0.5, OpenTime: afternoon}, {OpenTime: afternoon}}
	annotateHealth("pulsarv16", trs)
	if trs[0].Health != "red" || len(trs[0].HealthFlags) != 4 || trs[1].Health != "" {
		t.Fatalf("annotate wrong: %+v %+v", trs[0], trs[1])
	}
	annotateHealth("pulsarv3", []*PaperTrade{{CVD1h: 12}}) // 非燈號書不動
	if healthNote("pulsarv16", trs[0]) != " · 🔴 不健康" || healthNote("pulsarv3", trs[0]) != "" {
		t.Fatal("health note wrong")
	}
}
