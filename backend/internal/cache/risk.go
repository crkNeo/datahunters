package cache

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RiskItem is one tracked macro/equity instrument.
type RiskItem struct {
	Name   string  `json:"name"`
	ChgPct float64 `json:"chg_pct"`
	Price  float64 `json:"price"`
}

// MacroEvent is one upcoming/recent high-impact US economic-calendar event.
type MacroEvent struct {
	Title     string    `json:"title"`
	Impact    string    `json:"impact"`
	Time      time.Time `json:"time"`
	Forecast  string    `json:"forecast"`
	Previous  string    `json:"previous"`
	Actual    string    `json:"actual"`
	Countdown string    `json:"countdown"` // "3h12m" if upcoming, "" if released
	Released  bool      `json:"released"`
}

// PushRisk is the two-sided "external push" warning: crypto being dragged
// down (帶崩) or up (帶噴) by the US/macro backdrop.
type PushRisk struct {
	Dir     string   `json:"dir"`   // down | up | flat
	Level   string   `json:"level"` // 低 | 中 | 高
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
	Action  string   `json:"action"`
}

// RiskData is the US/macro risk-backdrop strip payload.
type RiskData struct {
	Items       []RiskItem   `json:"items"`
	Risk        string       `json:"risk"` // risk-on | neutral | risk-off
	RiskReasons []string     `json:"risk_reasons"`
	USStatus    string       `json:"us_status"` // 開盤中 | 盤前 | 已收盤 | 週末休市
	Countdown   string       `json:"countdown"`
	HighImpact  bool         `json:"high_impact"` // in US active hours (08:00–16:00 ET)
	Events      []MacroEvent `json:"events"`      // upcoming/just-released high-impact US events
	Push        PushRisk     `json:"push"`        // two-sided 帶崩/帶噴 warning
	UpdatedAt   string       `json:"updated_at"`
	Note        string       `json:"note"`
}

// Risk returns the cached US/macro backdrop, refreshing if older than 60s.
func (s *Store) Risk() RiskData {
	s.riskMu.Lock()
	defer s.riskMu.Unlock()
	if time.Since(s.riskTime) < 60*time.Second && len(s.riskData.Items) > 0 {
		return s.riskData
	}
	now := time.Now()
	d := RiskData{
		Items:       []RiskItem{},
		Risk:        "neutral",
		RiskReasons: []string{},
		UpdatedAt:   now.Format(time.RFC3339),
		Note:        "美股/風險背景燈(Yahoo 即時)· 風險時段提醒,非回測訊號",
	}

	syms := []struct{ name, sym string }{
		{"S&P期貨", "ES=F"}, {"Nasdaq期貨", "NQ=F"}, {"VIX", "^VIX"}, {"美元DXY", "DX-Y.NYB"},
	}
	chg := map[string]float64{}
	px := map[string]float64{}
	for _, x := range syms {
		q, err := s.ex.YahooQuote(x.sym)
		if err != nil {
			continue
		}
		chg[x.name] = q.ChgPct()
		px[x.name] = q.Price
		d.Items = append(d.Items, RiskItem{Name: x.name, ChgPct: round2(q.ChgPct()), Price: round2(q.Price)})
	}

	// composite risk score: equities up = risk-on; VIX/DXY up = risk-off
	es, nq, vix, dxy := chg["S&P期貨"], chg["Nasdaq期貨"], chg["VIX"], chg["美元DXY"]
	score := clamp(es/0.5, -2, 2) + clamp(nq/0.7, -1, 1) - clamp(vix/5, -2, 2) - clamp(dxy/0.4, -1, 1)
	if es <= -0.3 || nq <= -0.5 {
		d.RiskReasons = append(d.RiskReasons, fmt.Sprintf("美股期貨走弱(ES %.2f%% / NQ %.2f%%)", es, nq))
	} else if es >= 0.3 {
		d.RiskReasons = append(d.RiskReasons, fmt.Sprintf("美股期貨走強(ES %+.2f%%)", es))
	}
	if vix >= 5 {
		d.RiskReasons = append(d.RiskReasons, fmt.Sprintf("VIX 升溫 %+.1f%%(避險)", vix))
	} else if vix <= -5 {
		d.RiskReasons = append(d.RiskReasons, fmt.Sprintf("VIX 降溫 %.1f%%(風險偏好)", vix))
	}
	if dxy >= 0.3 {
		d.RiskReasons = append(d.RiskReasons, fmt.Sprintf("美元走強 %+.2f%%(壓加密)", dxy))
	}
	switch {
	case score >= 1.5:
		d.Risk = "risk-on"
	case score <= -1.5:
		d.Risk = "risk-off"
	}

	d.USStatus, d.Countdown, d.HighImpact = usSession(now)
	d.Events = s.macroEvents(now)

	// crypto-side confirmation: is BTC already rolling over?
	btc1h := 0.0
	if ks, err := s.Klines("BTC", "1h", 4); err == nil && len(ks) >= 2 {
		n := len(ks)
		if ks[n-2].C != 0 {
			btc1h = (ks[n-1].C - ks[n-2].C) / ks[n-2].C * 100
		}
	}
	d.Push = computePush(now, es, nq, vix, dxy, px["VIX"], btc1h, d.Events, d.HighImpact)
	s.maybeNotifyPush(d.Push)
	s.maybeNotifyEvents(now, d.Events)

	if len(d.Items) > 0 {
		s.riskData = d
		s.riskTime = now
	}
	return d
}

