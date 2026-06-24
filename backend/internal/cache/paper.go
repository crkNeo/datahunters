package cache

import (
	"sort"
	"time"
)

// PaperTrade is one simulated trade taken from a breakout-radar signal. Entry is
// the market price when the signal fired; TP/SL are the radar's fib levels.
type PaperTrade struct {
	Coin      string     `json:"coin"`
	Dir       string     `json:"dir"` // long | short
	Score     int        `json:"score"`
	Entry     float64    `json:"entry"` // fill price (market at signal)
	TP        float64    `json:"tp"`
	SL        float64    `json:"sl"`
	Cur       float64    `json:"cur"`     // latest price
	PnLPct    float64    `json:"pnl_pct"` // live (open) or final (closed)
	Status    string     `json:"status"`  // open | closed
	Outcome   string     `json:"outcome"` // "" | tp | sl | expired | reversed
	OpenTime  time.Time  `json:"open_time"`
	CloseTime *time.Time `json:"close_time,omitempty"`
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
	paperKeepClose = 60
)

// paperBook is one simulated-trading account with its own entry rules. The main
// book is disciplined (high bar, fresh-cross only); the gamble book is loose
// (low bar, chases already-elevated coins) — a live A/B of discipline vs FOMO.
type paperBook struct {
	minScore     int
	requireCross bool // true: only enter on a fresh cross up; false: chase anything
	cooldown     time.Duration
	trades       []*PaperTrade
	armed        map[string]bool
}

func newBook(minScore int, requireCross bool, cooldown time.Duration) *paperBook {
	return &paperBook{minScore: minScore, requireCross: requireCross, cooldown: cooldown, armed: map[string]bool{}}
}

// PaperTick advances both books from the latest radar + prices. Call on a ticker.
func (s *Store) PaperTick() {
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
		if tr.Status == "open" {
			if (tr.Dir == "long" && dumpSc[tr.Coin] >= b.minScore) ||
				(tr.Dir == "short" && pumpSc[tr.Coin] >= b.minScore) {
				closeTrade(tr, p, "reversed", now)
			}
		}
		if tr.Status == "open" && now.Sub(tr.OpenTime) > paperExpiry {
			closeTrade(tr, p, "expired", now)
		}
	}

	consider := func(items []RadarItem, dir string) {
		for _, it := range items {
			if open >= paperMaxOpen {
				return
			}
			if it.Score < b.minScore || active[it.Coin] {
				continue
			}
			key := it.Coin + "|" + dir
			if b.requireCross && !b.armed[key] {
				continue // disciplined: need a fresh cross up
			}
			if t, ok := recentClose[key]; ok && now.Sub(t) < b.cooldown {
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
			b.trades = append(b.trades, &PaperTrade{
				Coin: it.Coin, Dir: dir, Score: it.Score, Entry: p, TP: it.TP, SL: it.SL,
				Cur: p, Status: "open", OpenTime: now,
			})
			active[it.Coin] = true
			b.armed[key] = false
			open++
		}
	}
	consider(radar.Pump, "long")
	consider(radar.Dump, "short")

	b.trim()
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

// Paper returns the disciplined book; Gamble returns the loose book.
func (s *Store) Paper() PaperState {
	s.paperMu.Lock()
	defer s.paperMu.Unlock()
	return s.paperMain.state()
}

func (s *Store) Gamble() PaperState {
	s.paperMu.Lock()
	defer s.paperMu.Unlock()
	return s.paperGamble.state()
}
