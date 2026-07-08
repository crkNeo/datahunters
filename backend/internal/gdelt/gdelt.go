// Package gdelt pulls market-moving headlines from GDELT's free DOC 2.0 API (no
// key). One curated query spans the topics that move crypto/macro: key figures
// (Trump / Musk / Powell), central banks & rates, trade/tariffs/sanctions,
// geopolitics/war, and crypto. GDELT rate-limits to ~1 request / 5s, so the
// caller must poll slowly (every few minutes) — well within a low-frequency loop.
package gdelt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const docURL = "https://api.gdeltproject.org/api/v2/doc/doc"

// query is the OR-of-topics search (English sources only). Keep it moderate so a
// single request stays fast and inside GDELT's rate limit.
const query = `(Trump OR "Elon Musk" OR "Federal Reserve" OR "Bank of Japan" OR "interest rate" OR inflation OR tariff OR sanctions OR war OR ceasefire OR bitcoin OR ethereum OR cryptocurrency OR "Bitcoin ETF" OR "spot ETF" OR BlackRock OR MicroStrategy OR Grayscale OR "SEC lawsuit" OR "crypto hack" OR exploit OR bankruptcy) sourcelang:english`

// Article is one GDELT headline.
type Article struct {
	Title         string
	URL           string
	Domain        string
	SeenDate      string // GDELT "YYYYMMDDTHHMMSSZ"
	SourceCountry string
}

// Watcher fetches + translates GDELT headlines.
type Watcher struct{ http *http.Client }

func NewWatcher() *Watcher { return &Watcher{http: &http.Client{Timeout: 15 * time.Second}} }

// Fetch returns recent matching headlines (newest first). Best-effort: on a
// non-JSON body (GDELT rate-limit / error page) it returns an error so the caller
// can simply skip that tick.
func (w *Watcher) Fetch() ([]Article, error) {
	u := docURL + "?query=" + url.QueryEscape(query) +
		"&mode=ArtList&format=json&timespan=60min&sort=DateDesc&maxrecords=50"
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
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
		Articles []struct {
			URL           string `json:"url"`
			Title         string `json:"title"`
			SeenDate      string `json:"seendate"`
			Domain        string `json:"domain"`
			SourceCountry string `json:"sourcecountry"`
		} `json:"articles"`
	}
	if json.Unmarshal(body, &out) != nil {
		return nil, errNotJSON // rate-limited / HTML error page
	}
	arts := make([]Article, 0, len(out.Articles))
	for _, a := range out.Articles {
		arts = append(arts, Article{
			Title: strings.TrimSpace(a.Title), URL: a.URL, Domain: a.Domain,
			SeenDate: a.SeenDate, SourceCountry: a.SourceCountry,
		})
	}
	return arts, nil
}

type gdeltErr string

func (e gdeltErr) Error() string { return string(e) }

const errNotJSON = gdeltErr("gdelt: non-JSON response (rate limited?)")

// ParseTime turns GDELT's "YYYYMMDDTHHMMSSZ" into RFC3339 ("" on failure).
func ParseTime(s string) string {
	t, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Translate renders English headline text into Traditional Chinese via Google's
// keyless endpoint. Best-effort: returns the original text on any error.
func (w *Watcher) Translate(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	u := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=en&tl=zh-TW&dt=t&q=" + url.QueryEscape(text)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return text
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := w.http.Do(req)
	if err != nil {
		return text
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return text
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return text
	}
	var out []any
	if json.Unmarshal(body, &out) != nil || len(out) == 0 {
		return text
	}
	segs, ok := out[0].([]any)
	if !ok {
		return text
	}
	var b strings.Builder
	for _, s := range segs {
		if pair, ok := s.([]any); ok && len(pair) > 0 {
			if t, ok := pair[0].(string); ok {
				b.WriteString(t)
			}
		}
	}
	if b.Len() == 0 {
		return text
	}
	return b.String()
}
