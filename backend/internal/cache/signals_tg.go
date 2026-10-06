package cache

import (
	"fmt"
	"sort"
	"strings"
)

// signals_tg.go: 把當前「多空推薦」(綜合評分 |分數|≥門檻,與 /api/signals 同口徑)彙整成
// 一則訊息推到 Telegram。取代原本的「策略逐筆開平倉」Telegram 推播。整點呼叫一次。

const (
	sigTGThreshold = 20 // |分數| 門檻,與 handleSignals(多空推薦)一致
	sigTGPerSide   = 25 // 每邊最多列幾檔,避免超過 Telegram 4096 字上限
)

// SignalsTGTick 推一則多空推薦彙整到 Telegram(Telegram 未設定或目前無符合標的時為 no-op)。
func (s *Store) SignalsTGTick() {
	if s.notifier == nil || !s.notifier.Enabled() {
		return
	}
	if txt := s.signalsTGText(); txt != "" {
		s.notifier.Send(txt)
	}
}

type sigTGRow struct {
	coin               string
	score              int
	quality            string
	oi, okx, cvd, funr float64
}

// signalsTGText 組多空推薦訊息;無任何符合標的時回空字串(呼叫端不送)。
func (s *Store) signalsTGText() string {
	data, upd := s.All()
	var longs, shorts []sigTGRow
	for coin, sn := range data {
		if sn.Score > -sigTGThreshold && sn.Score < sigTGThreshold { // 分數不夠醒目 → 跳過
			continue
		}
		r := sigTGRow{coin, sn.Score, sn.Quality, sn.OIChg1h, sn.OKXChg, sn.CVDRatio, sn.Funding}
		if sn.Score > 0 {
			longs = append(longs, r)
		} else {
			shorts = append(shorts, r)
		}
	}
	if len(longs) == 0 && len(shorts) == 0 {
		return ""
	}
	byStrength := func(rs []sigTGRow) {
		sort.Slice(rs, func(i, j int) bool { return absInt(rs[i].score) > absInt(rs[j].score) })
	}
	byStrength(longs)
	byStrength(shorts)

	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>多空推薦</b> · %s\n綜合評分 |分數|≥%d,依強度排序\n", upd.Local().Format("01/02 15:04"), sigTGThreshold)
	writeSide := func(title string, rs []sigTGRow) {
		fmt.Fprintf(&b, "\n%s(%d)\n", title, len(rs))
		if len(rs) == 0 {
			b.WriteString("—\n")
			return
		}
		n := len(rs)
		if n > sigTGPerSide {
			n = sigTGPerSide
		}
		for _, r := range rs[:n] {
			fmt.Fprintf(&b, "<code>%-5s %+3d %s OI%+.1f%% 價%+.1f%% CVD%.2f 費%+.3f%%</code>\n",
				r.coin, r.score, r.quality, r.oi, r.okx, r.cvd, r.funr*100)
		}
		if len(rs) > n {
			fmt.Fprintf(&b, "…還有 %d 檔\n", len(rs)-n)
		}
	}
	writeSide("🟢 <b>做多</b>", longs)
	writeSide("🔴 <b>做空</b>", shorts)
	return b.String()
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