// maybeNotifyPush sends a Telegram alert when the push warning reaches 高,
// de-duped so it fires once per transition (not every tick). Caller holds riskMu.
func (s *Store) maybeNotifyPush(p PushRisk) {
	key := p.Dir + "|" + p.Level
	if key == s.lastPushKey {
		return
	}
	s.lastPushKey = key
	if p.Level == "低" || !s.notifier.Enabled() {
		return
	}
	msg := fmt.Sprintf("<b>⚠️ 被帶崩風險:%s</b>\n%s\n\n%s", p.Level, strings.Join(p.Reasons, "\n"), p.Action)
	go s.notifier.Send(msg)
}

// maybeNotifyEvents pushes a one-time alert ~30 min before a high-impact event.
// Caller holds riskMu.
func (s *Store) maybeNotifyEvents(now time.Time, events []MacroEvent) {
	if !s.notifier.Enabled() {
		return
	}
	for _, e := range events {
		if e.Released {
			continue
		}
		dt := e.Time.Sub(now)
		if dt <= 0 || dt > 30*time.Minute {
			continue
		}
		key := e.Title + "|" + e.Time.Format(time.RFC3339)
		if s.sentEvents[key] {
			continue
		}
		s.sentEvents[key] = true
		go s.notifier.Send(fmt.Sprintf("📅 <b>%s</b> 約 %s 後公布\n預期 %s · 前值 %s",
			e.Title, fmtDur(dt), nz(e.Forecast), nz(e.Previous)))
	}
}

