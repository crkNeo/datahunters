package indicator

import "strconv"

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func atof(s string) float64 {
	return parseFloat(s)
}
