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
	"net/url"
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

// URL is the public notice page for this announcement.
func (n Notice) URL() string {
	return fmt.Sprintf("https://upbit.com/service_center/notice?id=%d", n.ID)
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

// Poll fetches the announcement page once and returns (fresh, all):
//   - fresh: notices newer than the last seen id (oldest→newest), advancing the
//     watermark. The first Poll seeds the baseline and returns no fresh notices,
//     so history is never replayed to Telegram/push on startup.
//   - all: the full current page, newest first, for the on-page board.
func (w *Watcher) Poll() (fresh, all []Notice, err error) {
	notices, err := w.fetch()
	if err != nil {
		return nil, nil, err
	}
	all = make([]Notice, len(notices))
	copy(all, notices)
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })

	if !w.seeded {
		for _, n := range notices {
			if n.ID > w.lastID {
				w.lastID = n.ID
			}
		}
		w.seeded = true
		return nil, all, nil
	}
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
	return fresh, all, nil
}

// TranslateKo turns Korean announcement text into Traditional Chinese via
// Google's keyless translate endpoint (no API key, no third-party dep). It's
// best-effort: on any error it returns the original text so the board still
// shows something readable rather than a blank.
func (w *Watcher) TranslateKo(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	u := "https://translate.googleapis.com/translate_a/single?client=gtx&sl=ko&tl=zh-TW&dt=t&q=" + url.QueryEscape(text)
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
	// Response shape: [[["译文","원문",…],["译文2","원문2",…]], …]. We concatenate
	// the first element of each sentence segment to rebuild the full translation.
	var out []any
	if err := json.Unmarshal(body, &out); err != nil || len(out) == 0 {
		return text
	}
	segs, ok := out[0].([]any)
	if !ok {
		return text
	}
	var b strings.Builder
	for _, s := range segs {
		pair, ok := s.([]any)
		if !ok || len(pair) == 0 {
			continue
		}
		if t, ok := pair[0].(string); ok {
			b.WriteString(t)
		}
	}
	if b.Len() == 0 {
		return text
	}
	return b.String()
}
