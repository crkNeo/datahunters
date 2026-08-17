package collector

import (
	"database/sql"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"datahunter/internal/exchange"
)

// collector.go runs the 1-minute snapshot loop.
//
// Scope discipline — what this deliberately does NOT do:
//   - no entry logic, no thresholds, no scoring. The moment a rule is baked
//     into the collector, changing the rule means re-collecting the data. Rules
//     live offline, replayed against this history as often as needed.
//   - no per-trade tick data. taker_buy_quote arrives inside the kline for free
//     and carries the same signed-flow information CVD needs, at a fraction of
//     the request budget aggTrades would cost.
//   - no alerting. This build exists to answer whether there is an edge at all;
//     that question is settled with the labels table, not with notifications.

// Config is the collector's tunables. Defaults target ~100 symbols on a single
// outbound IP without approaching Binance's per-minute weight ceiling.
type Config struct {
	Universe      int           // how many symbols to track, ranked by 24h turnover
	Workers       int           // concurrent per-symbol fetches
	DepthEvery    int           // minutes between order-book snapshots (0 disables)
	DepthLimit    int           // order-book levels per side
	SpotEvery     int           // minutes between spot snapshots (0 disables)
	SettleDelay   time.Duration // wait past the minute boundary before reading
	RetentionDays int           // partitions older than this are dropped (0 keeps all)
}

func DefaultConfig() Config {
	return Config{
		Universe:   100,
		Workers:    8,
		DepthEvery: 5,
		// 100 levels/side spans ±2% comfortably on liquid books and costs a
		// fraction of limit=500's weight. Books that end sooner are flagged
		// truncated rather than silently under-reported.
		DepthLimit:    100,
		SpotEvery:     1,
		SettleDelay:   10 * time.Second,
		RetentionDays: 60,
	}
}

type Collector struct {
	ex  *exchange.Client
	db  *sql.DB
	cfg Config

	mu       sync.RWMutex
	universe []string        // ranked, highest turnover first
	spotOK   map[string]bool // symbol has a live spot pair
}

func New(ex *exchange.Client, db *sql.DB, cfg Config) *Collector {
	return &Collector{ex: ex, db: db, cfg: cfg, spotOK: map[string]bool{}}
}

// Init prepares the schema and loads the first universe. Called before Run.
func (c *Collector) Init() error {
	if err := ensureSchema(c.db, c.cfg.RetentionDays); err != nil {
		return err
	}
	return c.refreshUniverse()
}

// Run blocks, collecting one snapshot per minute until stop is closed.
func (c *Collector) Run(stop <-chan struct{}) {
	// Universe membership and partition upkeep move on a much slower clock than
	// the snapshot itself, so they get their own timer rather than a modulo
	// check inside the hot path.
	slow := time.NewTicker(time.Hour)
	defer slow.Stop()
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-slow.C:
				if err := c.refreshUniverse(); err != nil {
					log.Printf("collector: universe refresh: %v", err)
				}
				if err := ensurePartitions(c.db, c.cfg.RetentionDays); err != nil {
					log.Printf("collector: partitions: %v", err)
				}
			}
		}
	}()

	for {
		wait := time.Until(nextTickAt(time.Now(), c.cfg.SettleDelay))
		select {
		case <-stop:
			return
		case <-time.After(wait):
			c.tick(time.Now())
		}
	}
}

// nextTickAt returns the next minute boundary plus the settle delay. The delay
// exists because a bar read the instant it closes can still be revised — the
// exchange is aggregating trades right up to the boundary — and because OI and
// klines are separate calls that should describe as close to the same moment as
// the REST API allows.
func nextTickAt(now time.Time, delay time.Duration) time.Time {
	at := now.Truncate(time.Minute).Add(delay)
	if !at.After(now) {
		at = at.Add(time.Minute)
	}
	return at
}

