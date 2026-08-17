package collector

import (
	"log"
	"strings"
	"time"

	"datahunter/internal/unlock"
)

// unlocks.go records the token-unlock schedule once a day.
//
// Why an unlock feature belongs in a squeeze screener at all — the intuition
// runs backwards from the obvious one. An unlock is read as bearish, and over
// weeks it usually is. But over MINUTES it is the cleanest generator of the
// exact fuel this screener is looking for:
//
//	known date → traders short into it → price grinds to new lows, funding
//	turns negative, short OI piles up → any trigger (often the event passing
//	without the dump) forces them all to cover at once.
//
// So this is a Layer-1 feature: a days-ahead prior on WHERE crowded shorts are
// likely to accumulate, not a Layer-2 trigger. It is also the only genuinely
// forward-looking input in the whole pipeline — every other column describes
// something that has already happened.
//
// One consequence worth stating plainly: unlocks are sparse. Across ~100 coins
// they arrive on a monthly cadence per coin, so a few days of collection yields
// a single-digit sample and this feature will NOT move the early lift analysis.
// Its entire value is in having started recording early, because the schedule
// as-seen-today cannot be reconstructed later.

// UnlockHorizon is how far ahead the schedule is recorded. Far enough that the
// "days before the event" window is always covered, short enough to keep the
// daily re-snapshot small.
const UnlockHorizon = 180 * 24 * time.Hour

// RunUnlocks records the schedule at startup and once a day thereafter.
//
// It runs on its own timer rather than inside the minute loop: it talks to a
// different set of hosts (the emissions dataset CDN and CoinGecko), takes tens
// of seconds, and must never be able to delay a snapshot tick.
func (c *Collector) RunUnlocks(stop <-chan struct{}) {
	if c.unlockW == nil {
		return
	}
	c.captureUnlocks()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			c.captureUnlocks()
		}
	}
}

func (c *Collector) captureUnlocks() {
	started := time.Now()
	scheds, ok := c.unlockW.FetchSchedules(UnlockHorizon)
	if !ok {
		log.Printf("unlocks: every schedule fetch failed — skipping today's snapshot")
		return
	}

	c.mu.RLock()
	universe := append([]string(nil), c.universe...)
	c.mu.RUnlock()

	// Coins the collector is tracking today, keyed the same way the unlock
	// source keys them, so coverage can be judged coin-for-coin.
	inUniverse := make(map[string]bool, len(universe))
	for _, sym := range universe {
		inUniverse[coinFromSymbol(sym)] = true
	}

	now := time.Now()
	day := dayKey(now)
	events, snaps := buildUnlockRows(scheds, inUniverse, day, now.Unix())

	if err := writeUnlockEvents(c.db, events); err != nil {
		log.Printf("unlocks: %v", err)
	}
	if err := writeUnlockSnaps(c.db, snaps); err != nil {
		log.Printf("unlocks: %v", err)
	}
	covered := 0
	for _, s := range snaps {
		if s.InUniverse && s.Covered {
			covered++
		}
	}
	log.Printf("unlocks: day=%d schedules=%d events=%d coverage=%d/%d tracked coins took=%s",
		day, len(snaps), len(events), covered, len(inUniverse), time.Since(started).Round(time.Second))
}

