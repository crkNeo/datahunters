package cache

import (
	"strings"
	"testing"
)

func TestSignalsTGText(t *testing.T) {
	s := &Store{data: map[string]Snapshot{
		"BTC": {Score: 85, Bias: "long", Quality: "高", OIChg1h: 2.3, OKXChg: 1.5, CVDRatio: 1.8, Funding: 0.0001},
		"ETH": {Score: -72, Bias: "short", Quality: "中", OIChg1h: -1.1, OKXChg: -2.0, CVDRatio: 0.6, Funding: -0.0002},
		"SOL": {Score: 10}, // |分數|<20 → 應被排除
	}}
	txt := s.signalsTGText()
	for _, want := range []string{"多空推薦", "做多", "做空", "BTC", "ETH", "+85", "-72"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text missing %q\n--- got ---\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "SOL") {
		t.Error("SOL(分數10)應低於門檻被排除")
	}
	// 無任何符合標的 → 空字串(呼叫端不送)
	if (&Store{data: map[string]Snapshot{"X": {Score: 5}}}).signalsTGText() != "" {
		t.Error("無符合標的時應回空字串")
	}
}
