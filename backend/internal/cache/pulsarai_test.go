package cache

import "testing"

// The AI reply is free text around a JSON blob; the parser must extract it,
// reject out-of-range scores (a model that ignored the 1–100 rule), and cap reasons.
func TestParsePulsarAI(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantNil bool
		score   int
		reasons int
	}{
		{"clean", `{"score":72,"tag":"順勢","reasons":["a","b"]}`, false, 72, 2},
		{"wrapped", "分析如下:\n```json\n{\"score\":45,\"tag\":\"x\",\"reasons\":[\"a\"]}\n```\n完畢", false, 45, 1},
		{"score too high", `{"score":150,"reasons":["a"]}`, true, 0, 0},
		{"score zero", `{"score":0}`, true, 0, 0},
		{"no json", `抱歉我無法評分`, true, 0, 0},
		{"caps reasons", `{"score":50,"reasons":["a","b","c","d","e","f"]}`, false, 50, 4},
	}
	for _, c := range cases {
		got := parsePulsarAI(c.raw)
		if c.wantNil {
			if got != nil {
				t.Errorf("%s: want nil, got %+v", c.name, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: want score %d, got nil", c.name, c.score)
			continue
		}
		if got.Score != c.score {
			t.Errorf("%s: score = %d, want %d", c.name, got.Score, c.score)
		}
		if len(got.Reasons) != c.reasons {
			t.Errorf("%s: reasons = %d, want %d", c.name, len(got.Reasons), c.reasons)
		}
	}
}
