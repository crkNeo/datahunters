package cache

import (
	"strings"

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
	{"cb", "🏦 央行/利率", []string{"federal reserve", "interest rate", "rate cut", "rate hike", "inflation", "fomc", "cpi"}},
	{"trade", "📉 貿易/制裁", []string{"tariff", "trade war", "sanction", "embargo"}},
	{"geo", "⚔️ 地緣/戰爭", []string{"war", "invasion", "missile", "ceasefire", "conflict", "nuclear", "airstrike", "attack"}},
	{"crypto", "🪙 加密", []string{"bitcoin", "ethereum", "crypto"}},
}

func categorizeNews(title string) (category, label string) {
	t := strings.ToLower(title)
	for _, c := range newsCats {
		for _, k := range c.kw {
			if strings.Contains(t, k) {
				return c.key, c.label
			}
		}
	}
	return "market", "📰 市場"
}

// newsPushCats are the high-impact categories that trigger an admin Web Push.
// Generic 市場 and frequent 加密 headlines are display-only to avoid spam.
var newsPushCats = map[string]bool{"figure": true, "geo": true, "cb": true, "trade": true}

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
		cat, label := categorizeNews(a.Title)
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

	// admin-only Web Push for high-impact headlines (not the seeding tick, and never
	// generic 市場/加密 — capped per tick so a busy news window can't flood).
	if seeded && s.pushMgr != nil && s.db != nil {
		var hot []NewsItem
		for _, it := range items {
			if newsPushCats[it.Category] {
				hot = append(hot, it)
			}
		}
		if len(hot) > 6 {
			hot = hot[:6]
		}
		if len(hot) > 0 {
			if subs := s.db.adminSubs(); len(subs) > 0 {
				for _, it := range hot {
					s.pushMgr.SendTo(subs, it.Label, it.Title, "/?tab=news")
				}
			}
		}
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
