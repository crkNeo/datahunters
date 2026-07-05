// Package upbit watches Upbit's public announcement API and surfaces newly
// posted notices — new-listing ("거래지원") notices move markets. No third-party
// deps; it hits only the public announcement endpoint.
package upbit

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const apiURL = "https://api-manager.upbit.com/api/v1/announcements?os=web&page=1&per_page=20&category=all"

// Notice is one Upbit announcement.
type Notice struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	ListedAt string `json:"listed_at"`
}

// listingHints flag trading-support / new-listing notices by title keyword.
var listingHints = []string{"거래지원", "신규 거래", "마켓 추가", "market support"}

// IsListing reports whether the notice looks like a trading-support/listing one.
func (n Notice) IsListing() bool {
	if n.Category == "거래" {
		return true
	}
	t := strings.ToLower(n.Title)
	for _, h := range listingHints {
		if strings.Contains(t, strings.ToLower(h)) {
			return true
		}
	}
	return false
}

// TelegramText renders the notice as an HTML Telegram message (title escaped).
func (n Notice) TelegramText() string {
	tag := "📢 <b>[Upbit公告]</b>"
	if n.IsListing() {
		tag = "🚀 <b>[Upbit上架]</b>"
	}
	return fmt.Sprintf("%s\n%s\n分類 %s · %s\nhttps://upbit.com/service_center/notice?id=%d",
		tag, html.EscapeString(n.Title), html.EscapeString(n.Category), n.ListedAt, n.ID)
}

// Watcher polls the announcement API and reports notices newer than the last
// seen id. The first Fresh() call only seeds the baseline (returns nil), so it
// never replays history on startup.
type Watcher struct {
	http   *http.Client
	lastID int
	seeded bool
}

func NewWatcher() *Watcher {
	return &Watcher{http: &http.Client{Timeout: 10 * time.Second}}
}

func (w *Watcher) fetch() ([]Notice, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")
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
		Success bool `json:"success"`
		Data    struct {
			Notices []Notice `json:"notices"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Data.Notices, nil
}

// Fresh returns notices newer than the last seen id (oldest→newest), advancing
// the watermark. The first call seeds the baseline and returns nil.
func (w *Watcher) Fresh() ([]Notice, error) {
	notices, err := w.fetch()
	if err != nil {
		return nil, err
	}
	if !w.seeded {
		for _, n := range notices {
			if n.ID > w.lastID {
				w.lastID = n.ID
			}
		}
		w.seeded = true
		return nil, nil
	}
	var fresh []Notice
	for _, n := range notices {
		if n.ID > w.lastID {
			fresh = append(fresh, n)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].ID < fresh[j].ID })
	for _, n := range fresh {
		if n.ID > w.lastID {
			w.lastID = n.ID
		}
	}
	return fresh, nil
}
