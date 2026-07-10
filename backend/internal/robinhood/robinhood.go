// Package robinhood watches Robinhood's crypto currency-pair list and reports
// newly-tradable coins — a Coinbase-style "supported asset" diff, since Robinhood
// listings often move the coin. The list endpoint is public (no key); it's an
// undocumented internal endpoint, so treat it as best-effort and poll modestly.
package robinhood

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"
)

const pairsURL = "https://nummus.robinhood.com/currency_pairs/"

// Coin is one tradable Robinhood crypto asset.
type Coin struct {
	Code   string `json:"code"`   // asset code, e.g. "SUI"
	Name   string `json:"name"`   // asset name
	Symbol string `json:"symbol"` // display pair, e.g. "SUI-USD"
}

// Watcher polls the currency-pair list and remembers which codes are tradable, so
// it can report newly-added ones. The first Poll only seeds the baseline.
type Watcher struct {
	http   *http.Client
	seen   map[string]bool
	seeded bool
}

func NewWatcher() *Watcher {
	return &Watcher{http: &http.Client{Timeout: 15 * time.Second}, seen: map[string]bool{}}
}

func (w *Watcher) fetch() ([]Coin, error) {
	req, err := http.NewRequest("GET", pairsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct {
			DisplaySymbol string `json:"display_symbol"`
			Tradability   string `json:"tradability"`
			Asset         struct {
				Code string `json:"code"`
				Name string `json:"name"`
			} `json:"asset_currency"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	var coins []Coin
	for _, r := range out.Results {
		if r.Tradability != "tradable" || r.Asset.Code == "" {
			continue // only currently-tradable pairs count as a "listing"
		}
		coins = append(coins, Coin{Code: r.Asset.Code, Name: r.Asset.Name, Symbol: r.DisplaySymbol})
	}
	return coins, nil
}

// Poll returns (fresh, all): coins that became tradable since the last poll (nil on
// the first, seeding poll so history isn't replayed), and the full current tradable
// list (sorted by code).
func (w *Watcher) Poll() (fresh, all []Coin, err error) {
	coins, err := w.fetch()
	if err != nil {
		return nil, nil, err
	}
	if len(coins) == 0 {
		return nil, nil, errEmpty
	}
	for _, c := range coins {
		if !w.seen[c.Code] {
			if w.seeded {
				fresh = append(fresh, c)
			}
			w.seen[c.Code] = true
		}
	}
	w.seeded = true
	all = coins
	sort.Slice(all, func(i, j int) bool { return all[i].Code < all[j].Code })
	return fresh, all, nil
}

type rhErr string

func (e rhErr) Error() string { return string(e) }

const errEmpty = rhErr("robinhood: empty currency-pair list")
