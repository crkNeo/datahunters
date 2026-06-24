package cache

import (
	"math"
	"sort"
	"time"

	"datahunter/internal/exchange"
)

// ExpiryIV is one point on the implied-vol term structure.
type ExpiryIV struct {
	Expiry string  `json:"expiry"`
	Days   float64 `json:"days"`
	ATMIV  float64 `json:"atm_iv"`
}

// StrikeOI is open interest concentrated at one strike (an option "wall").
type StrikeOI struct {
	Strike float64 `json:"strike"`
	OI     float64 `json:"oi"`
}

// CoinOptions is the full options-derived dashboard for one major coin.
type CoinOptions struct {
	Coin       string     `json:"coin"`
	Spot       float64    `json:"spot"`
	DVOL       float64    `json:"dvol"`         // forward implied-vol index
	ATMIV      float64    `json:"atm_iv"`       // nearest tradable expiry
	RR25       float64    `json:"rr25"`         // 25Δ risk reversal: callIV - putIV (>0 bullish)
	PCRatioOI  float64    `json:"pc_ratio_oi"`  // put OI / call OI
	PCRatioVol float64    `json:"pc_ratio_vol"` // put vol / call vol
	MaxPain    float64    `json:"max_pain"`     // nearest expiry
	NearExpiry string     `json:"near_expiry"`
	NearDays   float64    `json:"near_days"`
	Term       []ExpiryIV `json:"term"`
	TopCalls   []StrikeOI `json:"top_calls"`
	TopPuts    []StrikeOI `json:"top_puts"`
}

// OptionsData is the /api/options payload.
type OptionsData struct {
	Coins     []CoinOptions `json:"coins"`
	UpdatedAt string        `json:"updated_at"`
	Note      string        `json:"note"`
}

func normCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

// callDelta is the Black-Scholes call delta with r=0.
func callDelta(s, k, tYears, sigma float64) float64 {
	if tYears <= 0 || sigma <= 0 || s <= 0 || k <= 0 {
		return 0
	}
	d1 := (math.Log(s/k) + 0.5*sigma*sigma*tYears) / (sigma * math.Sqrt(tYears))
	return normCDF(d1)
}

// atmIV returns the IV of the strike nearest spot for a set of same-expiry options.
func atmIV(opts []exchange.OptionQuote, spot float64) float64 {
	best, bestDist := 0.0, math.MaxFloat64
	for _, o := range opts {
		if o.MarkIV <= 0 {
			continue
		}
		if d := math.Abs(o.Strike - spot); d < bestDist {
			bestDist, best = d, o.MarkIV
		}
	}
	return round2(best)
}

// maxPain returns the strike that minimises total option payout to holders.
func maxPain(opts []exchange.OptionQuote) float64 {
	strikes := map[float64]bool{}
	for _, o := range opts {
		strikes[o.Strike] = true
	}
	bestK, bestPain := 0.0, math.MaxFloat64
	for k := range strikes {
		var pain float64
		for _, o := range opts {
			if o.IsCall {
				if k > o.Strike {
					pain += (k - o.Strike) * o.OpenInterest
				}
			} else if o.Strike > k {
				pain += (o.Strike - k) * o.OpenInterest
			}
		}
		if pain < bestPain {
			bestPain, bestK = pain, k
		}
	}
	return bestK
}

// riskReversal25 returns IV(25Δ call) - IV(25Δ put) for one expiry (skew sign).
func riskReversal25(opts []exchange.OptionQuote, spot, tYears float64) float64 {
	var cIV, pIV float64
	cBest, pBest := math.MaxFloat64, math.MaxFloat64
	for _, o := range opts {
		if o.MarkIV <= 0 {
			continue
		}
		d := callDelta(spot, o.Strike, tYears, o.MarkIV/100)
		if o.IsCall {
			if dist := math.Abs(d - 0.25); dist < cBest {
				cBest, cIV = dist, o.MarkIV
			}
		} else {
			if dist := math.Abs((d - 1) + 0.25); dist < pBest { // put delta = call delta - 1 ≈ -0.25
				pBest, pIV = dist, o.MarkIV
			}
		}
	}
	if cIV == 0 || pIV == 0 {
		return 0
	}
	return round2(cIV - pIV)
}

