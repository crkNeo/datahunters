package cache

import (
	"fmt"
	"sort"
	"time"
)

// PaperTrade is one simulated trade taken from a breakout-radar signal. Entry is
// the market price when the signal fired; TP/SL are the radar's fib levels.
type PaperTrade struct {
	ID        string     `json:"-"` // book|coin|dir|opentime, for persistence
	Coin      string     `json:"coin"`
	Dir       string     `json:"dir"` // long | short
	Score     int        `json:"score"`
	Entry     float64    `json:"entry"` // fill price (market at signal)
	TP        float64    `json:"tp"`
	SL        float64    `json:"sl"`
	Cur       float64    `json:"cur"`     // latest price
	PnLPct    float64    `json:"pnl_pct"` // live (open) or final (closed)
	Status    string     `json:"status"`  // open | closed
	Outcome   string     `json:"outcome"` // "" | tp | sl | expired | reversed | trail
	OpenTime  time.Time  `json:"open_time"`
	CloseTime *time.Time `json:"close_time,omitempty"`
	R         float64    `json:"r,omitempty"`    // swing range (for trailing books)
	Peak      float64    `json:"peak,omitempty"` // best price since entry (trailing books)
	OI        float64    `json:"oi"`             // OI % change at entry (radar)
	CVD       float64    `json:"cvd"`            // taker-buy CVD % at entry (radar)
	Funding    float64   `json:"funding"`            // funding rate at entry (persisted)
	CurFunding float64   `json:"cur_funding"`        // live funding rate (transient, set at serve)
	Momentum   string    `json:"momentum,omitempty"` // live momentum light: alive|weak|dead (transient)
}

// PaperStats summarises closed trades.
type PaperStats struct {
	Closed   int     `json:"closed"`
	Wins     int     `json:"wins"`
	Losses   int     `json:"losses"`
	WinRate  float64 `json:"win_rate"`
	AvgPnl   float64 `json:"avg_pnl"`
	TotalPnl float64 `json:"total_pnl"`
}

// PaperState is one tracker tab's payload.
type PaperState struct {
	Open   []*PaperTrade `json:"open"`
	Closed []*PaperTrade `json:"closed"`
	Stats  PaperStats    `json:"stats"`
}

const (
	paperMaxOpen   = 15
	paperExpiry    = 24 * time.Hour
	paperKeepClose = 500 // in-memory cap (full history still in SQLite)
)

// paperBook is one simulated-trading account with its own entry rules. The main
// book is disciplined (high bar, fresh-cross only); the gamble book is loose
// (low bar, chases already-elevated coins) — a live A/B of discipline vs FOMO.
type paperBook struct {
	name         string // "main" | "gamble", persistence key prefix
	minScore     int
	requireCross bool          // true: only enter on a fresh cross up; false: chase anything
	cooldown     time.Duration
	trail        float64       // >0: trailing-stop exit (trail×R behind peak); 0: fixed TP/SL
	skipNY       bool          // skip new entries during the NY session (12-18 UTC)
	requireAlign bool          // only enter when OI and CVD both agree with direction
	requireFuel  bool          // only enter when funding is "fuel" (contrarian) for the direction
	trades       []*PaperTrade
	armed        map[string]bool
	lastOpen     map[string]time.Time // coin|dir → last entry time (dedupe guard)
}

// isNYSession reports whether t falls in the weak NY block (12-18 UTC), where
// the backtest shows ignition signals perform far worse (US open + macro).
func isNYSession(t time.Time) bool {
	h := t.UTC().Hour()
	return h >= 12 && h < 18
}

// aligned reports whether OI and CVD both confirm the trade direction (the
// "OI+CVD+ for long / OI−CVD− for short" entry, which backtested far stronger
// than the OI/CVD-divergence entry).
func aligned(dir string, oi, cvd float64) bool {
	if dir == "long" {
		return oi > 0 && cvd > 0
	}
	return oi < 0 && cvd < 0
}

func newBook(name string, minScore int, requireCross bool, cooldown time.Duration, trail float64) *paperBook {
	return &paperBook{name: name, minScore: minScore, requireCross: requireCross, cooldown: cooldown, trail: trail, armed: map[string]bool{}, lastOpen: map[string]time.Time{}}
}

