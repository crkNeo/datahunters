package cache

import (
	"sort"
	"strings"
	"time"

	"datahunter/internal/exchange"
)

// stable / wrapped quote-like assets we don't count as "altcoins" for the
// altcoin-season proxy, and don't surface as recommendations.
var stableLike = map[string]bool{
	"USDC": true, "FDUSD": true, "TUSD": true, "BUSD": true, "USDP": true,
	"DAI": true, "USDE": true, "USD1": true,
}

// TickerLite is a price + 24h change pair for the header strip.
type TickerLite struct {
	Price float64 `json:"price"`
	Chg   float64 `json:"chg"`
}

// Rec is one row in the 做多/做空推薦 cards.
type Rec struct {
	Coin     string  `json:"coin"`
	Price    float64 `json:"price"`
	Chg      float64 `json:"chg"`
	Score    int     `json:"score"`
	Strength int     `json:"strength"` // 1..5 bars
	Featured bool    `json:"featured"` // top high-conviction pick
}

// signalCut is the |score| at which the scorer actually calls a coin long/short
// (matches DefaultDetailWeights().BiasThreshold). Only coins past this bar are
// eligible to be recommended, so the cards never disagree with the bias label.
const signalCut = 20

// markFeatured flags up to 3 leading recs as high-conviction "強力推薦".
// Input must already be filtered to real signals and sorted strongest-first.
func markFeatured(recs []Rec) {
	for i := range recs {
		if i >= 3 {
			break
		}
		recs[i].Featured = true
	}
}

// MarketRow is one row in the 合約市場 table.
type MarketRow struct {
	Coin  string  `json:"coin"`
	Price float64 `json:"price"`
	Chg   float64 `json:"chg"`
	Vol   float64 `json:"vol"`
}

// AltSeason is the altcoin-season gauge payload.
type AltSeason struct {
	Value int    `json:"value"` // 0..100
	Label string `json:"label"`
	Prev  int    `json:"prev"` // yesterday's value, 0 if unknown
}

// HomeData is the full landing-page payload.
type HomeData struct {
	Ticker    map[string]TickerLite `json:"ticker"`
	LongRecs  []Rec                 `json:"long_recs"`
	ShortRecs []Rec                 `json:"short_recs"`
	AltSeason AltSeason             `json:"alt_season"`
	Market    []MarketRow           `json:"market"`
	Total     int                   `json:"total"`
}

func coinOf(symbol string) string { return strings.TrimSuffix(symbol, "USDT") }

// strength maps a board score to 1..5 strength bars.
func strength(score int) int {
	s := score
	if s < 0 {
		s = -s
	}
	b := (s + 7) / 8 // ~ceil(s/8)
	if b < 1 {
		b = 1
	}
	if b > 5 {
		b = 5
	}
	return b
}

// Home builds the landing-page payload: it pulls the full contract market in a
// single call, derives the header tickers / altcoin-season gauge from it, and
// ranks recommendations from the cached per-coin scores.
func (s *Store) Home() (HomeData, error) {
	tickers, err := s.ex.BinanceAllTickers()
	if err != nil {
		return HomeData{}, err
	}

	bySym := make(map[string]float64, len(tickers))   // symbol -> change%
	priceBySym := make(map[string]float64, len(tickers))
	for _, t := range tickers {
		bySym[t.Symbol] = t.ChgPct
		priceBySym[t.Symbol] = t.Price
	}

	// header tickers
	hdr := map[string]TickerLite{}
	for _, c := range []string{"BTC", "ETH"} {
		hdr[c] = TickerLite{Price: priceBySym[c+"USDT"], Chg: bySym[c+"USDT"]}
	}

	// market table: USDT perps by 24h volume, top 150
	market := make([]MarketRow, 0, len(tickers))
	for _, t := range tickers {
		market = append(market, MarketRow{
			Coin:  coinOf(t.Symbol),
			Price: t.Price,
			Chg:   t.ChgPct,
			Vol:   t.QuoteVol,
		})
	}
	sort.Slice(market, func(i, j int) bool { return market[i].Vol > market[j].Vol })
	total := len(market)
	if len(market) > 150 {
		market = market[:150]
	}

	// recommendations from cached scores
	data, _ := s.All()
	longs, shorts := []Rec{}, []Rec{}
	for coin, snap := range data {
		if stableLike[coin] {
			continue
		}
		r := Rec{
			Coin:     coin,
			Price:    priceBySym[coin+"USDT"],
			Chg:      bySym[coin+"USDT"],
			Score:    snap.Score,
			Strength: strength(snap.Score),
		}
		if snap.Score >= signalCut {
			longs = append(longs, r)
		} else if snap.Score <= -signalCut {
			shorts = append(shorts, r)
		}
	}
	sort.Slice(longs, func(i, j int) bool { return longs[i].Score > longs[j].Score })
	sort.Slice(shorts, func(i, j int) bool { return shorts[i].Score < shorts[j].Score })
	longs = topN(longs, 5)
	shorts = topN(shorts, 5)
	markFeatured(longs)
	markFeatured(shorts)

	return HomeData{
		Ticker:    hdr,
		LongRecs:  longs,
		ShortRecs: shorts,
		AltSeason: s.altSeason(tickers, bySym["BTCUSDT"]),
		Market:    market,
		Total:     total,
	}, nil
}

func topN(r []Rec, n int) []Rec {
	if len(r) > n {
		return r[:n]
	}
	return r
}

// altSeason is a 24h proxy for the altcoin-season index: among the top liquid
// alts, the share that outperformed BTC over the last 24h. It is NOT the
// official 90-day index — just a fast, self-hosted approximation.
func (s *Store) altSeason(tickers []exchange.MarketTicker, btcChg float64) AltSeason {
	// rank alts by volume, take the top 100 (excluding BTC and stables)
	alts := make([]exchange.MarketTicker, 0, len(tickers))
	for _, t := range tickers {
		c := coinOf(t.Symbol)
		if c == "BTC" || stableLike[c] {
			continue
		}
		alts = append(alts, t)
	}
	sort.Slice(alts, func(i, j int) bool { return alts[i].QuoteVol > alts[j].QuoteVol })
	if len(alts) > 100 {
		alts = alts[:100]
	}

	value := 50
	if len(alts) > 0 {
		out := 0
		for _, t := range alts {
			if t.ChgPct > btcChg {
				out++
			}
		}
		value = int(float64(out) / float64(len(alts)) * 100)
	}

	return AltSeason{
		Value: value,
		Label: altLabel(value),
		Prev:  s.trackAltSeason(value),
	}
}

func altLabel(v int) string {
	switch {
	case v < 25:
		return "BTC 季"
	case v < 45:
		return "偏 BTC"
	case v <= 55:
		return "中性"
	case v <= 75:
		return "偏山寨"
	default:
		return "山寨季"
	}
}

// trackAltSeason records one value per UTC day and returns the previous day's
// value (0 if not yet known). In-memory only; resets on restart.
func (s *Store) trackAltSeason(value int) int {
	s.altMu.Lock()
	defer s.altMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if s.altDate == "" {
		s.altDate, s.altToday = today, value
		return 0
	}
	if today != s.altDate {
		s.altYesterday = s.altToday
		s.altDate, s.altToday = today, value
	} else {
		s.altToday = value
	}
	return s.altYesterday
}