// tick collects the bar that closed at the most recent minute boundary.
func (c *Collector) tick(now time.Time) {
	started := time.Now()
	// The bar that just closed opened one minute before this boundary.
	barTs := now.Truncate(time.Minute).Add(-time.Minute).UnixMilli()
	minute := time.UnixMilli(barTs).UTC().Minute()

	c.mu.RLock()
	syms := append([]string(nil), c.universe...)
	spotOK := c.spotOK
	c.mu.RUnlock()
	if len(syms) == 0 {
		log.Printf("collector: empty universe, skipping tick")
		return
	}

	// One batch call covers mark/index/funding/settlement for every symbol.
	prem, err := c.ex.BinancePremiumIndexAll()
	if err != nil {
		log.Printf("collector: premiumIndex: %v", err)
		prem = map[string]exchange.PremiumIndex{}
	}

	doDepth := c.cfg.DepthEvery > 0 && minute%c.cfg.DepthEvery == 0
	doSpot := c.cfg.SpotEvery > 0 && minute%c.cfg.SpotEvery == 0

	var (
		mu      sync.Mutex
		snaps   []snapRow
		depths  []depthRow
		spots   []spotRow
		nErrOI  int
		nErrBar int
	)

	sem := make(chan struct{}, max(1, c.cfg.Workers))
	var wg sync.WaitGroup
	for _, sym := range syms {
		wg.Add(1)
		go func(sym string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// limit=2 so the closed bar is present even if the forming one has
			// already appeared; we match on openTime rather than trusting order.
			kl, err := c.ex.BinanceKlines(sym, "1m", 2)
			if err != nil {
				mu.Lock()
				nErrBar++
				mu.Unlock()
				return
			}
			var bar exchange.Candle
			var found bool
			for _, k := range kl {
				if k.Ts == barTs {
					bar, found = k, true
					break
				}
			}
			if !found {
				mu.Lock()
				nErrBar++
				mu.Unlock()
				return
			}

			p := prem[sym]
			// OI has no historical endpoint at 1m resolution, so this is a
			// point-in-time read taken shortly AFTER the bar closed — treat it
			// as "OI as of the bar's close", not as an average across the bar.
			oi, err := c.ex.BinanceOpenInterest(sym)
			if err != nil {
				mu.Lock()
				nErrOI++
				mu.Unlock()
			}
			// USDT-M perp quantities are denominated in the base asset, so
			// notional is simply size × price. Mark price is preferred over the
			// bar close because it is the price liquidations are struck at.
			px := p.Mark
			if px <= 0 {
				px = bar.Close
			}

			row := snapRow{
				Ts: barTs, Symbol: sym,
				Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close,
				VolQuote: bar.QuoteVol, Trades: bar.Trades,
				TakerBuyQuote: bar.TakerBuyQuote,
				OIContracts:   exchange.NullZero(oi),
				OIUSD:         exchange.NullZero(oi * px),
				Funding:       p.Funding, NextFundingTs: p.NextFundingTs,
				Mark: p.Mark, IndexPx: p.Index,
			}

			var dr *depthRow
			if doDepth {
				if d, trunc, err := c.ex.BinanceDepth(sym, c.cfg.DepthLimit); err == nil {
					dr = &depthRow{
						Ts: barTs, Symbol: sym,
						Bid1: d.Bid1, Ask1: d.Ask1, SpreadBps: exchange.NullZero(d.SpreadBps),
						BidUSD05: d.BidUSD05, BidUSD1: d.BidUSD1, BidUSD2: d.BidUSD2,
						AskUSD05: d.AskUSD05, AskUSD1: d.AskUSD1, AskUSD2: d.AskUSD2,
						Truncated: trunc,
					}
				}
			}

			var sr *spotRow
			if doSpot && spotOK[sym] {
				if sk, err := c.ex.BinanceSpotKlines(sym, "1m", 2); err == nil {
					for _, k := range sk {
						if k.Ts == barTs {
							sr = &spotRow{Ts: barTs, Symbol: sym, Close: k.Close,
								VolQuote: k.QuoteVol, TakerBuyQuote: k.TakerBuyQuote}
							break
						}
					}
				}
			}

			mu.Lock()
			snaps = append(snaps, row)
			if dr != nil {
				depths = append(depths, *dr)
			}
			if sr != nil {
				spots = append(spots, *sr)
			}
			mu.Unlock()
		}(sym)
	}
	wg.Wait()

	if err := writeSnaps(c.db, snaps); err != nil {
		log.Printf("collector: %v", err)
	}
	if err := writeDepths(c.db, depths); err != nil {
		log.Printf("collector: %v", err)
	}
	if err := writeSpots(c.db, spots); err != nil {
		log.Printf("collector: %v", err)
	}
	if err := writeRegime(c.db, buildRegime(barTs, snaps)); err != nil {
		log.Printf("collector: %v", err)
	}

	log.Printf("collector: bar=%s snaps=%d depth=%d spot=%d errs(bar=%d oi=%d) took=%s",
		time.UnixMilli(barTs).UTC().Format("15:04"), len(snaps), len(depths), len(spots),
		nErrBar, nErrOI, time.Since(started).Round(time.Millisecond))
}

