package scorer

import "math"

// smoothSat: saturating curve. 0 at x=0, ±max as |x|->inf, ±max/2 at x=±half.
// A smooth, signed, monotonic response that replaces hard score buckets.
// Shared by the detail scorer in detail.go.
func smoothSat(x, max, half float64) float64 {
	if half <= 0 {
		return 0
	}
	return max * x / (math.Abs(x) + half)
}

func round(f float64) int { return int(math.Round(f)) }
