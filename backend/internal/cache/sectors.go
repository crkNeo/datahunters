package cache

import (
	"fmt"
	"sort"
	"time"
)

// sectors.go: hourly 板塊強弱 + 板塊輪動. It buckets the whole market's 24h change by
// sector (coinSector map), ranks sectors by strength, and detects rotation — the
// hour-over-hour change in each sector's strength-relative-to-BTC. A clearly
// heating sector is pushed. Runs once per hour.

const sectorRotatePush = 1.5 // Δ(vs-BTC) ≥ this pp AND vsBTC>0 → 「板塊轉強」push

// SectorRow is one sector's aggregated strength.
type SectorRow struct {
	Sector  string  `json:"sector"`
	AvgChg  float64 `json:"avg_chg"`  // equal-weight mean 24h %
	VwChg   float64 `json:"vw_chg"`   // volume-weighted 24h %
	Breadth float64 `json:"breadth"`  // % of members up (24h)
	VsBTC   float64 `json:"vs_btc"`   // avgChg − BTC 24h%  (relative strength)
	Delta   float64 `json:"delta"`    // vsBTC change vs last hour (rotation: + heating, − cooling)
	Count   int     `json:"count"`
}

// SectorData is the 板塊強弱 tab payload.
type SectorData struct {
	Rows      []SectorRow `json:"rows"`
	BtcChg    float64     `json:"btc_chg"`
	UpdatedAt string      `json:"updated_at"`
}

// SectorTick recomputes the sector board once per hour, diffs against last hour for
// rotation, and pushes the strongest newly-heating sector. First tick only seeds.
func (s *Store) SectorTick() {
	h := time.Now().UTC().Unix() / 3600
	if h == s.sectorBucket {
		return
	}
	tickers, err := s.ex.BinanceAllTickers()
	if err != nil || len(tickers) == 0 {
		s.apiFail("板塊強弱", "取市場行情失敗")
		return
	}
	s.apiOK("板塊強弱")
	s.sectorBucket = h
	seeded := s.sectorSeeded
	s.sectorSeeded = true

	type acc struct {
		sum, up, volSum, volChgSum float64
		n                          int
	}
	m := map[string]*acc{}
	var btcChg float64
	for _, t := range tickers {
		if t.Symbol == "BTCUSDT" {
			btcChg = t.ChgPct
		}
		sec, ok := coinSector[coinOf(t.Symbol)]
		if !ok {
			continue // not in the sector map → 其他, skip
		}
		a := m[sec]
		if a == nil {
			a = &acc{}
			m[sec] = a
		}
		a.sum += t.ChgPct
		a.n++
		if t.ChgPct > 0 {
			a.up++
		}
		a.volSum += t.QuoteVol
		a.volChgSum += t.ChgPct * t.QuoteVol
	}

	rows := make([]SectorRow, 0, len(m))
	prev := map[string]float64{}
	for sec, a := range m {
		if a.n == 0 {
			continue
		}
		avg := a.sum / float64(a.n)
		vw := avg
		if a.volSum > 0 {
			vw = a.volChgSum / a.volSum
		}
		vsBTC := avg - btcChg
		delta := 0.0
		if p, ok := s.sectorPrev[sec]; ok {
			delta = vsBTC - p
		}
		rows = append(rows, SectorRow{
			Sector: sec, AvgChg: round2(avg), VwChg: round2(vw),
			Breadth: round2(a.up / float64(a.n) * 100), VsBTC: round2(vsBTC),
			Delta: round2(delta), Count: a.n,
		})
		prev[sec] = vsBTC
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].AvgChg > rows[j].AvgChg })

	s.sectorMu.Lock()
	s.sectorBoard = rows
	s.sectorBtcChg = round2(btcChg)
	s.sectorTime = time.Now()
	s.sectorMu.Unlock()
	s.sectorPrev = prev // baseline for next hour's Δ

	if seeded { // push the single strongest newly-heating sector (rotation signal)
		best := SectorRow{}
		for _, r := range rows {
			if r.Delta > best.Delta {
				best = r
			}
		}
		if best.Delta >= sectorRotatePush && best.VsBTC > 0 {
			s.PushSend("📊 板塊輪動", fmt.Sprintf("%s 板塊轉強(較上小時 +%.1fpp,平均 %+.2f%%)", best.Sector, best.Delta, best.AvgChg), "/?tab=sectors")
		}
	}
}

// SectorBoard returns the cached sector board (strongest first).
func (s *Store) SectorBoard() SectorData {
	s.sectorMu.RLock()
	defer s.sectorMu.RUnlock()
	rows := make([]SectorRow, len(s.sectorBoard))
	copy(rows, s.sectorBoard)
	out := SectorData{Rows: rows, BtcChg: s.sectorBtcChg}
	if !s.sectorTime.IsZero() {
		out.UpdatedAt = s.sectorTime.Format(time.RFC3339)
	}
	return out
}
