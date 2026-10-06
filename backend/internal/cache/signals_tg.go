package cache

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// signals_tg.go: 多空推薦 → Telegram。不等整點,改成「當前快照出現異常 OI 或 CVD 就即時通知」。
// 每分鐘掃一次最新快照,挑出 |1h OI 變化| 或 |CVD%| 超過門檻的幣,整批推一則;同一幣在冷卻
// 時間內不重複通知(避免同一波異常洗版)。

const (
	oiAbnPct         = 10.0           // |1h OI 變化%| ≥ 視為異常(OI 一小時跳動 ≥10% 很醒目)
	cvdAbnPct        = 25.0           // |CVD%| ≥ 視為異常(單邊吃單 ≥25% 全窗量)
	sigAlertCooldown = 2 * time.Hour  // 同一幣再次通知的最短間隔
	sigAlertMax      = 20             // 單則訊息最多列幾檔,守 Telegram 4096 字
)

type anomalyHit struct {
	coin   string
	sn     Snapshot
	oiAbn  bool
	cvdAbn bool
}

// anomalyHits 掃描快照,回傳本輪「新異常(OI 或 CVD 超標)且通過冷卻」的幣,並更新冷卻時刻。
func (s *Store) anomalyHits(data map[string]Snapshot, now time.Time) []anomalyHit {
	var hits []anomalyHit
	s.sigAlertMu.Lock()
	defer s.sigAlertMu.Unlock()
	for coin, sn := range data {
		oiAbn := math.Abs(sn.OIChg1h) >= oiAbnPct
		cvdAbn := math.Abs(sn.CVDRatio) >= cvdAbnPct
		if !oiAbn && !cvdAbn {
			continue
		}
		if last, ok := s.sigAlertLast[coin]; ok && now.Sub(last) < sigAlertCooldown {
			continue // 冷卻中,不重複
		}
		s.sigAlertLast[coin] = now
		hits = append(hits, anomalyHit{coin, sn, oiAbn, cvdAbn})
	}
	return hits
}

// SignalAlertTick 掃當前快照,對 OI 或 CVD 異常的幣即時推 Telegram(同幣冷卻內不重複)。
// Telegram 未設定、或本輪沒有新異常時為 no-op。每分鐘呼叫一次。
func (s *Store) SignalAlertTick() {
	if s.notifier == nil || !s.notifier.Enabled() {
		return
	}
	hits := s.anomalyHits(s.allData(), time.Now())
	if len(hits) == 0 {
		return
	}
	s.notifier.Send(anomalyText(hits, time.Now()))
}

// allData 取當前快照(去掉 updated 時刻,給內部掃描用)。
func (s *Store) allData() map[string]Snapshot {
	d, _ := s.All()
	return d
}

// anomalyText 把本輪異常的幣組成 Telegram 訊息(依分數強度排序,上限 sigAlertMax 檔)。
func anomalyText(hits []anomalyHit, now time.Time) string {
	sort.Slice(hits, func(i, j int) bool { return absInt(hits[i].sn.Score) > absInt(hits[j].sn.Score) })
	var b strings.Builder
	fmt.Fprintf(&b, "⚡ <b>異常訊號</b> · %s\nOI 或 CVD 異常(OI|Δ1h|≥%.0f%% 或 |CVD|≥%.0f%%)\n\n",
		now.Local().Format("01/02 15:04"), oiAbnPct, cvdAbnPct)
	n := len(hits)
	if n > sigAlertMax {
		n = sigAlertMax
	}
	for _, h := range hits[:n] {
		oi := fmt.Sprintf("OI%+.1f%%", h.sn.OIChg1h)
		if h.oiAbn {
			oi += "⚠"
		}
		cvd := fmt.Sprintf("CVD%+.0f%%", h.sn.CVDRatio)
		if h.cvdAbn {
			cvd += "⚠"
		}
		fmt.Fprintf(&b, "<code>%-5s %s %+3d %s %s 價%+.1f%% 費%+.3f%%</code>\n",
			h.coin, biasCN(h.sn.Bias), h.sn.Score, oi, cvd, h.sn.OKXChg, h.sn.Funding*100)
	}
	if len(hits) > n {
		fmt.Fprintf(&b, "…還有 %d 檔\n", len(hits)-n)
	}
	return b.String()
}

// biasCN 把 long/short/neutral 轉成多空標籤。
func biasCN(bias string) string {
	switch bias {
	case "long":
		return "做多"
	case "short":
		return "做空"
	default:
		return "中性"
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