// PaperTick advances both books from the latest radar + prices. Call on a ticker.
func (s *Store) PaperTick() {
	s.refreshFunding() // keep the all-coins funding map fresh for entries + tables
	radar := s.Radar()
	tickers, err := s.ex.BinanceAllTickers()
	if err != nil {
		return
	}
	px := make(map[string]float64, len(tickers))
	for _, t := range tickers {
		px[coinOf(t.Symbol)] = t.Price
	}
	pumpSc := map[string]int{}
	for _, it := range radar.Pump {
		pumpSc[it.Coin] = it.Score
	}
	dumpSc := map[string]int{}
	for _, it := range radar.Dump {
		dumpSc[it.Coin] = it.Score
	}
	now := time.Now()

	s.paperMu.Lock()
	defer s.paperMu.Unlock()
	s.tickBook(s.paperMain, radar, px, pumpSc, dumpSc, now)
	s.tickBook(s.paperGamble, radar, px, pumpSc, dumpSc, now)
	s.tickBook(s.paperPremium, radar, px, pumpSc, dumpSc, now)
	if s.db != nil {
		for _, t := range s.paperMain.trades {
			s.db.upsertTrade("main", t)
		}
		for _, t := range s.paperGamble.trades {
			s.db.upsertTrade("gamble", t)
		}
		for _, t := range s.paperPremium.trades {
			s.db.upsertTrade("premium", t)
		}
	}
}

func (s *Store) tickBook(b *paperBook, radar RadarData, px map[string]float64, pumpSc, dumpSc map[string]int, now time.Time) {
	// arm sides seen below the bar (only used when requireCross)
	for _, it := range radar.Pump {
		if it.Score < b.minScore {
			b.armed[it.Coin+"|long"] = true
		}
	}
	for _, it := range radar.Dump {
		if it.Score < b.minScore {
			b.armed[it.Coin+"|short"] = true
		}
	}

	recentClose := map[string]time.Time{}
	for _, tr := range b.trades {
		if tr.Status == "closed" && tr.CloseTime != nil {
			k := tr.Coin + "|" + tr.Dir
			if t, ok := recentClose[k]; !ok || tr.CloseTime.After(t) {
				recentClose[k] = *tr.CloseTime
			}
		}
	}

	active := map[string]bool{}
	open := 0
	for _, tr := range b.trades {
		if tr.Status != "open" {
			continue
		}
		active[tr.Coin] = true
		open++
		p := px[tr.Coin]
		if p <= 0 {
			continue
		}
		tr.Cur = p
		tr.PnLPct = pnl(tr.Dir, tr.Entry, p)
		if b.trail > 0 {
			// self-heal R/Peak after a restart (not persisted): R from TP/entry,
			// Peak from entry (tr.SL — the live stop — IS persisted, so protection holds)
			if tr.R == 0 {
				tr.R = abs2(tr.TP-tr.Entry) / 0.618
			}
			if tr.Peak == 0 {
				tr.Peak = tr.Entry
			}
			// trailing-stop exit: ratchet the stop behind the peak (tr.SL = live stop)
			if tr.Dir == "long" {
				if p > tr.Peak {
					tr.Peak = p
					if ns := tr.Peak - b.trail*tr.R; ns > tr.SL {
						tr.SL = ns
					}
				}
				if p <= tr.SL {
					closeTrade(tr, tr.SL, "trail", now)
				}
			} else {
				if p < tr.Peak {
					tr.Peak = p
					if ns := tr.Peak + b.trail*tr.R; ns < tr.SL {
						tr.SL = ns
					}
				}
				if p >= tr.SL {
					closeTrade(tr, tr.SL, "trail", now)
				}
			}
		} else {
			switch tr.Dir {
			case "long":
				if p >= tr.TP {
					closeTrade(tr, tr.TP, "tp", now)
				} else if p <= tr.SL {
					closeTrade(tr, tr.SL, "sl", now)
				}
			case "short":
				if p <= tr.TP {
					closeTrade(tr, tr.TP, "tp", now)
				} else if p >= tr.SL {
					closeTrade(tr, tr.SL, "sl", now)
				}
			}
		}
		if tr.Status == "open" {
			if (tr.Dir == "long" && dumpSc[tr.Coin] >= b.minScore) ||
				(tr.Dir == "short" && pumpSc[tr.Coin] >= b.minScore) {
				closeTrade(tr, p, "reversed", now)
			}
		}
		if tr.Status == "open" && now.Sub(tr.OpenTime) > paperExpiry {
			closeTrade(tr, p, "expired", now)
		}
		if tr.Status == "closed" { // just closed this tick → alert
			s.notifyTradeClose(b, tr, now)
		}
	}

	consider := func(items []RadarItem, dir string) {
		if b.skipNY && isNYSession(now) {
			return // don't open new positions during the NY session
		}
		for _, it := range items {
			if open >= paperMaxOpen {
				return
			}
			if it.Score < b.minScore || active[it.Coin] {
				continue
			}
			if b.requireAlign && !aligned(dir, it.OIChg, it.CVD) {
				continue // OI and CVD must both confirm the direction
			}
			if b.requireFuel {
				f := s.Funding(it.Coin)
				if !((dir == "long" && f < 0) || (dir == "short" && f > 0)) {
					continue // funding must be contrarian "fuel" for the direction
				}
			}
			key := it.Coin + "|" + dir
			if b.requireCross && !b.armed[key] {
				continue // disciplined: need a fresh cross up
			}
			if t, ok := recentClose[key]; ok && now.Sub(t) < b.cooldown {
				continue
			}
			// dedupe: don't re-enter (or re-notify) the same coin+dir within the
			// cooldown window, even if the prior trade's close path missed above
			if t, ok := b.lastOpen[key]; ok && now.Sub(t) < b.cooldown {
				continue
			}
			p := px[it.Coin]
			if p <= 0 || it.TP <= 0 || it.SL <= 0 {
				continue
			}
			if dir == "long" && !(it.TP > p && it.SL < p) {
				continue
			}
			if dir == "short" && !(it.TP < p && it.SL > p) {
				continue
			}
			tr := &PaperTrade{
				ID:   fmt.Sprintf("%s|%s|%s|%d", b.name, it.Coin, dir, now.UnixMilli()),
				Coin: it.Coin, Dir: dir, Score: it.Score, Entry: p, TP: it.TP, SL: it.SL,
				Cur: p, Status: "open", OpenTime: now, OI: it.OIChg, CVD: it.CVD,
				Funding: s.Funding(it.Coin),
			}
			if b.trail > 0 { // trailing book: derive R, set peak + initial 0.5R stop
				tr.R = abs2(it.TP-it.SL) / 1.118 // R from radar's 0.618/0.5 levels
				tr.Peak = p
				if dir == "long" {
					tr.SL = p - 0.5*tr.R
				} else {
					tr.SL = p + 0.5*tr.R
				}
			}
			b.trades = append(b.trades, tr)
			s.notifyTradeOpen(b, tr)
			active[it.Coin] = true
			b.armed[key] = false
			b.lastOpen[key] = now
			open++
		}
	}
	consider(radar.Pump, "long")
	consider(radar.Dump, "short")

	b.trim()
}