// buildRegime reduces the minute's cross-section to the market-state row.
//
// median rather than mean return, because a single +60% coin would drag a mean
// far enough to make every other coin look weak against it — and the whole
// point of the baseline is to measure a coin against its peers.
func buildRegime(barTs int64, snaps []snapRow) regimeRow {
	r := regimeRow{Ts: barTs, Universe: len(snaps)}
	rets := make([]float64, 0, len(snaps))
	for _, s := range snaps {
		r.TotalOIUSD += s.OIUSD
		switch s.Symbol {
		case "BTCUSDT":
			r.BTCPx, r.BTCOIUSD = s.Close, s.OIUSD
		case "ETHUSDT":
			r.ETHPx, r.ETHOIUSD = s.Close, s.OIUSD
		}
		if s.Open <= 0 {
			continue
		}
		ret := s.Close/s.Open - 1
		if !finiteF(ret) {
			continue
		}
		rets = append(rets, ret)
		if ret > 0 {
			r.AdvCount++
		} else if ret < 0 {
			r.DecCount++
		}
	}
	if len(rets) == 0 {
		return r
	}
	sort.Float64s(rets)
	r.MedianRet = median(rets)
	// Cross-sectional dispersion: high when money is picking individual coins,
	// low when the whole board moves together. In the low state a "best coin"
	// ranking is mostly re-ranking market beta.
	var mean float64
	for _, v := range rets {
		mean += v
	}
	mean /= float64(len(rets))
	var ss float64
	for _, v := range rets {
		ss += (v - mean) * (v - mean)
	}
	r.Disp = math.Sqrt(ss / float64(len(rets)))
	if !finiteF(r.Disp) {
		r.Disp = 0
	}
	return r
}

// refreshUniverse re-ranks by 24h turnover and records the full tradable symbol
// list for the day.
//
// The daily universe_1d row covers EVERY trading perp, not just the tracked
// slice, because it is the survivorship record: a coin that gets delisted next
// month has to still be visible to an analysis run after the delisting, or the
// backtest quietly drops exactly the population most prone to violent moves.
func (c *Collector) refreshUniverse() error {
	info, err := c.ex.BinanceSymbolInfo()
	if err != nil {
		return err
	}
	tickers, err := c.ex.BinanceAllTickers()
	if err != nil {
		return err
	}
	type cand struct {
		sym string
		vol float64
	}
	cands := make([]cand, 0, len(tickers))
	for _, t := range tickers {
		si, ok := info[t.Symbol]
		if !ok || si.Status != "TRADING" {
			continue
		}
		cands = append(cands, cand{t.Symbol, t.QuoteVol})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].vol > cands[j].vol })

	n := c.cfg.Universe
	if n <= 0 || n > len(cands) {
		n = len(cands)
	}
	picked := make([]string, 0, n)
	inUniverse := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		picked = append(picked, cands[i].sym)
		inUniverse[cands[i].sym] = true
	}
	// BTC and ETH anchor the regime row; keep them even if turnover ever ranks
	// them out of the tracked slice.
	for _, must := range []string{"BTCUSDT", "ETHUSDT"} {
		if !inUniverse[must] && info[must].Status == "TRADING" {
			picked = append(picked, must)
			inUniverse[must] = true
		}
	}

	spot, err := c.ex.BinanceSpotSymbols()
	if err != nil {
		log.Printf("collector: spot symbols: %v (spot capture degraded)", err)
		spot = map[string]bool{}
	}

	day := dayKey(time.Now())
	rows := make([]universeRow, 0, len(cands))
	for i, cd := range cands {
		si := info[cd.sym]
		rows = append(rows, universeRow{
			Day: day, Symbol: cd.sym, Status: si.Status, OnboardTs: si.OnboardTs,
			QuoteVol24h: cd.vol, RankVol: i + 1, Selected: inUniverse[cd.sym],
		})
	}
	if err := writeUniverse(c.db, rows); err != nil {
		log.Printf("collector: %v", err)
	}

	c.mu.Lock()
	c.universe = picked
	c.spotOK = spot
	c.mu.Unlock()
	log.Printf("collector: universe=%d tracked (of %d trading perps), spot pairs=%d",
		len(picked), len(cands), len(spot))
	return nil
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func finiteF(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
