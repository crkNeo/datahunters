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
	{"figure", "🗣 人物", []string{"trump", "musk", "powell", "biden", "yellen", "putin", "xi jinping", "zelensky", "川普", "馬斯克", "鮑爾", "貝森特", "普丁", "拜登"}},
	{"cb", "🏦 央行/利率", []string{"federal reserve", "the fed", "interest rate", "rate cut", "rate hike", "inflation", "fomc", "cpi", "bank of japan", "european central bank", "ecb", "聯準會", "美聯儲", "升息", "降息", "通膨", "利率", "日本央行", "歐洲央行"}},
	{"trade", "📉 貿易/制裁", []string{"tariff", "trade war", "sanction", "embargo", "關稅", "貿易戰", "制裁", "禁運"}},
	{"geo", "⚔️ 地緣/戰爭", []string{"war", "invasion", "missile", "ceasefire", "conflict", "nuclear", "airstrike", "attack", "戰爭", "飛彈", "停火", "衝突", "核武", "空襲", "入侵", "開戰"}},
	{"reg", "⚖️ 監管", []string{"lawsuit", "regulator", "regulation", "cftc", "securities and exchange", "crackdown", "court rules", "sued", "etf approval", "etf denial", "監管", "訴訟", "起訴", "法院", "證交會", "sec ", "核准"}},
	{"hack", "🚨 爆雷/駭客", []string{"hack", "exploit", "breach", "bankruptcy", "insolvency", "depeg", "rug pull", "stolen", "drained", "駭客", "被駭", "漏洞", "破產", "脫鉤", "盜取", "資不抵債", "被盜"}},
	{"inst", "🏛 機構", []string{"blackrock", "microstrategy", "grayscale", "bitcoin etf", "spot etf", "institutional", "fidelity", "ishares", "etf", "貝萊德", "微策略", "灰度", "機構", "富達"}},
	{"crypto", "🪙 加密", []string{"bitcoin", "ethereum", "crypto", "比特幣", "以太坊", "以太幣", "加密", "虛擬貨幣", "幣圈", "穩定幣"}},
}

// cryptoCtx is the extra context required to tag a "whale" headline as 巨鯨 (so a
// literal marine-whale story doesn't get miscategorised).
var cryptoCtx = []string{"bitcoin", "ethereum", "crypto", "wallet", "token", "transfer", "btc", "eth"}

// categorizeNews tags a headline. Every article returned by the GDELT query is
// already on-topic, so a title that doesn't hit a specific keyword falls back to
// 綜合 (general) rather than being dropped — otherwise the feed goes empty, since
// GDELT matches article BODY and many titles don't contain the exact keyword.
func categorizeNews(title string) (category, label string, ok bool) {
	t := strings.ToLower(title)
	if strings.Contains(t, "巨鯨") || strings.Contains(t, "鯨魚") {
		return "whale", "🐋 巨鯨", true
	}
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
	return "misc", "📰 綜合", true
}

// GdeltTick polls GDELT for fresh market-moving headlines, translates the new ones
// to Traditional Chinese (each URL once), and prepends them to the feed. GDELT
// rate-limits to ~1 req/5s, so call this on a slow ticker (every few minutes); a
// rate-limited / non-JSON response just skips the tick.
func (s *Store) GdeltTick() {
	if s.gdeltW == nil {
		return
	}
	arts, err := s.gdeltW.FetchRSS() // crypto RSS: 動區/鏈新聞 (繁中) + Coindesk/Cointelegraph/The Block (英譯) — GDELT was too slow/rate-limited
	if err != nil {
		s.apiFail("新聞快訊(RSS)", err.Error())
		return
	}
	s.apiOK("新聞快訊(RSS)")
	if len(arts) == 0 {
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
		cat, label, _ := categorizeNews(a.Title)
		title, en := a.Title, a.Title
		if a.Zh {
			en = "" // already Traditional Chinese → no English original, no translation
		} else {
			title = s.gdeltW.Translate(a.Title) // network; each URL translated once
		}
		items = append(items, NewsItem{
			Title:    title,
			TitleEN:  en,
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

	// Web Push to ALL users for every new headline (skip the seeding tick). The
	// generic 綜合 bucket shows in the feed but is NOT pushed (avoids noise).
	if seeded {
		for _, it := range items {
			if it.Category == "misc" {
				continue
			}
			s.PushSend(it.Label, it.Title, "/?tab=news")
		}
	}
}

// EtfTick scrapes Farside for the latest daily BTC/ETH spot-ETF net flow and, once
// per new trading day per asset, injects it into the news feed (🏛 機構) + pushes
// it. No free official API exists — Farside HTML is the de-facto public source.
func (s *Store) EtfTick() {
	anyOK := false
	for _, asset := range []string{"BTC", "ETH"} {
		f, err := etf.FetchFlow(asset)
		if err != nil || f.Date == "" {
			continue
		}
		anyOK = true
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
	if anyOK {
		s.apiOK("ETF 淨流入(Farside)")
	} else {
		s.apiFail("ETF 淨流入(Farside)", "Farside 抓取/解析失敗")
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
