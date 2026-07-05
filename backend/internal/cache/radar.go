package cache

import (
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
	"datahunter/internal/indicator"
)

// RadarItem is one candidate on the breakout radar.
type RadarItem struct {
	Coin     string  `json:"coin"`
	Price    float64 `json:"price"`
	Chg24    float64 `json:"chg_24h"`
	Vol24    float64 `json:"vol_24h"`
	VolSpike float64 `json:"vol_spike"` // recent 3h vol / 48h baseline
	OIChg    float64 `json:"oi_chg"`    // recent OI % change
	CVD      float64 `json:"cvd"`       // recent taker-buy CVD %
	Accel    float64 `json:"accel"`     // last 6h price change %
	Score    int     `json:"score"`     // 0..100 ignition score
	Entry    float64 `json:"entry"`     // suggested pullback/bounce entry
	Trigger  float64 `json:"trigger"`   // momentum entry (breakout/breakdown level)
	TP       float64 `json:"tp"`        // take-profit (1.382 fib extension)
	SL       float64 `json:"sl"`        // stop-loss (swing extreme)
}

// RadarData is the breakout-radar payload: potential pumps and dumps.
type RadarData struct {
	Pump      []RadarItem `json:"pump"`
	Dump      []RadarItem `json:"dump"`
	Stocks    []RadarItem `json:"stocks"` // tokenized US-stock/ETF perps, shown separately
	Scanned   int         `json:"scanned"`
	UpdatedAt string      `json:"updated_at"`
}

// coinTypes returns the cached coin -> underlyingType map (refreshed ~daily),
// so the radar can split crypto ("COIN") from tokenized equities/commodities.
func (s *Store) coinTypes() map[string]string {
	s.symMu.Lock()
	defer s.symMu.Unlock()
	if s.symTypes != nil && time.Since(s.symTime) < 12*time.Hour {
		return s.symTypes
	}
	if m, err := s.ex.BinanceSymbolTypes(); err == nil && len(m) > 0 {
		s.symTypes, s.symTime = m, time.Now()
	}
	return s.symTypes
}

// Radar returns the cached breakout radar (recomputed at most every ~150s —
// PaperTick calls this every 60s, so the TTL is what actually paces the heavy
// all-coins recompute and its Binance weight).
func (s *Store) Radar() RadarData {
	s.radarMu.RLock()
	d, t := s.radar, s.radarTime
	s.radarMu.RUnlock()
	if !t.IsZero() && time.Since(t) < 240*time.Second {
		return d
	}
	// stale: serialise the (heavy, all-coins) recompute so a burst of requests
	// triggers ONE fetch, not one each — re-check freshness after acquiring.
	s.radarCompute.Lock()
	defer s.radarCompute.Unlock()
	s.radarMu.RLock()
	d, t = s.radar, s.radarTime
	s.radarMu.RUnlock()
	if !t.IsZero() && time.Since(t) < 240*time.Second {
		return d
	}
	d = s.computeRadar()
	s.radarMu.Lock()
	s.radar, s.radarTime = d, time.Now()
	s.radarMu.Unlock()
	return d
}

