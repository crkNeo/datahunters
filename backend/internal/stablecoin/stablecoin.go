// Package stablecoin pulls aggregate stablecoin-supply data from DefiLlama's free
// (no-key) API. Net stablecoin minting is a proxy for capital entering crypto:
// growing supply = fresh dollars parked on exchanges ready to buy; shrinking
// supply = capital leaving. Best-effort — any fetch/parse error keeps the old value.
package stablecoin

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Second}

// Point is one day's total stablecoin market cap (US$, absolute dollars).
type Point struct {
	T    int64   `json:"t"`
	Mcap float64 `json:"mcap"`
}

// Coin is one stablecoin's supply + recent change (US$).
type Coin struct {
	Symbol  string  `json:"symbol"`
	Mcap    float64 `json:"mcap"`
	DayChg  float64 `json:"day_chg"`
	WeekChg float64 `json:"week_chg"`
}

// Summary is the aggregate stablecoin snapshot for the capital-flow panel.
type Summary struct {
	Total    float64 `json:"total"`     // latest total stablecoin mcap (US$)
	DayChg   float64 `json:"day_chg"`   // Δ vs previous day (US$; + = net mint/inflow)
	WeekChg  float64 `json:"week_chg"`  // Δ vs 7 days ago
	MonthChg float64 `json:"month_chg"` // Δ vs 30 days ago
	History  []Point `json:"history"`   // total mcap trend, oldest→newest
	Coins    []Coin  `json:"coins"`     // top stablecoins by supply
}

func getJSON(url string, v any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// Fetch builds the summary: total-supply trend (last histDays) + day/week/month
// deltas from the chart, plus the top nCoins stablecoins with their day/week change.
func Fetch(histDays, nCoins int) (Summary, error) {
	var chart []struct {
		Date string `json:"date"`
		Tot  struct {
			Pegged float64 `json:"peggedUSD"`
		} `json:"totalCirculatingUSD"`
	}
	if err := getJSON("https://stablecoins.llama.fi/stablecoincharts/all", &chart); err != nil {
		return Summary{}, err
	}
	if len(chart) < 2 {
		return Summary{}, errEmpty
	}
	at := func(fromEnd int) float64 { // fromEnd 0 = latest
		i := len(chart) - 1 - fromEnd
		if i < 0 {
			i = 0
		}
		return chart[i].Tot.Pegged
	}
	s := Summary{
		Total:    at(0),
		DayChg:   at(0) - at(1),
		WeekChg:  at(0) - at(7),
		MonthChg: at(0) - at(30),
		History:  []Point{},
		Coins:    []Coin{},
	}
	start := len(chart) - histDays
	if start < 0 {
		start = 0
	}
	for _, p := range chart[start:] {
		var sec int64
		for _, c := range p.Date {
			if c < '0' || c > '9' {
				sec = 0
				break
			}
			sec = sec*10 + int64(c-'0')
		}
		s.History = append(s.History, Point{T: sec * 1000, Mcap: p.Tot.Pegged})
	}

	// per-coin breakdown (best-effort; the panel still works without it)
	var summ struct {
		Assets []struct {
			Symbol string `json:"symbol"`
			Circ   struct {
				Pegged float64 `json:"peggedUSD"`
			} `json:"circulating"`
			CircPrevDay struct {
				Pegged float64 `json:"peggedUSD"`
			} `json:"circulatingPrevDay"`
			CircPrevWk struct {
				Pegged float64 `json:"peggedUSD"`
			} `json:"circulatingPrevWeek"`
		} `json:"peggedAssets"`
	}
	if err := getJSON("https://stablecoins.llama.fi/stablecoins?includePrices=true", &summ); err == nil {
		for _, a := range summ.Assets {
			if a.Circ.Pegged <= 0 {
				continue
			}
			s.Coins = append(s.Coins, Coin{
				Symbol:  a.Symbol,
				Mcap:    a.Circ.Pegged,
				DayChg:  a.Circ.Pegged - a.CircPrevDay.Pegged,
				WeekChg: a.Circ.Pegged - a.CircPrevWk.Pegged,
			})
		}
		sort.Slice(s.Coins, func(i, j int) bool { return s.Coins[i].Mcap > s.Coins[j].Mcap })
		if len(s.Coins) > nCoins {
			s.Coins = s.Coins[:nCoins]
		}
	}
	return s, nil
}

type serr string

func (e serr) Error() string { return string(e) }

const errEmpty = serr("stablecoin: empty chart")
