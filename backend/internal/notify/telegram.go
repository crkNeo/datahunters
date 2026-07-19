// Package notify sends outbound alerts (currently Telegram). It is a no-op
// unless TELEGRAM_TOKEN and TELEGRAM_CHAT_ID are set, so the app runs fine
// without any configuration.
package notify

import (
	"net/http"
	"net/url"
	"os"
	"time"
)

// Telegram pushes messages to a chat via the Bot API.
type Telegram struct {
	token  string
	chatID string
	http   *http.Client
}

func NewTelegram() *Telegram {
	return &Telegram{
		token:  os.Getenv("TELEGRAM_TOKEN"),
		chatID: os.Getenv("TELEGRAM_CHAT_ID"),
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether a token + chat id are configured.
// nil-safe:未設定 Telegram 時呼叫端不必先判空(Send 也是靠這個把關)。
func (t *Telegram) Enabled() bool { return t != nil && t.token != "" && t.chatID != "" }

// Send posts a message (HTML parse mode). Safe to call when disabled (no-op).
// Best-effort: errors are swallowed so a failed alert never affects the app.
func (t *Telegram) Send(text string) {
	if !t.Enabled() {
		return
	}
	api := "https://api.telegram.org/bot" + t.token + "/sendMessage"
	resp, err := t.http.PostForm(api, url.Values{
		"chat_id":    {t.chatID},
		"text":       {text},
		"parse_mode": {"HTML"},
	})
	if err != nil {
		return
	}
	resp.Body.Close()
}
