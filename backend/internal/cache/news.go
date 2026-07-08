package cache

import (
	"fmt"
	"strings"
	"time"

	"datahunter/internal/etf"
	"datahunter/internal/gdelt"
)

// news.go: the GDELT market-news feed. It polls a curated multi-topic query, tags
// each headline by category, translates it to Traditional Chinese (each URL once),
// and keeps a rolling recent list for the /api/news page. Display-only (no push).

// NewsItem is one market-moving headline for the feed.
type NewsItem struct {
	Title    string `json:"title"`    // Traditional Chinese
	TitleEN  string `json:"title_en"` // original English
	URL      string `json:"url"`
	Domain   string `json:"domain"`
	Category string `json:"category"` // figure | cb | trade | geo | crypto | market
	Label    string `json:"label"`    // display label (emoji + zh)
	Country  string `json:"country"`
	Time     string `json:"time"` // RFC3339 (article seen time)
}

// newsCats maps title keywords to a category + display label. First match wins,
// so figures (Trump/Musk/Powell) take priority over the topic they speak about.
var newsCats = []struct {
	key, label string
	kw         []string
}{
	{"figure", "🗣 人物", []string{"trump", "musk", "powell", "biden", "yellen", "putin", "xi jinping", "zelensky"}},
	{"cb", "🏦 央行/利率", []string{"federal reserve", "the fed", "interest rate", "rate cut", "rate hike", "inflation", "fomc", "cpi", "bank of japan", "european central bank", "ecb"}},
	{"trade", "📉 貿易/制裁", []string{"tariff", "trade war", "sanction", "embargo"}},
	{"geo", "⚔️ 地緣/戰爭", []string{"war", "invasion", "missile", "ceasefire", "conflict", "nuclear", "airstrike", "attack"}},
	{"reg", "⚖️ 監管", []string{"lawsuit", "regulator", "regulation", "cftc", "securities and exchange", "crackdown", "court rules", "sued", "etf approval", "etf denial"}},
	{"hack", "🚨 爆雷/駭客", []string{"hack", "exploit", "breach", "bankruptcy", "insolvency", "depeg", "rug pull", "stolen", "drained"}},
	{"inst", "🏛 機構", []string{"blackrock", "microstrategy", "grayscale", "bitcoin etf", "spot etf", "institutional", "fidelity", "ishares"}},
	{"crypto", "🪙 加密", []string{"bitcoin", "ethereum", "crypto"}},
}

// cryptoCtx is the extra context required to tag a "whale" headline as 巨鯨 (so a
// literal marine-whale story doesn't get miscategorised).
var cryptoCtx = []string{"bitcoin", "ethereum", "crypto", "wallet", "token", "transfer", "btc", "eth"}

// categorizeNews tags a headline. ok=false means no market-moving category matched
// (generic 市場) → the caller drops it.
func categorizeNews(title string) (category, label string, ok bool) {
	t := strings.ToLower(title)
	if strings.Contains(t, "whale") {
		for _, c := range cryptoCtx {
			if strings.Contains(t, c) {
				return "whale", "🐋 巨鯨", true
			}
		}
	}
	for _, c := range newsCats {
		for _, k := range c.kw {
			if strings.Contains(t, k) {
				return c.key, c.label, true
			}
		}
	}
	return "", "", false
}