func nz(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// computePush blends the leading macro signals into a two-sided warning: crypto
// being dragged DOWN (帶崩) or UP (帶噴). Heuristic risk gauge — NOT a backtested
// signal; it flags when conditions for a US-driven move are stacking up so you
// can de-risk (down) or lean in carefully (up). Ties favour the downside.
func computePush(now time.Time, es, nq, vix, dxy, vixLvl, btc1h float64, events []MacroEvent, nyWindow bool) PushRisk {
	down := 0
	var dr []string

	// ---- downside (risk-off): the only side we warn on ("被帶崩") ----
	if es <= -0.5 || nq <= -0.7 {
		down += 2
		dr = append(dr, fmt.Sprintf("美股期貨急跌(ES %.2f%% / NQ %.2f%%)", es, nq))
	} else if es <= -0.2 {
		down++
		dr = append(dr, fmt.Sprintf("美股期貨走弱(ES %.2f%%)", es))
	}
	if vix >= 5 {
		down += 2
		dr = append(dr, fmt.Sprintf("VIX 急升 %+.1f%%(恐慌)", vix))
	} else if vix >= 2 {
		down++
	}
	if vixLvl >= 22 {
		down++
		dr = append(dr, fmt.Sprintf("VIX 高檔 %.1f", vixLvl))
	}
	if dxy >= 0.3 {
		down++
		dr = append(dr, fmt.Sprintf("美元走強 %+.2f%%", dxy))
	}
	if nyWindow {
		down++
		dr = append(dr, "紐約高影響時段")
	}
	if btc1h <= -0.5 {
		down++
		dr = append(dr, fmt.Sprintf("BTC 已轉弱(1h %.2f%%)", btc1h))
	}
	// imminent high-impact event = downside caution (volatility risk)
	for _, e := range events {
		if e.Released {
			continue
		}
		dt := e.Time.Sub(now)
		if dt > 0 && dt <= time.Hour {
			down += 2
			dr = append(dr, fmt.Sprintf("%s 即將公布(%s)", e.Title, fmtDur(dt)))
			break
		} else if dt > 0 && dt <= 3*time.Hour {
			down++
			dr = append(dr, fmt.Sprintf("%s 在 %s 後", e.Title, fmtDur(dt)))
			break
		}
	}

	p := PushRisk{Dir: "flat", Level: "低", Reasons: []string{}, Action: "背景平穩,照常依訊號操作。"}
	if down < 3 {
		return p
	}
	p.Dir, p.Score, p.Reasons = "down", down, dr
	if p.Score >= 5 {
		p.Level = "高"
		p.Action = "高風險被帶崩:避免新多單、縮小部位、收緊止損;有多單考慮減碼/對沖。"
	} else {
		p.Level = "中"
		p.Action = "留意被帶崩:新多單保守、別追高,緊盯 ES 期貨與即將公布的數據。"
	}
	return p
}

// ensureCalendarLocked refreshes the cached high-impact US calendar. The feed
// rate-limits aggressive polling, so refetch every 30 min (retry every 5 min
// until the first success) and keep last-good data on failure. Caller holds riskMu.
func (s *Store) ensureCalendarLocked(now time.Time) {
	needFetch := time.Since(s.calTime) > 30*time.Minute ||
		(len(s.calRaw) == 0 && time.Since(s.calTime) > 5*time.Minute)
	if !needFetch {
		return
	}
	var raw []MacroEvent
	for _, which := range []string{"thisweek", "nextweek"} {
		evs, err := s.ex.ForexFactoryWeek(which)
		if err != nil {
			continue
		}
		for _, e := range evs {
			if e.Country != "USD" || e.Impact != "High" {
				continue
			}
			t, err := time.Parse(time.RFC3339, e.Date)
			if err != nil {
				continue
			}
			raw = append(raw, MacroEvent{
				Title: e.Title, Impact: e.Impact, Time: t,
				Forecast: e.Forecast, Previous: e.Previous, Actual: e.Actual,
			})
		}
	}
	s.calTime = now
	if len(raw) > 0 { // keep last-good if a refresh got rate-limited
		sort.Slice(raw, func(i, j int) bool { return raw[i].Time.Before(raw[j].Time) })
		s.calRaw = raw
	}
}

// withCountdown returns cached events newer than now-pastH, countdown filled in.
// limit<=0 means no limit. Caller holds riskMu.
func (s *Store) withCountdown(now time.Time, pastH float64, limit int) []MacroEvent {
	out := []MacroEvent{}
	for _, e := range s.calRaw {
		dt := e.Time.Sub(now)
		if dt < -time.Duration(pastH*float64(time.Hour)) {
			continue
		}
		ev := e
		if dt > 0 {
			ev.Countdown = fmtDur(dt)
		} else {
			ev.Released = true
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// macroEvents (risk strip): next few high-impact events within ±2h.
func (s *Store) macroEvents(now time.Time) []MacroEvent {
	s.ensureCalendarLocked(now)
	return s.withCountdown(now, 2, 6)
}

// Events (dedicated tab): full high-impact US calendar incl. last 24h released.
func (s *Store) Events() []MacroEvent {
	s.riskMu.Lock()
	defer s.riskMu.Unlock()
	now := time.Now()
	s.ensureCalendarLocked(now)
	return s.withCountdown(now, 24, 0)
}

// usSession reports the US equity session status, a countdown, and whether we
// are inside the high-impact window (08:00–16:00 ET, when US macro/equities move
// crypto most). Uses America/New_York so DST is handled automatically.
func usSession(now time.Time) (status, countdown string, highImpact bool) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return "未知", "", false
	}
	et := now.In(loc)
	if wd := et.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return "週末休市", "", false
	}
	y, m, day := et.Date()
	openT := time.Date(y, m, day, 9, 30, 0, 0, loc)
	closeT := time.Date(y, m, day, 16, 0, 0, 0, loc)
	preData := time.Date(y, m, day, 8, 0, 0, 0, loc)
	hi := !et.Before(preData) && et.Before(closeT)
	switch {
	case et.Before(openT):
		return "盤前", "距開盤 " + fmtDur(openT.Sub(et)), hi
	case et.Before(closeT):
		return "開盤中", "距收盤 " + fmtDur(closeT.Sub(et)), true
	default:
		return "已收盤", "", false
	}
}

func fmtDur(d time.Duration) string {
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
