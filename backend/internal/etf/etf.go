// Package etf scrapes Farside Investors (farside.co.uk) for the latest daily
// spot-ETF net flow — there is no free official API, and Farside is the de-facto
// public source (an HTML table). Best-effort: on any fetch/parse failure the
// caller simply keeps the previous value.
package etf

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	dateRe = regexp.MustCompile(`\d{2} [A-Z][a-z]{2} \d{4}`)
	tagRe  = regexp.MustCompile(`<[^>]*>`)
	numRe  = regexp.MustCompile(`\(?-?[\d,]+\.\d+\)?`) // "54.8", "(24.9)", "1,234.5"
)

var client = &http.Client{Timeout: 15 * time.Second}

// Flow is one asset's most recent daily net ETF flow.
type Flow struct {
	Asset string  // BTC | ETH
	Date  string  // "07 Jul 2026"
	NetM  float64 // daily net flow in US$m (negative = net outflow)
}

// FetchFlow scrapes the latest daily net flow for asset ("BTC" or "ETH").
func FetchFlow(asset string) (Flow, error) {
	s, err := FetchSeries(asset, 1)
	if err != nil {
		return Flow{}, err
	}
	return s[0], nil
}

// FetchSeries scrapes the last n daily net flows for asset ("BTC" or "ETH"),
// newest first. Days that haven't settled yet (no number) are skipped, so a
// pending freshest row doesn't leave a hole. Used by the ETF panel + AI context.
func FetchSeries(asset string, n int) ([]Flow, error) {
	u := "https://farside.co.uk/btc/"
	if asset == "ETH" {
		u = "https://farside.co.uk/eth/"
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	html := strings.ReplaceAll(string(body), "\n", " ")
	locs := dateRe.FindAllStringIndex(html, -1)
	if len(locs) == 0 {
		return nil, errParse
	}
	// Each date row ends with a "Total" (cumulative) column; the last decimal value
	// BEFORE that "Total" is the day's net total. Walk newest→oldest, collecting up
	// to n rows that yield a number (skip pending/holiday rows with none).
	out := make([]Flow, 0, n)
	for i := len(locs) - 1; i >= 0 && len(out) < n; i-- {
		start := locs[i][1]
		end := len(html)
		if i+1 < len(locs) {
			end = locs[i+1][0] // bound to this row (up to the next date)
		}
		rest := html[start:end]
		if idx := strings.Index(rest, "Total"); idx >= 0 {
			rest = rest[:idx]
		}
		nums := numRe.FindAllString(tagRe.ReplaceAllString(rest, " "), -1)
		if len(nums) == 0 {
			continue
		}
		net := parseNum(nums[len(nums)-1])
		// Farside renders the current (not-yet-settled) day as a 0.0 total. Multi-
		// billion-AUM BTC/ETH ETFs practically never net exactly zero, so a 0.0 is
		// the pending placeholder — skip it so "latest" is the last SETTLED day
		// instead of a misleading 0 (this was the "常看到 0" report).
		if net == 0 {
			continue
		}
		out = append(out, Flow{Asset: asset, Date: html[locs[i][0]:locs[i][1]], NetM: net})
	}
	if len(out) == 0 {
		return nil, errParse
	}
	return out, nil
}

// parseNum turns a Farside cell ("1,234.5" / "(24.9)") into a float (()=negative).
func parseNum(s string) float64 {
	neg := strings.HasPrefix(s, "(")
	s = strings.NewReplacer("(", "", ")", "", ",", "").Replace(s)
	v, _ := strconv.ParseFloat(s, 64)
	if neg {
		v = -v
	}
	return v
}

type perr string

func (e perr) Error() string { return string(e) }

const errParse = perr("etf: could not parse Farside table")