// GdeltTick polls GDELT for fresh market-moving headlines, translates the new ones
// to Traditional Chinese (each URL once), and prepends them to the feed. GDELT
// rate-limits to ~1 req/5s, so call this on a slow ticker (every few minutes); a
// rate-limited / non-JSON response just skips the tick.
func (s *Store) GdeltTick() {
	if s.gdeltW == nil {
		return
	}
	arts, err := s.gdeltW.Fetch()
	if err != nil || len(arts) == 0 {
		return
	}
	// pick the URLs we haven't seen (mark them under the lock; translate outside it).
	s.gdeltMu.Lock()
	seeded := s.gdeltSeeded // first successful tick only seeds — no push burst of history
	s.gdeltSeeded = true
	var fresh []gdelt.Article
	for _, a := range arts {
		if a.URL == "" || a.Title == "" || s.gdeltSeen[a.URL] {
			continue
		}
		s.gdeltSeen[a.URL] = true
		fresh = append(fresh, a)
	}
	s.gdeltMu.Unlock()
	if len(fresh) == 0 {
		return
	}

	items := make([]NewsItem, 0, len(fresh))
	for _, a := range fresh {
		cat, label, ok := categorizeNews(a.Title)
		if !ok {
			continue // generic 市場 (no market-moving category) → drop
		}
		items = append(items, NewsItem{
			Title:    s.gdeltW.Translate(a.Title), // network; each URL translated once
			TitleEN:  a.Title,
			URL:      a.URL,
			Domain:   a.Domain,
			Category: cat,
			Label:    label,
			Country:  a.SourceCountry,
			Time:     gdelt.ParseTime(a.SeenDate),
		})
	}
	if len(items) == 0 {
		return
	}

	s.gdeltMu.Lock()
	s.gdeltFeed = append(items, s.gdeltFeed...) // newest first (GDELT DateDesc)
	if len(s.gdeltFeed) > 60 {
		s.gdeltFeed = s.gdeltFeed[:60]
	}
	if len(s.gdeltSeen) > 800 { // prune: keep only URLs still on the board
		keep := make(map[string]bool, len(s.gdeltFeed))
		for _, it := range s.gdeltFeed {
			keep[it.URL] = true
		}
		s.gdeltSeen = keep
	}
	s.gdeltMu.Unlock()

	// Web Push to ALL users for every new categorised headline (skip the seeding
	// tick). No throttle — report as it comes.
	if seeded {
		for _, it := range items {
			s.PushSend(it.Label, it.Title, "/?tab=news")
		}
	}
}

// EtfTick scrapes Farside for the latest daily BTC/ETH spot-ETF net flow and, once
// per new trading day per asset, injects it into the news feed (🏛 機構) + pushes
// it. No free official API exists — Farside HTML is the de-facto public source.
func (s *Store) EtfTick() {
	for _, asset := range []string{"BTC", "ETH"} {
		f, err := etf.FetchFlow(asset)
		if err != nil || f.Date == "" {
			continue
		}
		s.gdeltMu.Lock()
		if s.etfSeen[asset] == f.Date { // already reported this day
			s.gdeltMu.Unlock()
			continue
		}
		firstSeed := s.etfSeen[asset] == ""
		s.etfSeen[asset] = f.Date
		item := etfNewsItem(f)
		s.gdeltFeed = append([]NewsItem{item}, s.gdeltFeed...)
		if len(s.gdeltFeed) > 60 {
			s.gdeltFeed = s.gdeltFeed[:60]
		}
		s.gdeltMu.Unlock()
		if !firstSeed { // don't push the pre-existing latest value on boot
			s.PushSend(item.Label, item.Title, "/?tab=news")
		}
	}
}

// etfNewsItem renders a daily ETF flow as a 🏛 機構 news item.
func etfNewsItem(f etf.Flow) NewsItem {
	amt := f.NetM
	dir, emoji, dirEN := "淨流入", "🟢", "inflow"
	if amt < 0 {
		amt, dir, emoji, dirEN = -amt, "淨流出", "🔴", "outflow"
	}
	return NewsItem{
		Title:    fmt.Sprintf("%s %s 現貨 ETF %s $%.1fM(%s)", emoji, f.Asset, dir, amt, f.Date),
		TitleEN:  fmt.Sprintf("%s spot ETF net %s $%.1fM (%s)", f.Asset, dirEN, amt, f.Date),
		URL:      "https://farside.co.uk/" + strings.ToLower(f.Asset) + "/",
		Domain:   "farside.co.uk",
		Category: "inst",
		Label:    "🏛 機構",
		Country:  "US",
		Time:     time.Now().UTC().Format(time.RFC3339),
	}
}

// News returns the recent market-moving headlines (newest first).
func (s *Store) News() []NewsItem {
	s.gdeltMu.RLock()
	defer s.gdeltMu.RUnlock()
	out := make([]NewsItem, len(s.gdeltFeed))
	copy(out, s.gdeltFeed)
	return out
}