// buildUnlockRows turns fetched schedules into the two tables' rows. Pure, so
// the coverage and next-unlock logic is testable without network or database.
func buildUnlockRows(scheds []unlock.Schedule, inUniverse map[string]bool, day int, now int64) ([]unlockEventRow, []unlockSnapRow) {
	var (
		events []unlockEventRow
		snaps  []unlockSnapRow
		seen   = map[string]bool{}
	)
	for _, s := range scheds {
		if s.Coin == "" {
			continue
		}
		coin := strings.ToUpper(s.Coin)
		if seen[coin] {
			continue // two slugs mapping to one ticker; first wins
		}
		seen[coin] = true

		row := unlockSnapRow{
			Day: day, Coin: coin,
			InUniverse: inUniverse[coin],
			Covered:    true,
			Price:      s.Price, Circ: s.Circ, MaxSupply: s.MaxSupply,
		}
		for _, e := range s.Events {
			if e.Ts <= now {
				continue // already happened; the horizon is forward-looking
			}
			row.HorizonAmt += e.Amount
			row.EventsN++
			switch {
			case row.NextUnlockTs == 0 || e.Ts < row.NextUnlockTs:
				row.NextUnlockTs, row.NextUnlockAmt = e.Ts, e.Amount
			case e.Ts == row.NextUnlockTs:
				// Same moment, different allocation bucket — the tradable
				// quantity is the whole cliff, not one bucket's slice of it.
				row.NextUnlockAmt += e.Amount
			}
			events = append(events, unlockEventRow{
				AsofDay: day, Coin: coin, UnlockTs: e.Ts,
				Category: e.Category, Amount: e.Amount,
			})
		}
		row.HasUpcoming = row.NextUnlockTs > 0
		snaps = append(snaps, row)
	}

	// Tracked coins the unlock source knows nothing about are recorded with
	// covered=0. Leaving them out entirely would let a later analysis silently
	// treat "unknown" as "no unlock" — and the coins missing from a curated
	// major-cap slug list are disproportionately the small, new and meme names
	// that produce the very moves this project is chasing.
	for coin := range inUniverse {
		if seen[coin] {
			continue
		}
		snaps = append(snaps, unlockSnapRow{Day: day, Coin: coin, InUniverse: true})
	}
	return events, snaps
}

// coinFromSymbol maps a perp symbol to the ticker the unlock source uses.
//
// The leading-multiplier contracts matter here: Binance lists cheap tokens as
// 1000PEPEUSDT / 1000000MOGUSDT, and a naive suffix trim would look up "1000PEPE"
// and find nothing. Those contracts are exactly the low-float names most prone
// to violent moves, so silently dropping their coverage would bias the dataset
// against the population of interest.
func coinFromSymbol(sym string) string {
	s := strings.ToUpper(strings.TrimSuffix(sym, "USDT"))
	for _, p := range []string{"1000000", "100000", "10000", "1000"} {
		if len(s) > len(p) && strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

type unlockEventRow struct {
	AsofDay  int
	Coin     string
	UnlockTs int64
	Category string
	Amount   float64
}

type unlockSnapRow struct {
	Day           int
	Coin          string
	InUniverse    bool
	Covered       bool
	HasUpcoming   bool
	NextUnlockTs  int64
	NextUnlockAmt float64
	HorizonAmt    float64
	EventsN       int
	Price         float64
	Circ          float64
	MaxSupply     float64
}

// Both writers use INSERT IGNORE, so the first capture of a UTC day is the one
// that stands. That is deliberate: the point of an as-of snapshot is to pin
// what was knowable at a fixed moment, and a second run later the same day
// would otherwise quietly move that moment forward.
func writeUnlockEvents(db execer, rows []unlockEventRow) error {
	cols := []string{"asof_day", "coin", "unlock_ts", "category", "amount"}
	return insertChunkRows(db, "unlock_events", cols, len(rows), func(i int) []any {
		r := rows[i]
		return []any{r.AsofDay, r.Coin, r.UnlockTs, r.Category, r.Amount}
	})
}

func writeUnlockSnaps(db execer, rows []unlockSnapRow) error {
	cols := []string{"day", "coin", "in_universe", "covered", "has_upcoming",
		"next_unlock_ts", "next_unlock_amt", "horizon_amt", "events_n",
		"price", "circ", "max_supply"}
	return insertChunkRows(db, "unlock_snapshot_1d", cols, len(rows), func(i int) []any {
		r := rows[i]
		return []any{r.Day, r.Coin, b2i(r.InUniverse), b2i(r.Covered), b2i(r.HasUpcoming),
			r.NextUnlockTs, r.NextUnlockAmt, r.HorizonAmt, r.EventsN,
			r.Price, r.Circ, r.MaxSupply}
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UnlockSource is the slice of *unlock.Watcher the collector needs, kept as an
// interface so the capture path can be driven without network access.
type UnlockSource interface {
	FetchSchedules(horizon time.Duration) ([]unlock.Schedule, bool)
}
