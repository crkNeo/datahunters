package cache

import (
	"math"

	"datahunter/internal/exchange"
)

// indicators.go: shared technical indicators used across strategies.
// (emaSeries lives in paper_ema.go; rsiSeries/smaSeries/stdevSeries in microrev.go.)

// atrSeries is the Wilder ATR over period p, returned as a full-length series
// aligned to cs (zero until the warmup completes at index p).
// Used by 冥王星 (convergence.go) and the mean-reversion books (microrev.go).
func atrSeries(cs []exchange.Candle, p int) []float64 {
	n := len(cs)
	out := make([]float64, n)
	if n < p+1 {
		return out
	}
	tr := func(i int) float64 {
		v := cs[i].High - cs[i].Low
		if d := math.Abs(cs[i].High - cs[i-1].Close); d > v {
			v = d
		}
		if d := math.Abs(cs[i].Low - cs[i-1].Close); d > v {
			v = d
		}
		return v
	}
	var sum float64
	for i := 1; i <= p; i++ {
		sum += tr(i)
	}
	atr := sum / float64(p)
	out[p] = atr
	for i := p + 1; i < n; i++ {
		atr = (atr*float64(p-1) + tr(i)) / float64(p)
		out[i] = atr
	}
	return out
}
