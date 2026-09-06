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

const maiSystem = "你是專業、有觀點的加密貨幣大盤分析師。只根據我提供的即時數據判讀,敢下判斷、給明確偏向。" +
	"數字一律引用數據內的真實值,不要自行編造沒有的數字;數據沒有的欄位就寫「—」。用繁體中文。\n" +
	"嚴格照以下結構輸出:用 emoji 標題與換行分段,不要用 markdown 的 #、*、表格,不要重複整段貼回數據。\n" +
	"第一行:20 字內的重點摘要當標題(不要標點結尾)。第二行空一行。接著依序:\n" +
	"🔴 市場偏多/偏空/中性 · 信心 高/中/低(自行判斷方向與信心,擇一)\n" +
	"① 重點一\n② 重點二\n③ 重點三\n" +
	"\n📍 關鍵支撐壓力\nBTC 支撐 X / 壓力 Y\nETH 支撐 X / 壓力 Y(直接引用數據中的『關鍵位』數字,務必填上,不要寫「—」)\n" +
	"\n📊 經濟數據\n就數據中的經濟數據用 1–3 點判讀:已公布的比較「實際 vs 預期」推論對風險資產偏多或偏空;即將公布的給倒數與觀望提醒。無數據就寫「近期無高影響數據」。\n" +
	"\n🌐 板塊 / 山寨季\n一句話:領頭與落後板塊、本小時資金轉向、山寨季位置。\n" +
	"\n👀 接下來注意\n1–3 點:關鍵價位觸發的情境(例如跌破 X → 空間擴大)。\n" +
	"\n📝 總結:一句話 方向 + 關鍵價 + 該做什麼\n" +
	"⚠️ 僅提供資訊,不構成投資建議"

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
	if len(risk.Events) > 0 { // 高影響美國經濟數據(非農/小非農/PPI/CPI…)實際 vs 預期
		b.WriteString("經濟數據(高影響):")
		n := 0
		for _, e := range risk.Events {
			if n >= 4 {
				break
			}
			if n > 0 {
				b.WriteString("; ")
			}
			if e.Released {
				fmt.Fprintf(&b, "%s 已公布 實際%s/預期%s/前值%s", e.Title, nz(e.Actual), nz(e.Forecast), nz(e.Previous))
			} else {
				fmt.Fprintf(&b, "%s %s公布 預期%s/前值%s", e.Title, humanCountdown(e.Time), nz(e.Forecast), nz(e.Previous))
			}
			n++
		}
		b.WriteString("\n")
	}

	liq := s.Liquidations()
	fmt.Fprintf(&b, "近1h清算:多單爆 $%.1fM、空單爆 $%.1fM\n", liq.LongUSD1h/1e6, liq.ShortUSD1h/1e6)

	if fb := s.FundingBoard(); len(fb.Rows) > 0 {
		hi := fb.Rows[0]              // most positive (rows sorted desc)
		lo := fb.Rows[len(fb.Rows)-1] // most negative
		fmt.Fprintf(&b, "資金費率極端:%s %+.3f%%(多方擁擠)/ %s %+.3f%%(空方擁擠)\n", hi.Coin, hi.Rate*100, lo.Coin, lo.Rate*100)
	}

	{ // BTC/ETH 關鍵位:一定有值(確認 SR 優先,否則近 24h 區間),給 📍 用
		bs, br := s.keyLevels("BTC")
		es, er := s.keyLevels("ETH")
		fmt.Fprintf(&b, "關鍵位:BTC 支撐$%s 壓力$%s;ETH 支撐$%s 壓力$%s\n", fmtPx(bs), fmtPx(br), fmtPx(es), fmtPx(er))
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

	if sb := s.SectorBoard(); len(sb.Rows) >= 2 {
		r := sb.Rows
		fmt.Fprintf(&b, "板塊強弱(24h,相對BTC):領頭 %s %+.1f%%、%s %+.1f%%;落後 %s %+.1f%%\n",
			r[0].Sector, r[0].VsBTC, r[1].Sector, r[1].VsBTC, r[len(r)-1].Sector, r[len(r)-1].VsBTC)
		hot := SectorRow{}
		for _, s := range r {
			if s.Delta > hot.Delta {
				hot = s
			}
		}
		if hot.Delta >= 0.8 {
			fmt.Fprintf(&b, "本小時資金轉向:%s 板塊轉強(較上小時 +%.1fpp)\n", hot.Sector, hot.Delta)
		}
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

// keyLevels 回傳某幣「一定有值」的關鍵支撐/壓力:優先用已確認的 SR 群聚 level,沒有就退回
// 近 24 根 1h 的區間低/高。給 AI 的 📍 用,避免顯示「—」。
func (s *Store) keyLevels(coin string) (sup, res float64) {
	s.srMu.Lock()
	info := s.srInfo[coin]
	s.srMu.Unlock()
	sup, res = info.Support, info.Resistance
	if sup > 0 && res > 0 {
		return
	}
	cs := s.closed1h(coin, 24) // 退回:近 24 根 1h 的區間高低
	if len(cs) == 0 {
		return
	}
	lo, hi := cs[0].Low, cs[0].High
	for _, c := range cs {
		if c.Low < lo {
			lo = c.Low
		}
		if c.High > hi {
			hi = c.High
		}
	}
	if sup == 0 {
		sup = lo
	}
	if res == 0 {
		res = hi
	}
	return
}

// humanCountdown 把經濟事件的倒數轉成好讀字串(>48h 顯示「約N天後」,否則 XhYm後)。
func humanCountdown(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "即將"
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("約%d天後", int(d.Hours())/24)
	}
	return fmt.Sprintf("%dh%02dm後", int(d.Hours()), int(d.Minutes())%60)
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
