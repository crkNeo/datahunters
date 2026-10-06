package cache

import (
	"testing"
	"time"
)

func TestAnomalyHits(t *testing.T) {
	s := &Store{sigAlertLast: map[string]time.Time{}}
	data := map[string]Snapshot{
		"BTC": {Score: 40, OIChg1h: 18.0, CVDRatio: 5},   // OI 異常
		"ETH": {Score: -30, OIChg1h: 2.0, CVDRatio: -40}, // CVD 異常
		"SOL": {Score: 80, OIChg1h: 3.0, CVDRatio: 10},   // 都正常 → 不觸發
		"BNB": {Score: 10, OIChg1h: 12.0, CVDRatio: -50}, // 兩者皆異常
	}
	now := time.Now()
	hits := s.anomalyHits(data, now)

	got := map[string]anomalyHit{}
	for _, h := range hits {
		got[h.coin] = h
	}
	if _, ok := got["SOL"]; ok {
		t.Error("SOL 正常,不該觸發")
	}
	if len(got) != 3 {
		t.Fatalf("應觸發 3 檔(BTC/ETH/BNB),實際 %d", len(got))
	}
	if !got["BTC"].oiAbn || got["BTC"].cvdAbn {
		t.Error("BTC 應只有 OI 異常")
	}
	if got["ETH"].oiAbn || !got["ETH"].cvdAbn {
		t.Error("ETH 應只有 CVD 異常")
	}
	if !got["BNB"].oiAbn || !got["BNB"].cvdAbn {
		t.Error("BNB 應兩者皆異常")
	}

	// 冷卻:同一輪(now)再掃一次,應全部被冷卻擋下
	if again := s.anomalyHits(data, now); len(again) != 0 {
		t.Errorf("冷卻內不該重複觸發,實際 %d", len(again))
	}
	// 超過冷卻後應可再觸發
	if later := s.anomalyHits(data, now.Add(sigAlertCooldown+time.Minute)); len(later) != 3 {
		t.Errorf("超過冷卻應再觸發 3 檔,實際 %d", len(later))
	}
}
