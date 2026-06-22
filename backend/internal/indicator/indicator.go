package indicator

import "datahunter/internal/exchange"

// CVD computes a simple cumulative volume delta from aggregated trades.
// Buyer-maker == true means the aggressive side was a seller (sell pressure).
func CVD(trades []exchange.BinanceAggTrade) float64 {
	var delta float64
	for _, t := range trades {
		qty := atof(t.Qty)
		if t.IsBuyerMaker {
			delta -= qty // aggressive sell
		} else {
			delta += qty // aggressive buy
		}
	}
	return delta
}

// CVDFromKlines computes a cumulative-volume-delta ratio (%) over the last
// `window` bars using the taker-buy volume embedded in Binance klines.
// net = takerBuy - takerSell = 2*takerBuy - totalVolume; ratio = net/total*100.
// Positive = net aggressive buying. This is consistent across coins and
// available historically, unlike trade-level CVD over a fixed trade count.
func CVDFromKlines(bars []exchange.Candle, window int) float64 {
	lo := len(bars) - window
	if lo < 0 {
		lo = 0
	}
	var net, tot float64
	for i := lo; i < len(bars); i++ {
		tot += bars[i].Volume
		net += 2*bars[i].TakerBuy - bars[i].Volume
	}
	if tot == 0 {
		return 0
	}
	return net / tot * 100
}

// PctChange returns the percentage change between two values.
func PctChange(from, to float64) float64 {
	if from == 0 {
		return 0
	}
	return (to - from) / from * 100
}

// CandleChange returns the percent change from the open of the first
// (oldest) bar to the close of the last (newest) bar in the slice.
// OKX returns newest-first; Binance returns oldest-first — callers should
// pass bars already ordered oldest..newest.
func CandleChange(bars []exchange.Candle) float64 {
	if len(bars) < 2 {
		return 0
	}
	return PctChange(bars[0].Open, bars[len(bars)-1].Close)
}

// PriceStructure classifies recent market structure from a slice of bars
// ordered oldest..newest. It returns a human label and a direction:
//
//	+1  bullish  (HH/HL — higher highs and higher lows)
//	-1  bearish  (LH/LL — lower highs and lower lows)
//	 0  CHoCH / no clear structure
//
// The label distinguishes a CHoCH (expansion / character change) from a plain
// "no clear structure" so the UI can show the same wording as the reference app.
func PriceStructure(bars []exchange.Candle) (string, int) {
	if len(bars) < 6 {
		return "暫無明確 HH/HL/CHoCH", 0
	}
	mid := len(bars) / 2
	first, second := bars[:mid], bars[mid:]

	fHigh, fLow := first[0].High, first[0].Low
	for _, b := range first {
		if b.High > fHigh {
			fHigh = b.High
		}
		if b.Low < fLow {
			fLow = b.Low
		}
	}
	sHigh, sLow := second[0].High, second[0].Low
	for _, b := range second {
		if b.High > sHigh {
			sHigh = b.High
		}
		if b.Low < sLow {
			sLow = b.Low
		}
	}

	switch {
	case sHigh > fHigh && sLow > fLow:
		return "HH/HL", 1
	case sHigh < fHigh && sLow < fLow:
		return "LH/LL", -1
	case sHigh > fHigh || sLow < fLow:
		// broke one side but not the other → change of character
		return "CHoCH", 0
	default:
		return "暫無明確 HH/HL/CHoCH", 0
	}
}

// CVDRatio expresses CVD as a percentage of total traded volume in the window.
func CVDRatio(trades []exchange.BinanceAggTrade) float64 {
	var total, delta float64
	for _, t := range trades {
		qty := atof(t.Qty)
		total += qty
		if t.IsBuyerMaker {
			delta -= qty
		} else {
			delta += qty
		}
	}
	if total == 0 {
		return 0
	}
	return delta / total * 100
}