// computeRadar scans the ENTIRE USDT-perp market (concurrently) for EARLY
// ignition: coins whose volume/volatility is spiking but whose price hasn't
// moved much yet. The "earliness" weight demotes coins that already ran, so the
// radar leans toward the start of a move rather than the tail of one.
func (s *Store) computeRadar() RadarData {
	tickers, err := s.ex.BinanceAllTickers()
	if err != nil {
		// no data (e.g. rate-limit ban) — return empty (not nil) slices so the
		// frontend renders the empty state instead of crashing on .length.
		return RadarData{Pump: []RadarItem{}, Dump: []RadarItem{}, Stocks: []RadarItem{}, UpdatedAt: time.Now().Format(time.RFC3339)}
	}

	type tk struct {
		coin       string
		price, chg float64
		vol        float64
	}
	var pool []tk
	for _, t := range tickers {
		coin := coinOf(t.Symbol)
		if stableLike[coin] || t.QuoteVol < 1_000_000 {
			continue // skip stablecoins and near-dead markets only
		}
		pool = append(pool, tk{coin, t.Price, t.ChgPct, t.QuoteVol})
	}
	// cap the scan to the top-N most liquid names: halves the kline/OI weight
	// per sweep, and the paper books trade liquid movers anyway.
	sort.Slice(pool, func(i, j int) bool { return pool[i].vol > pool[j].vol })
	if len(pool) > radarPoolMax {
		pool = pool[:radarPoolMax]
	}

	// concurrent kline scan across the whole pool
	type res struct {
		item RadarItem
		pump bool
		ok   bool
	}
	out := make([]res, len(pool))
	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup
	for i, c := range pool {
		wg.Add(1)
		go func(i int, c tk) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			kl := s.klines1hCached(c.coin, 48) // cached 4 min (shared) — avoids 418 ban
			if len(kl) < 28 {
				return
			}
			n := len(kl)

			// volume spike: last 3h average vs the whole-window average
			var recent, base float64
			for j := n - 3; j < n; j++ {
				recent += kl[j].Volume
			}
			recent /= 3
			for _, b := range kl {
				base += b.Volume
			}
			base /= float64(n)
			volSpike := 0.0
			if base > 0 {
				volSpike = recent / base
			}

			// whale footprint: recent avg trade size vs the window baseline
			var rs, bs float64
			var rc, bc int
			for j := n - 3; j < n; j++ {
				if kl[j].Trades > 0 {
					rs += kl[j].QuoteVol / kl[j].Trades
					rc++
				}
			}
			for j := 0; j < n; j++ {
				if kl[j].Trades > 0 {
					bs += kl[j].QuoteVol / kl[j].Trades
					bc++
				}
			}
			whale := 1.0
			if rc > 0 && bc > 0 && bs > 0 {
				whale = (rs / float64(rc)) / (bs / float64(bc))
			}

			// OI accumulation over ~12h (backtest: strongest pump-ahead signal)
			oiAccum := 0.0
			oiHist := s.oiHist1h(c.coin, 13) // cached 3 min (no WS)
			if len(oiHist) >= 2 && oiHist[0].SumOIValue > 0 {
				oiAccum = indicator.PctChange(oiHist[0].SumOIValue, oiHist[len(oiHist)-1].SumOIValue)
			}

			cvd := indicator.CVDFromKlines(kl, 6)
			accel := indicator.PctChange(kl[n-4].Close, kl[n-1].Close) // last ~3h (fresh nudge)

			// earliness: demote coins that already made a big 24h move
			// (divisor 68 tuned by weight search — softer than the initial 40)
			earliness := clamp(1-math.Abs(c.chg)/68, 0.3, 1)
			score := earlyScore(volSpike, oiAccum, whale, accel, cvd, earliness)

			// direction: OI-accumulation direction (backtest: best net edge for the
			// pump/dump call; 3h momentum was barely 50%). CVD breaks ties when OI
			// is flat (CVD had the best hit-rate).
			pump := oiAccum >= 0
			if math.Abs(oiAccum) < 1 {
				pump = cvd >= 0
			}
			entry, trigger, tp, sl := entryLevels(kl, pump)
			out[i] = res{
				item: RadarItem{
					Coin: c.coin, Price: c.price, Chg24: round2(c.chg), Vol24: c.vol,
					VolSpike: round2(volSpike), OIChg: round2(oiAccum), CVD: round2(cvd),
					Accel: round2(accel), Score: score, Entry: entry, Trigger: trigger,
					TP: tp, SL: sl,
				},
				pump: pump, ok: true,
			}
		}(i, c)
	}
	wg.Wait()

	types := s.coinTypes()
	isCrypto := func(coin string) bool {
		if t, ok := types[coin]; ok {
			return t == "COIN"
		}
		return true // unknown -> keep as crypto rather than hide it
	}

	var pump, dump, stocks []RadarItem
	for _, r := range out {
		if !r.ok {
			continue
		}
		if !isCrypto(r.item.Coin) {
			stocks = append(stocks, r.item)
			continue
		}
		if r.pump {
			pump = append(pump, r.item)
		} else {
			dump = append(dump, r.item)
		}
	}
	byScore := func(a []RadarItem) func(i, j int) bool {
		return func(i, j int) bool { return a[i].Score > a[j].Score }
	}
	sort.Slice(pump, byScore(pump))
	sort.Slice(dump, byScore(dump))
	sort.Slice(stocks, byScore(stocks))

	return RadarData{
		Pump:      topRadar(pump, 12),
		Dump:      topRadar(dump, 12),
		Stocks:    topRadar(stocks, 12),
		Scanned:   len(pool),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
}

// earlyScore blends the backtest-validated pump-ahead signals, with weights
// tuned by a train/test weight search (consistent across 12h & 24h horizons):
// OI accumulation leads, volume surge follows, CVD matters more than first set,
// and whale/accel contribute little. Scaled by earliness (demotes coins that
// already ran). The composite out-predicts any single signal (lift ~1.55–1.83x).
func earlyScore(volSpike, oiAccum, whale, accel, cvd, earliness float64) int {
	vp := clamp((volSpike-1)*6, 0, 40)        // volume surge
	oi := clamp(math.Abs(oiAccum)*1.8, 0, 30) // OI accumulation (strongest)
	wh := clamp((whale-1)*1.0, 0, 12)         // whale footprint (minor)
	ac := clamp(math.Abs(accel)*1.0, 0, 12)
	cv := 0.0
	if (accel >= 0) == (cvd >= 0) {
		cv = clamp(math.Abs(cvd)*0.4, 0, 8)
	}
	return int(math.Round((vp + oi + wh + ac + cv) * earliness))
}

// TP/SL multipliers in units of the recent swing range R. Backtest-optimized
// (TP 0.618R / SL 0.5R) — the tight-TP/tight-SL combo gave the best win-rate and
// expectancy across 12h & 24h, beating the original far 1.382 extension.
// radarPoolMax caps the radar sweep to the most liquid names (by 24h quote
// volume) so the per-sweep Binance weight stays modest.
const radarPoolMax = 200

const tpMult, slMult = 0.618, 0.5

// entryLevels derives entry, trigger and TP/SL from the recent ~12h swing.
// Entry is at market (the radar targets the early stage); TP/SL are set relative
// to the current price by tpMult/slMult × the swing range.
func entryLevels(kl []exchange.Candle, pump bool) (entry, trigger, tp, sl float64) {
	n := len(kl)
	w := 12
	if n < w {
		w = n
	}
	hi, lo := kl[n-1].High, kl[n-1].Low
	for i := n - w; i < n; i++ {
		if kl[i].High > hi {
			hi = kl[i].High
		}
		if kl[i].Low < lo {
			lo = kl[i].Low
		}
	}
	rng := hi - lo
	cur := kl[n-1].Close
	if pump {
		return hi - 0.382*rng, hi * 1.003, cur + tpMult*rng, cur - slMult*rng
	}
	return lo + 0.382*rng, lo * 0.997, cur - tpMult*rng, cur + slMult*rng
}

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func topRadar(r []RadarItem, n int) []RadarItem {
	if len(r) > n {
		return r[:n]
	}
	if r == nil {
		return []RadarItem{}
	}
	return r
}
