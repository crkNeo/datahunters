package unlock

import (
	"sort"
	"strings"
	"time"
)

// schedule.go exposes the DATED unlock schedule, for research rather than for
// the dashboard board.
//
// The board (Fetch/Row) answers "what is unlocking soon, biggest first", so it
// collapses everything into next-7d / next-30d totals and drops any token with
// no unlock in the next 30 days. Both of those are wrong for research:
//
//   - A totals field cannot produce "hours until the next unlock", which is the
//     feature that actually matters. The interesting window is signed — the
//     squeeze often fires AFTER the date passes, when the shorts that sold the
//     rumour have no thesis left and have to cover.
//   - Dropping tokens with no upcoming unlock deletes the control group. "No
//     unlock scheduled" is a state to compare against, not an absence of data.
//
// A third distinction this API preserves: a coin the curated slug set does not
// track at all is UNKNOWN, which is not the same as "no unlock". Collapsing
// unknown into zero would quietly poison the control group with coins that may
// well have had an unlock nobody recorded.

// Event is one dated unlock: an increase in the unlocked supply of one
// allocation bucket at one timestamp.
type Event struct {
	Ts       int64   // unix seconds, UTC
	Category string  // zh bucket (投資人 / 內部人 / …) or the raw label
	Amount   float64 // tokens released at this timestamp
}

// Schedule is one token's forward unlock schedule as seen at fetch time,
// together with the contemporaneous price/supply needed to size it.
//
// Price and Circ are recorded rather than folded into a USD or percentage
// figure on purpose: they are the as-of values, so any derived magnitude stays
// reproducible offline and no formula gets frozen into collection.
type Schedule struct {
	Coin      string // upper-case ticker (CoinGecko symbol), falls back to Name
	Name      string
	Gecko     string
	Price     float64
	Circ      float64
	MaxSupply float64
	Events    []Event // sorted by Ts, horizon-limited, staking excluded
}

// FetchSchedules returns the dated schedule for every curated slug, INCLUDING
// tokens with nothing upcoming (Events empty) so callers can tell "no unlock"
// apart from "not tracked".
//
// horizon bounds how far ahead events are kept. ok=false only if every dataset
// fetch failed, matching Fetch's contract.
func (w *Watcher) FetchSchedules(horizon time.Duration) (out []Schedule, ok bool) {
	type pending struct {
		name, gecko string
		max         float64
		events      []Event
	}
	var ps []pending
	var ids []string
	anyFetched := false

	for _, slug := range w.slugs {
		d := w.fetchDataset(slug)
		if d == nil {
			continue
		}
		anyFetched = true
		ps = append(ps, pending{
			name:   d.Name,
			gecko:  d.Gecko,
			max:    d.Supply.Max,
			events: extractEvents(d, time.Now(), horizon),
		})
		if d.Gecko != "" {
			ids = append(ids, d.Gecko)
		}
		time.Sleep(60 * time.Millisecond) // gentle pacing on the dataset CDN
	}
	if !anyFetched {
		return nil, false
	}

	mk := w.enrich(ids)
	for _, p := range ps {
		g := mk[p.gecko]
		coin := strings.ToUpper(g.Symbol)
		if coin == "" {
			coin = p.name
		}
		out = append(out, Schedule{
			Coin: coin, Name: p.name, Gecko: p.gecko,
			Price: g.Price, Circ: g.Circ, MaxSupply: p.max,
			Events: p.events,
		})
	}
	return out, true
}

// extractEvents turns the cumulative "unlocked" series into dated deltas.
//
// The source reports cumulative unlocked supply per bucket, so an event is the
// positive step between consecutive points. Staking buckets are excluded for
// the same reason the board excludes them: continuous emission is not a
// scheduled supply event and would swamp the real cliffs.
func extractEvents(d *dataset, now time.Time, horizon time.Duration) []Event {
	skip := map[string]bool{}
	for _, l := range d.Categories["staking"] {
		skip[l] = true
	}
	labelGroup := map[string]string{}
	for group, labels := range d.Categories {
		for _, l := range labels {
			labelGroup[l] = groupZh(group)
		}
	}
	from := now.Unix()
	to := now.Add(horizon).Unix()

	// Same (ts, category) can appear across multiple raw labels in one bucket;
	// merge them so a category has one amount per timestamp.
	type key struct {
		ts  int64
		cat string
	}
	agg := map[key]float64{}
	for _, c := range d.Doc.Data {
		if skip[c.Label] {
			continue
		}
		cat := labelGroup[c.Label]
		if cat == "" {
			cat = c.Label // unknown group → keep the raw label
		}
		var prev float64
		first := true
		for _, pt := range c.Data {
			if !first && pt.U > prev && pt.T > from && pt.T <= to {
				agg[key{pt.T, cat}] += pt.U - prev
			}
			prev = pt.U
			first = false
		}
	}
	out := make([]Event, 0, len(agg))
	for k, amt := range agg {
		out = append(out, Event{Ts: k.ts, Category: k.cat, Amount: amt})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ts != out[j].Ts {
			return out[i].Ts < out[j].Ts
		}
		return out[i].Category < out[j].Category
	})
	return out
}
