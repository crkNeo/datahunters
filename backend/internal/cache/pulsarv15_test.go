package cache

import "testing"

func TestV15OK(t *testing.T) {
	cases := []struct {
		cvd, ls float64
		want    bool
		why     string
	}{
		{5, 0.65, true, "CVD 溫和、散戶偏多 → 進"},
		{10, 0.60, true, "剛好在門檻上 → 進"},
		{12, 0.65, false, "1h 買盤太猛(追最後一棒)→ 不進"},
		{5, 0.55, false, "散戶做多 < 60% → 不進"},
		{5, 0, true, "抓不到多空比 → 不擋"},
		{-8, 0.7, true, "1h 賣方主導也可以進"},
	}
	for _, c := range cases {
		if got := v15OK(c.cvd, c.ls); got != c.want {
			t.Errorf("%s: v15OK(%v,%v)=%v", c.why, c.cvd, c.ls, got)
		}
	}
	s := &Store{stratCfg: map[string]StratCfg{}}
	b := s.newPulsarV15Book()
	if b.intraSignal == nil || b.gate == nil || !b.tagEntry || b.name != "pulsarv15" {
		t.Error("v15 應是盤中書 + 有濾網 + 記錄因子")
	}
	if !observeOnlyBooks["pulsarv15"] {
		t.Error("v15 必須是只觀察(不被 BITUNIX_BOOKS=all 帶進實盤)")
	}
}
