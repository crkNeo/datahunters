package cache

import (
	"testing"
	"time"

	"datahunter/internal/exchange"
)

// intraSetup 造「盤整 40 根 → 一根放量突破(已收)→ 形成中的第二根」;last 決定形成中那根的樣子。
// 用 v3 本來就會成立的溫和突破,專門測盤中護欄。
func intraSetup(last func(prev exchange.Candle, i int) exchange.Candle) []exchange.Candle {
	cs := quietBars(40, 100)
	cs = append(cs, bar(len(cs), 100, 100.55, 99.95, 100.5, 600))
	return append(cs, last(cs[len(cs)-1], len(cs)))
}

// 漂亮的盤中大陽:突破前高、收在高點附近。
func cleanBar(prev exchange.Candle, i int) exchange.Candle {
	return bar(i, prev.Close, prev.Close+0.55, prev.Close-0.05, prev.Close+0.5, 300)
}

// 衝高後被砸回:上影 0.45 < 實體 0.5、實體佔全距 > 50%(v3 的反插針放行),
// 但離高點回落 0.45 > 全距 0.95 的 30%。
func spikedBar(prev exchange.Candle, i int) exchange.Candle {
	return bar(i, prev.Close, prev.Close+0.95, prev.Close, prev.Close+0.5, 300)
}

func TestSurgeV12V13(t *testing.T) {
	clean := intraSetup(cleanBar)
	if _, _, _, _, ok := surgeV3Signal(clean); !ok {
		t.Fatalf("測試前提:v3 應成立, why=%q", surgeV3Why(clean))
	}
	// 才開始 2 分鐘:v12 進、v13 等滿 5 分鐘
	if _, _, _, _, ok := surgeV12Signal(clean, 120); !ok {
		t.Error("v12 條件成立就該進")
	}
	if _, _, _, _, ok := surgeV13Signal(clean, 120); ok {
		t.Error("v13 未滿 5 分鐘不該進")
	}
	if _, _, _, _, ok := surgeV13Signal(clean, 400); !ok {
		t.Error("v13 滿 5 分鐘、破前高、沒回落 → 應進")
	}

	// 衝高被砸:v12 照買(這就是它的風險),v13 擋
	spiked := intraSetup(spikedBar)
	if _, _, _, _, ok := surgeV12Signal(spiked, 400); !ok {
		t.Errorf("v12 應照 v3 條件進場, v3 why=%q", surgeV3Why(spiked))
	}
	if _, _, _, _, ok := surgeV13Signal(spiked, 400); ok {
		t.Error("v13 離高點回落 > 30% 不該進")
	}

	// 沒突破前一根高點:v13 擋
	noBreak := intraSetup(func(prev exchange.Candle, i int) exchange.Candle {
		return bar(i, prev.Close, prev.Close+0.05, prev.Close-0.02, prev.Close+0.04, 300)
	})
	if noBreak[len(noBreak)-1].Close > noBreak[len(noBreak)-2].High {
		t.Fatal("測試前提:收盤應未破前高")
	}
	if _, _, _, _, ok := surgeV13Signal(noBreak, 400); ok {
		t.Error("v13 沒破前高不該進")
	}
}

func TestMicroIntraOpen(t *testing.T) {
	s := &Store{stratCfg: map[string]StratCfg{}}
	b := &microBook{name: "pulsarv13", tf: "15m", barSec: 900, klimit: 200, minBars: 40, expiry: 16, cooldown: 16, keep: 500, plan: tpPulsarV3, intraSignal: surgeV13Signal}
	cs := intraSetup(cleanBar)
	now := time.UnixMilli(cs[len(cs)-1].Ts + 400_000).UTC()

	if !s.microIntraOpen(b, "AERO", cs, 400, now) {
		t.Fatal("條件成立應開倉")
	}
	tr := b.trades[0]
	if !tr.OpenTime.Equal(now) || tr.Entry != roundPx(cs[len(cs)-1].Close) || tr.Status != "open" {
		t.Errorf("盤中單應記觸發時刻與當下價: open=%v entry=%v", tr.OpenTime, tr.Entry)
	}
	if s.microIntraOpen(b, "AERO", cs, 460, now.Add(time.Minute)) || len(b.trades) != 1 {
		t.Error("已有持倉不該重複開")
	}

	// 剛平倉 → 冷卻中不開
	ct := now.Add(2 * time.Minute)
	tr.Status, tr.CloseTime = "closed", &ct
	if s.microIntraOpen(b, "AERO", cs, 400, now.Add(3*time.Minute)) {
		t.Error("冷卻中不該開")
	}
}
