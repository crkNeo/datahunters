package exchange

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// CalEvent is one raw economic-calendar entry from the faireconomy feed.
type CalEvent struct {
	Title    string `json:"title"`
	Country  string `json:"country"`
	Date     string `json:"date"` // RFC3339, e.g. 2026-06-21T21:00:00-04:00
	Impact   string `json:"impact"`
	Forecast string `json:"forecast"`
	Previous string `json:"previous"`
	Actual   string `json:"actual"`
}

// ForexFactoryWeek fetches a week of the public faireconomy economic calendar.
// which = "thisweek" | "nextweek" | "lastweek". Free, no key (needs a UA).
func (c *Client) ForexFactoryWeek(which string) ([]CalEvent, error) {
	url := fmt.Sprintf("https://nfs.faireconomy.media/ff_calendar_%s.json", which)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("faireconomy status %d", resp.StatusCode)
	}
	var out []CalEvent
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