func bookLabel(name string) string {
	switch name {
	case "gamble":
		return "動能狙擊倉"
	case "premium":
		return "精選狙擊倉"
	case "trail":
		return "移動止損"
	}
	return "紀律倉"
}

func dirCN(dir string) string {
	if dir == "short" {
		return "做空"
	}
	return "做多"
}

func outcomeCN(o string) string {
	switch o {
	case "tp":
		return "止盈 TP"
	case "sl":
		return "止損 SL"
	case "trail":
		return "移動止損"
	case "reversed":
		return "反向出場"
	case "expired":
		return "逾時平倉"
	}
	return o
}

func abs2(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// notifyTradeOpen / notifyTradeClose push paper-trade alerts with a 備註
// (score + levels on open; outcome + P&L + holding time on close).
func (s *Store) notifyTradeOpen(b *paperBook, tr *PaperTrade) {
	if !s.notifier.Enabled() {
		return
	}
	go s.notifier.Send(fmt.Sprintf("🟢 <b>[%s] 開倉</b> %s %s\n點火 %d · 進場 $%.4g · TP $%.4g (%+.2f%%) · SL $%.4g (%+.2f%%)\n進場 OI %+.2f%% · CVD %+.2f%% · 費率 %+.4f%%",
		bookLabel(b.name), tr.Coin, dirCN(tr.Dir), tr.Score, tr.Entry,
		tr.TP, pnl(tr.Dir, tr.Entry, tr.TP), tr.SL, pnl(tr.Dir, tr.Entry, tr.SL), tr.OI, tr.CVD, tr.Funding*100))
}

func (s *Store) notifyTradeClose(b *paperBook, tr *PaperTrade, now time.Time) {
	if !s.notifier.Enabled() {
		return
	}
	hold := fmtDur(now.Sub(tr.OpenTime))
	go s.notifier.Send(fmt.Sprintf("🔴 <b>[%s] 平倉</b> %s %s\n結果 %s · 損益 %+.2f%% · 持倉 %s\n進 $%.4g → 出 $%.4g · 進場 OI %+.2f%% / CVD %+.2f%% / 費率 %+.4f%%",
		bookLabel(b.name), tr.Coin, dirCN(tr.Dir), outcomeCN(tr.Outcome), tr.PnLPct, hold, tr.Entry, tr.Cur, tr.OI, tr.CVD, tr.Funding*100))
}

func pnl(dir string, entry, cur float64) float64 {
	if entry == 0 {
		return 0
	}
	if dir == "short" {
		return (entry - cur) / entry * 100
	}
	return (cur - entry) / entry * 100
}

func closeTrade(tr *PaperTrade, exit float64, outcome string, now time.Time) {
	tr.Status = "closed"
	tr.Outcome = outcome
	tr.Cur = exit
	tr.PnLPct = round2(pnl(tr.Dir, tr.Entry, exit))
	t := now
	tr.CloseTime = &t
}

func (b *paperBook) trim() {
	var open, closed []*PaperTrade
	for _, tr := range b.trades {
		if tr.Status == "open" {
			open = append(open, tr)
		} else {
			closed = append(closed, tr)
		}
	}
	sort.Slice(closed, func(i, j int) bool { return closed[i].CloseTime.After(*closed[j].CloseTime) })
	if len(closed) > paperKeepClose {
		closed = closed[:paperKeepClose]
	}
	b.trades = append(open, closed...)
}

func (b *paperBook) state() PaperState {
	st := PaperState{Open: []*PaperTrade{}, Closed: []*PaperTrade{}}
	var sumPnl float64
	for _, tr := range b.trades {
		if tr.Status == "open" {
			st.Open = append(st.Open, tr)
			continue
		}
		st.Closed = append(st.Closed, tr)
		st.Stats.Closed++
		sumPnl += tr.PnLPct
		if tr.PnLPct > 0 {
			st.Stats.Wins++
		} else {
			st.Stats.Losses++
		}
	}
	sort.Slice(st.Open, func(i, j int) bool { return st.Open[i].OpenTime.After(st.Open[j].OpenTime) })
	sort.Slice(st.Closed, func(i, j int) bool { return st.Closed[i].CloseTime.After(*st.Closed[j].CloseTime) })
	if st.Stats.Closed > 0 {
		st.Stats.WinRate = round2(float64(st.Stats.Wins) / float64(st.Stats.Closed) * 100)
		st.Stats.AvgPnl = round2(sumPnl / float64(st.Stats.Closed))
		st.Stats.TotalPnl = round2(sumPnl)
	}
	return st
}

// Paper = disciplined; Gamble = loose; Premium = aligned + funding-fuel control.
func (s *Store) Paper() PaperState   { return s.serve(s.paperMain, 55) }
func (s *Store) Gamble() PaperState  { return s.serve(s.paperGamble, 45) }
func (s *Store) Premium() PaperState { return s.serve(s.paperPremium, 45) }

// serve snapshots a book and stamps live funding + momentum onto open trades.
// The radar is read before taking paperMu to avoid holding it during a recompute.
func (s *Store) serve(b *paperBook, gate int) PaperState {
	radar := s.Radar()
	pumpItem := map[string]RadarItem{}
	dumpItem := map[string]RadarItem{}
	for _, it := range radar.Pump {
		pumpItem[it.Coin] = it
	}
	for _, it := range radar.Dump {
		dumpItem[it.Coin] = it
	}
	s.paperMu.Lock()
	defer s.paperMu.Unlock()
	st := b.state()
	for _, tr := range st.Open {
		tr.CurFunding = s.Funding(tr.Coin)
		tr.Momentum = momentumLight(tr, pumpItem, dumpItem, gate)
	}
	return st
}

// momentumLight grades whether the entry's momentum is still alive, using the
// live radar: ignition score still ≥ gate, and CVD still confirming direction.
// alive = both hold; weak = one holds; dead = neither / dropped off the radar.
func momentumLight(tr *PaperTrade, pump, dump map[string]RadarItem, gate int) string {
	var it RadarItem
	var found bool
	if tr.Dir == "long" {
		it, found = pump[tr.Coin]
	} else {
		it, found = dump[tr.Coin]
	}
	if !found {
		return "dead" // no longer igniting in our direction
	}
	good := 0
	if it.Score >= gate {
		good++
	}
	if (tr.Dir == "long" && it.CVD > 0) || (tr.Dir == "short" && it.CVD < 0) {
		good++
	}
	switch good {
	case 2:
		return "alive"
	case 1:
		return "weak"
	default:
		return "dead"
	}
}

