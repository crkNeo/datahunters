package newsfeed

import (
	"encoding/json"
	"fmt"
	neturl "net/url"
	"strconv"
	"strings"
	"time"
)

// ---- GitHub 新版本(專案升級)----

// GitHubRelease 抓某 repo 最近的 release。token 可空(未帶 token 每小時 60 次)。coin = 對應幣種。
func (c *Client) GitHubRelease(repo, coin, token string) ([]Item, error) {
	h := map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	var rs []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"published_at"`
	}
	if err := c.getJSON("https://api.github.com/repos/"+repo+"/releases?per_page=5", h, &rs); err != nil {
		return nil, err
	}
	var out []Item
	for _, r := range rs {
		if r.Draft {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.TagName
		}
		pre := ""
		if r.Prerelease {
			pre = "(預覽版)"
		}
		pub, _ := time.Parse(time.RFC3339, r.PublishedAt)
		out = append(out, Item{Source: "github", ID: fmt.Sprintf("%s#%d", repo, r.ID), Lang: "en",
			Title: fmt.Sprintf("%s 發布新版本 %s%s", repo, name, pre), URL: r.HTMLURL, Published: pub.UTC(),
			Coins: []string{coin}, Category: "dev"})
	}
	return out, nil
}

// ---- Snapshot 治理提案 ----

// Snapshot 抓這些 space 最近的提案。spaces:space id → 幣種。
func (c *Client) Snapshot(spaces map[string]string) ([]Item, error) {
	ids := make([]string, 0, len(spaces))
	for id := range spaces {
		ids = append(ids, strconv.Quote(id))
	}
	gql := `query { proposals(first: 30, skip: 0, where: { space_in: [` + strings.Join(ids, ",") +
		`] }, orderBy: "created", orderDirection: desc) { id title created start end state space { id } } }`
	body, _ := json.Marshal(map[string]string{"query": gql})
	q := string(body)
	b, err := c.do("POST", "https://hub.snapshot.org/graphql", q, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Proposals []struct {
				ID      string `json:"id"`
				Title   string `json:"title"`
				Created int64  `json:"created"`
				State   string `json:"state"`
				Space   struct {
					ID string `json:"id"`
				} `json:"space"`
			} `json:"proposals"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("解析失敗: %v", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("snapshot: %s", resp.Errors[0].Message)
	}
	var out []Item
	for _, p := range resp.Data.Proposals {
		coin := spaces[p.Space.ID]
		out = append(out, Item{Source: "snapshot", ID: p.ID, Lang: "en", Category: "governance",
			Title: fmt.Sprintf("[%s 治理提案] %s", coin, p.Title), URL: "https://snapshot.org/#/" + p.Space.ID + "/proposal/" + p.ID,
			Published: time.Unix(p.Created, 0).UTC(), Coins: []string{coin}})
	}
	return out, nil
}

// ---- SEC EDGAR 申報(加密 ETF / 信託)----

// SEC 用 EDGAR 全文檢索找最近 7 天提到 query 的 S-1 / 8-K / 19b-4 類申報。ua 必須帶聯絡方式
// (SEC 規定),coin = 對應幣種。
func (c *Client) SEC(query, coin, ua string) ([]Item, error) {
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -7)
	url := fmt.Sprintf("https://efts.sec.gov/LATEST/search-index?q=%s&dateRange=custom&startdt=%s&enddt=%s&forms=S-1,S-1/A,8-K,10-K,S-3",
		neturl.QueryEscape(strconv.Quote(query)), start.Format("2006-01-02"), end.Format("2006-01-02"))
	var resp struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source struct {
					DisplayNames []string `json:"display_names"`
					FileDate     string   `json:"file_date"`
					Form         string   `json:"form"`
					ADSH         string   `json:"adsh"`
					CIKs         []string `json:"ciks"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := c.getJSON(url, map[string]string{"User-Agent": ua}, &resp); err != nil {
		return nil, err
	}
	var out []Item
	for _, h := range resp.Hits.Hits {
		src := h.Source
		name := strings.Join(src.DisplayNames, ", ")
		pub, _ := time.Parse("2006-01-02", src.FileDate)
		link := "https://efts.sec.gov/LATEST/search-index?q=" + neturl.QueryEscape(strconv.Quote(query))
		if len(src.CIKs) > 0 && src.ADSH != "" {
			link = fmt.Sprintf("https://www.sec.gov/Archives/edgar/data/%s/%s/", strings.TrimLeft(src.CIKs[0], "0"), strings.ReplaceAll(src.ADSH, "-", ""))
		}
		out = append(out, Item{Source: "sec", ID: h.ID, Lang: "en", Category: "etf",
			Title: fmt.Sprintf("SEC 申報 %s:%s(提到 %s)", src.Form, name, query), URL: link, Published: pub.UTC(), Coins: []string{coin}})
	}
	return out, nil
}
