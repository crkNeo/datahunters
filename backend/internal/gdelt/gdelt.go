// Package gdelt pulls market-moving headlines from GDELT's free DOC 2.0 API (no
// key). One curated query spans the topics that move crypto/macro: key figures
// (Trump / Musk / Powell), central banks & rates, trade/tariffs/sanctions,
// geopolitics/war, and crypto. GDELT rate-limits to ~1 request / 5s, so the
// caller must poll slowly (every few minutes) — well within a low-frequency loop.
package gdelt

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const docURL = "https://api.gdeltproject.org/api/v2/doc/doc"

// query is the OR-of-topics search (English sources only). Keep it moderate so a
// single request stays fast and inside GDELT's rate limit.
// GDELT throttles "larger" queries hard (see its 429), so keep this small: only
// single-word terms, no quoted phrases.
const query = `(bitcoin OR ethereum OR crypto OR Trump OR tariff OR sanctions OR war OR inflation) sourcelang:english`

// Article is one headline (from GDELT or an RSS feed).
type Article struct {
	Title         string
	URL           string
	Domain        string
	SeenDate      string // GDELT "YYYYMMDDTHHMMSSZ" or RSS RFC1123
	SourceCountry string
	Zh            bool // title is already (Traditional) Chinese → no translation needed
}

// Watcher fetches + translates GDELT headlines.
type Watcher struct{ http *http.Client }

// GDELT's DOC API is often slow to respond (heavy backend + rate limiting), so
// give it a generous timeout — a short cap caused "awaiting headers" timeouts and
// an empty feed.
func NewWatcher() *Watcher { return &Watcher{http: &http.Client{Timeout: 45 * time.Second}} }

// Fetch returns recent matching headlines (newest first). Best-effort: on a
// non-JSON body (GDELT rate-limit / error page) it returns an error so the caller
// can simply skip that tick.
func (w *Watcher) Fetch() ([]Article, error) {
	u := docURL + "?query=" + url.QueryEscape(query) +
		"&mode=ArtList&format=json&timespan=60min&sort=DateDesc&maxrecords=20"
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

// ParseTime normalises a GDELT ("YYYYMMDDTHHMMSSZ") or RSS (RFC1123) timestamp to
// RFC3339 ("" on failure).
func ParseTime(s string) string {
	for _, layout := range []string{"20060102T150405Z", time.RFC3339, time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// rssFeeds are reliable crypto news RSS sources (fast, free, no key) — the
// GDELT DOC API proved too slow / rate-limited. zh=true feeds are already
// Traditional Chinese (no translation needed).
var rssFeeds = []struct {
	name, url string
	zh        bool
}{
	{"動區", "https://www.blocktempo.com/feed/", true},
	{"鏈新聞", "https://abmedia.io/feed", true},
	{"Coindesk", "https://www.coindesk.com/arc/outboundfeeds/rss/?outputType=xml", false},
	{"Cointelegraph", "https://cointelegraph.com/rss", false},
	{"The Block", "https://www.theblock.co/rss.xml", false},
}

type rssDoc struct {
	Items []struct {
		Title   string `xml:"title"`
		Link    string `xml:"link"`
		PubDate string `xml:"pubDate"`
	} `xml:"channel>item"`
}

// FetchRSS pulls the crypto RSS feeds and returns their items (newest not
// guaranteed sorted; the caller dedupes by URL). Best-effort per feed.
func (w *Watcher) FetchRSS() ([]Article, error) {
	var arts []Article
	for _, f := range rssFeeds {
		req, err := http.NewRequest("GET", f.url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		resp, err := w.http.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}
		var doc rssDoc
		if xml.Unmarshal(body, &doc) != nil {
			continue
		}
		for _, it := range doc.Items {
			title := strings.TrimSpace(it.Title)
			link := strings.TrimSpace(it.Link)
			if title == "" || link == "" {
				continue
			}
			arts = append(arts, Article{Title: title, URL: link, Domain: f.name, SeenDate: strings.TrimSpace(it.PubDate), Zh: f.zh})
		}
	}
	if len(arts) == 0 {
		return nil, errNotJSON
	}
	return arts, nil
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