// computeCoinOptions builds the dashboard for one currency from its chain.
func computeCoinOptions(coin string, opts []exchange.OptionQuote, dvol float64, now time.Time) CoinOptions {
	co := CoinOptions{Coin: coin, DVOL: round2(dvol), Term: []ExpiryIV{}, TopCalls: []StrikeOI{}, TopPuts: []StrikeOI{}}
	if len(opts) == 0 {
		return co
	}
	co.Spot = opts[0].Underlying

	// put/call ratios across the whole chain
	var callOI, putOI, callVol, putVol float64
	byExpiry := map[int64][]exchange.OptionQuote{}
	for _, o := range opts {
		if o.IsCall {
			callOI += o.OpenInterest
			callVol += o.Volume
		} else {
			putOI += o.OpenInterest
			putVol += o.Volume
		}
		byExpiry[o.ExpiryMs] = append(byExpiry[o.ExpiryMs], o)
	}
	if callOI > 0 {
		co.PCRatioOI = round2(putOI / callOI)
	}
	if callVol > 0 {
		co.PCRatioVol = round2(putVol / callVol)
	}

	// sorted expiries → term structure
	exps := make([]int64, 0, len(byExpiry))
	for e := range byExpiry {
		exps = append(exps, e)
	}
	sort.Slice(exps, func(i, j int) bool { return exps[i] < exps[j] })

	nowMs := now.UnixMilli()
	nearestTradable := int64(-1)
	for _, e := range exps {
		days := float64(e-nowMs) / 86400000
		if days < 0 {
			continue
		}
		iv := atmIV(byExpiry[e], co.Spot)
		if iv <= 0 {
			continue
		}
		co.Term = append(co.Term, ExpiryIV{Expiry: byExpiry[e][0].ExpiryLabel, Days: round2(days), ATMIV: iv})
		if nearestTradable < 0 && days >= 1 { // skip same-day noise
			nearestTradable = e
		}
	}
	if nearestTradable < 0 && len(exps) > 0 {
		nearestTradable = exps[len(exps)-1]
	}
	if nearestTradable > 0 {
		ne := byExpiry[nearestTradable]
		co.NearExpiry = ne[0].ExpiryLabel
		co.NearDays = round2(float64(nearestTradable-nowMs) / 86400000)
		co.ATMIV = atmIV(ne, co.Spot)
		co.MaxPain = maxPain(ne)
		co.RR25 = riskReversal25(ne, co.Spot, math.Max(co.NearDays, 0.5)/365)

		// top OI strikes (walls) for the nearest expiry
		var calls, puts []StrikeOI
		for _, o := range ne {
			if o.OpenInterest <= 0 {
				continue
			}
			if o.IsCall {
				calls = append(calls, StrikeOI{round2(o.Strike), round2(o.OpenInterest)})
			} else {
				puts = append(puts, StrikeOI{round2(o.Strike), round2(o.OpenInterest)})
			}
		}
		sort.Slice(calls, func(i, j int) bool { return calls[i].OI > calls[j].OI })
		sort.Slice(puts, func(i, j int) bool { return puts[i].OI > puts[j].OI })
		co.TopCalls = topStrikes(calls, 5)
		co.TopPuts = topStrikes(puts, 5)
	}
	return co
}

func topStrikes(s []StrikeOI, n int) []StrikeOI {
	if len(s) > n {
		s = s[:n]
	}
	if s == nil {
		return []StrikeOI{}
	}
	return s
}

// Options returns the cached BTC/ETH options dashboard, refreshing if stale.
func (s *Store) Options() OptionsData {
	s.optMu.Lock()
	defer s.optMu.Unlock()
	if time.Since(s.optTime) < 2*time.Minute && len(s.optData.Coins) > 0 {
		return s.optData
	}
	now := time.Now()
	out := OptionsData{
		Coins:     []CoinOptions{},
		UpdatedAt: now.Format(time.RFC3339),
		Note:      "選擇權監控儀表(Deribit 即時)· 非回測訊號,供盤勢判讀參考",
	}
	for _, coin := range []string{"BTC", "ETH"} {
		opts, err := s.ex.DeribitOptions(coin)
		if err != nil || len(opts) == 0 {
			continue
		}
		dvol := 0.0
		if pts, err := s.ex.DeribitDVOL(coin, now.Add(-6*time.Hour).UnixMilli(), now.UnixMilli(), 3600); err == nil && len(pts) > 0 {
			dvol = pts[len(pts)-1].Value
		}
		out.Coins = append(out.Coins, computeCoinOptions(coin, opts, dvol, now))
	}
	if len(out.Coins) > 0 {
		s.optData = out
		s.optTime = now
	}
	return out
}
