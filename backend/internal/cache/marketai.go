package cache

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// marketai.go: the public 大盤分析 tab. Once per hour it assembles a snapshot of
// the market signals the app already tracks and asks a free (keyless) AI for a
// short zh-TW commentary, then displays + pushes it.

// MarketAIData is the 大盤分析 payload.
type MarketAIData struct {
	Text      string `json:"text"`    // full zh-TW analysis
	Summary   string `json:"summary"` // one-line headline
	UpdatedAt string `json:"updated_at"`
}

const maiSystem = "你是專業、有觀點的加密貨幣大盤分析師。根據提供的即時數據,用繁體中文分析目前大盤動態," +
	"並明確給出你自己的偏向與看法(偏多/偏空/中性,以及你的理由、信心程度、該留意的關卡)。敢下判斷,不要只是中立描述數據。" +
	"格式:第一行是一句 20 字內的重點摘要(當標題,不要標點結尾);接著 3-5 句分析與你的判斷;最後獨立一行『⚠️ 僅提供資訊,不構成投資建議』。" +
	"不要用 markdown、不要條列符號、不要重複貼回數據。"

// MarketAITick generates the hourly market commentary. Self-gated to once per hour
// bucket; the first run seeds (shows, no push).
func (s *Store) MarketAITick() {
	if s.maiW == nil {
		return
	}
	now := time.Now()
	h := now.UTC().Unix() / 3600
	if h == s.maiBucket { // already succeeded this hour
		return
	}
	if now.Before(s.maiRetryAt) { // backing off after a recent failure
		return
	}

	snap := s.marketSnapshot()
	label := "大盤AI分析(" + s.maiW.Provider() + ")"
	text, err := s.maiW.Analyze(maiSystem, "目前大盤數據:\n"+snap+"\n\n請分析目前大盤動態。")
	if err != nil {
		s.maiRetryAt = now.Add(5 * time.Minute) // don't consume the hour; retry in 5 min
		log.Printf("market-AI: analysis FAILED via %s: %v (retry in 5m)", s.maiW.Provider(), err)
		s.apiFail(label, err.Error())
		return
	}
	s.apiOK(label)
	s.maiBucket = h // success → done for this hour
	seeded := s.maiSeeded
	s.maiSeeded = true
	log.Printf("market-AI: analysis updated via %s (%d chars)", s.maiW.Provider(), len(text))
	summary := text
	if i := strings.IndexByte(text, '\n'); i > 0 {
		summary = strings.TrimSpace(text[:i])
	}
	s.maiMu.Lock()
	s.maiText, s.maiSummary, s.maiTime = text, summary, now
	s.maiMu.Unlock()

	if seeded { // Web Push the headline every hour (skip the boot/seed run)
		body := summary
		if r := []rune(body); len(r) > 90 {
			body = string(r[:90]) + "…"
		}
		s.PushSend("🔔整點「大盤分析」", body, "/")
	}
}

// MarketAIProvider names the active AI backend (Gemini if a key is set, else
// Pollinations) — logged at startup so a missing key is obvious.
func (s *Store) MarketAIProvider() string {
	if s.maiW == nil {
		return "off"
	}
	return s.maiW.Provider()
}

// MarketAI returns the latest commentary.
func (s *Store) MarketAI() MarketAIData {
	s.maiMu.RLock()
	defer s.maiMu.RUnlock()
	out := MarketAIData{Text: s.maiText, Summary: s.maiSummary}
	if !s.maiTime.IsZero() {
		out.UpdatedAt = s.maiTime.Format(time.RFC3339)
	}
	return out
}

// marketSnapshot builds a compact zh-TW snapshot from the signals the app already
// tracks, for the AI prompt.
func (s *Store) marketSnapshot() string {
	var b strings.Builder
	home, _ := s.Home()
	if t, ok := home.Ticker["BTC"]; ok {
		fmt.Fprintf(&b, "BTC $%.0f(24h %+.2f%%)\n", t.Price, t.Chg)
	}
	if t, ok := home.Ticker["ETH"]; ok {
		fmt.Fprintf(&b, "ETH $%.0f(24h %+.2f%%)\n", t.Price, t.Chg)
	}
	fmt.Fprintf(&b, "山寨季指數 %d/100(%s)\n", home.AltSeason.Value, home.AltSeason.Label)

	px := s.livePrices()
	biasCN := func(c string) string {
		mb := s.marketBias(c, px)
		if !mb.OK {
			return "評估中"
		}
		switch mb.Bias {
		case "long":
			return "多頭"
		case "short":
			return "空頭"
		default:
			return "中性"
		}
	}
	fmt.Fprintf(&b, "1h 趨勢:BTC %s、ETH %s\n", biasCN("BTC"), biasCN("ETH"))

	risk := s.Risk()
	fmt.Fprintf(&b, "美股/宏觀:%s;被帶崩風險 %s\n", riskCN(risk.Risk), orDash(risk.Push.Level))
	if len(risk.RiskReasons) > 0 {
		fmt.Fprintf(&b, "風險因素:%s\n", strings.Join(risk.RiskReasons, "、"))
	}

	liq := s.Liquidations()
	fmt.Fprintf(&b, "近1h清算:多單爆 $%.1fM、空單爆 $%.1fM\n", liq.LongUSD1h/1e6, liq.ShortUSD1h/1e6)

	if fb := s.FundingBoard(); len(fb.Rows) > 0 {
		hi := fb.Rows[0]              // most positive (rows sorted desc)
		lo := fb.Rows[len(fb.Rows)-1] // most negative
		fmt.Fprintf(&b, "資金費率極端:%s %+.3f%%(多方擁擠)/ %s %+.3f%%(空方擁擠)\n", hi.Coin, hi.Rate*100, lo.Coin, lo.Rate*100)
	}

	if sr := s.SR(); len(sr.Levels) > 0 { // 主流幣 1h 支撐壓力位 + 剛破/剛突破
		b.WriteString("支撐壓力(1h):")
		for i, l := range sr.Levels {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(l.Coin)
			if l.SupOK {
				fmt.Fprintf(&b, " 支撐$%s", fmtPx(l.Support))
			}
			if l.ResOK {
				fmt.Fprintf(&b, " 壓力$%s", fmtPx(l.Resistance))
			}
			switch l.Status {
			case "break_down":
				b.WriteString("(剛跌破支撐)")
			case "break_up":
				b.WriteString("(剛突破壓力)")
			}
		}
		b.WriteString("\n")
	}

	if news := s.News(); len(news) > 0 {
		b.WriteString("近期快訊:")
		for i, n := range news {
			if i >= 4 {
				break
			}
			if i > 0 {
				b.WriteString(" / ")
			}
			b.WriteString(n.Title)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func riskCN(r string) string {
	switch r {
	case "risk-on":
		return "偏多(risk-on)"
	case "risk-off":
		return "偏空(risk-off)"
	default:
		return "中性"
	}
}
