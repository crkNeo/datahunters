package newsfeed

import (
	"regexp"
	"strings"
	"time"
)

// Telegram 公開頻道:t.me/s/<頻道> 是 Telegram 官方的網頁預覽,公開頻道不用登入、不用帳號
// 就能看到最近約 20 則訊息。用它取代 Telethon(不需要拿自己的帳號登入,也沒有封號風險)。

var (
	tgMsgRe  = regexp.MustCompile(`(?s)data-post="([^"]+)".*?(?:<div class="tgme_widget_message_text[^"]*"[^>]*>(.*?)</div>).*?<time[^>]+datetime="([^"]+)"`)
	tgSplitR = regexp.MustCompile(`<div class="tgme_widget_message_wrap`)
)

// Telegram 抓一個公開頻道最近的訊息。lang 是這個頻道的主要語言(en / ko / zh)。
func (c *Client) Telegram(channel, lang string) ([]Item, error) {
	b, err := c.get("https://t.me/s/"+channel, map[string]string{"Accept": "text/html"})
	if err != nil {
		return nil, err
	}
	return parseTelegram(string(b), channel, lang), nil
}

// parseTelegram 解析 t.me/s 頁面:一則訊息一個 tgme_widget_message_wrap,逐段解析,
// 避免某則沒有文字(純圖片)時吃到下一則。
func parseTelegram(html, channel, lang string) []Item {
	var out []Item
	parts := tgSplitR.Split(html, -1)
	for _, p := range parts[1:] {
		m := tgMsgRe.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		text := stripTags(m[2])
		if text == "" {
			continue
		}
		var pub time.Time
		if t, err := time.Parse(time.RFC3339, m[3]); err == nil {
			pub = t.UTC()
		}
		title := text
		if r := []rune(title); len(r) > 280 {
			title = string(r[:280]) + "…"
		}
		out = append(out, Item{Source: "tg:" + channel, ID: m[1], Title: strings.TrimSpace(title), Lang: lang,
			URL: "https://t.me/" + m[1], Published: pub})
	}
	return out
}
