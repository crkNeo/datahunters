package newsfeed

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ---- 幣安公告 ----

// binanceCatalogs:48 = 新幣上架、161 = 下架、49 = 最新消息(含上合約、監控標籤等)。
var binanceCatalogs = []int{48, 161, 49}

// Binance 抓幣安公告(公開 CMS 接口,非官方文件)。
func (c *Client) Binance() ([]Item, error) {
	var out []Item
	var lastErr error
	seen := map[string]bool{}
	for _, cat := range binanceCatalogs {
		url := fmt.Sprintf("https://www.binance.com/bapi/composite/v1/public/cms/article/catalog/list/query?catalogId=%d&pageNo=1&pageSize=20", cat)
		b, err := c.get(url, map[string]string{"clienttype": "web", "lang": "en"})
		if err != nil {
			lastErr = err
			continue
		}
		items, err := parseBinance(b, seen)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, items...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// parseBinance:回應外層結構有兩種版本(data.articles / data.catalogs[].articles),這裡用通用
// 走訪找出「有 title + code + releaseDate」的物件,不綁死欄位層級。
func parseBinance(b []byte, seen map[string]bool) ([]Item, error) {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("解析失敗: %v", err)
	}
	var out []Item
	walkObjects(raw, func(m map[string]any) {
		title, code := str(m, "title"), str(m, "code")
		if title == "" || code == "" || seen[code] {
			return
		}
		if _, ok := m["releaseDate"]; !ok {
			return
		}
		seen[code] = true
		out = append(out, Item{Source: "binance", ID: code, Title: title, Lang: "en",
			URL: "https://www.binance.com/en/support/announcement/" + code, Published: msTime(m, "releaseDate")})
	})
	return out, nil
}

// ---- OKX 公告 ----

var okxAnnTypes = []string{"announcements-new-listings", "announcements-delistings"}

// OKX 抓 OKX 公告(v5 公開接口 /api/v5/support/announcements)。
func (c *Client) OKX() ([]Item, error) {
	var out []Item
	var lastErr error
	for _, t := range okxAnnTypes {
		b, err := c.get("https://www.okx.com/api/v5/support/announcements?annType="+t, nil)
		if err != nil {
			lastErr = err
			continue
		}
		items, err := parseOKX(b)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, items...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func parseOKX(b []byte) ([]Item, error) {
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Details []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				PTime string `json:"pTime"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("解析失敗: %v", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("OKX code %s %s", resp.Code, resp.Msg)
	}
	var out []Item
	for _, d := range resp.Data {
		for _, a := range d.Details {
			if a.Title == "" {
				continue
			}
			id := a.URL
			if id == "" {
				id = a.Title
			}
			out = append(out, Item{Source: "okx", ID: id, Title: a.Title, URL: a.URL, Lang: "en",
				Published: msTime(map[string]any{"t": a.PTime}, "t")})
		}
	}
	return out, nil
}

// ---- Bybit 公告 ----

var bybitTypes = []string{"new_crypto", "delistings"}

// Bybit 抓 Bybit 公告(v5 公開接口 /v5/announcements/index)。
func (c *Client) Bybit() ([]Item, error) {
	var out []Item
	var lastErr error
	for _, t := range bybitTypes {
		b, err := c.get("https://api.bybit.com/v5/announcements/index?locale=en-US&limit=20&type="+t, nil)
		if err != nil {
			lastErr = err
			continue
		}
		items, err := parseBybit(b)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, items...)
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func parseBybit(b []byte) ([]Item, error) {
	var resp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				Title         string `json:"title"`
				URL           string `json:"url"`
				DateTimestamp int64  `json:"dateTimestamp"`
				PublishTime   int64  `json:"publishTime"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("解析失敗: %v", err)
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("Bybit retCode %d %s", resp.RetCode, resp.RetMsg)
	}
	var out []Item
	for _, a := range resp.Result.List {
		if a.Title == "" {
			continue
		}
		ts := a.PublishTime
		if ts == 0 {
			ts = a.DateTimestamp
		}
		var pub time.Time
		if ts > 0 {
			pub = time.UnixMilli(ts).UTC()
		}
		id := a.URL
		if id == "" {
			id = a.Title
		}
		out = append(out, Item{Source: "bybit", ID: id, Title: a.Title, URL: a.URL, Lang: "en", Published: pub})
	}
	return out, nil
}

// ---- Bithumb 公告(韓文)----

var (
	nextDataRe   = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__"[^>]*>(.*?)</script>`)
	bithumbAnchr = regexp.MustCompile(`(?s)<a[^>]+href="(/notice/(\d+))"[^>]*>(.*?)</a>`)
)

// Bithumb 抓 Bithumb 公告。優先走官方公開 API(api.bithumb.com/v1/notices);公告網頁
// feed.bithumb.com 有 Cloudflare 防爬,伺服器直接抓常回 403,只當備援。
func (c *Client) Bithumb() ([]Item, error) {
	b, err := c.get("https://api.bithumb.com/v1/notices?count=20", map[string]string{"Accept": "application/json"})
	if err == nil {
		items, perr := parseBithumbAPI(b)
		if perr == nil {
			return items, nil
		}
		err = perr
	}
	page, perr := c.get("https://feed.bithumb.com/notice", map[string]string{"Accept": "text/html", "Accept-Language": "ko-KR,ko;q=0.9"})
	if perr != nil {
		return nil, fmt.Errorf("API:%v;網頁:%v", err, perr)
	}
	return parseBithumb(string(page))
}

var trailingNum = regexp.MustCompile(`(\d+)/?$`)

// parseBithumbAPI 解析官方公告 API:每則有 title + pc_url(+ published_at)。外層包裝不固定,用通用走訪。
func parseBithumbAPI(b []byte) ([]Item, error) {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("解析失敗: %v", err)
	}
	var out []Item
	seen := map[string]bool{}
	walkObjects(raw, func(o map[string]any) {
		title := str(o, "title")
		link := str(o, "pc_url")
		if link == "" {
			link = str(o, "url")
		}
		if title == "" || link == "" {
			return
		}
		id := link
		if m := trailingNum.FindStringSubmatch(link); m != nil {
			id = m[1]
		}
		if seen[id] {
			return
		}
		seen[id] = true
		var pub time.Time
		for _, k := range []string{"published_at", "publishedAt", "created_at", "modified_at"} {
			if v := str(o, k); v != "" {
				if t, err := parseLooseTime(v); err == nil {
					pub = t
					break
				}
			}
		}
		out = append(out, Item{Source: "bithumb", ID: id, Title: title, Lang: "ko", URL: link, Published: pub})
	})
	if len(out) == 0 {
		return nil, fmt.Errorf("API 回應裡沒有公告")
	}
	return out, nil
}

// parseBithumb:新版頁面是 Next.js,先試頁面內嵌的 __NEXT_DATA__ JSON(找有 id + title 的物件),
// 失敗再退回比對 <a href="/notice/123">標題</a>。
func parseBithumb(html string) ([]Item, error) {
	var out []Item
	seen := map[string]bool{}
	if m := nextDataRe.FindStringSubmatch(html); m != nil {
		var raw any
		if json.Unmarshal([]byte(m[1]), &raw) == nil {
			walkObjects(raw, func(o map[string]any) {
				id, title := str(o, "id"), str(o, "title")
				if id == "" || title == "" || seen[id] {
					return
				}
				seen[id] = true
				var pub time.Time
				for _, k := range []string{"publicationDateTime", "createdAt", "publishedAt", "date"} {
					if s := str(o, k); s != "" {
						if t, err := parseLooseTime(s); err == nil {
							pub = t
							break
						}
					}
				}
				out = append(out, Item{Source: "bithumb", ID: id, Title: title, Lang: "ko",
					URL: "https://feed.bithumb.com/notice/" + id, Published: pub})
			})
		}
	}
	if len(out) == 0 {
		for _, m := range bithumbAnchr.FindAllStringSubmatch(html, -1) {
			id, title := m[2], stripTags(m[3])
			if title == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, Item{Source: "bithumb", ID: id, Title: title, Lang: "ko", URL: "https://feed.bithumb.com" + m[1]})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("頁面解析不到公告(格式可能變了)")
	}
	return out, nil
}

// parseLooseTime 嘗試幾種常見時間格式;沒帶時區的視為韓國時間(KST,+09:00)。
func parseLooseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	kst := time.FixedZone("KST", 9*3600)
	for _, f := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006.01.02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, kst); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unknown time %q", s)
}
