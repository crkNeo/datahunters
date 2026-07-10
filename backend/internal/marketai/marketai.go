// Package marketai calls a free, keyless AI endpoint (Pollinations' OpenAI-
// compatible API) to turn a market-data snapshot into a short zh-TW commentary.
// Best-effort: it's a community service, so callers must tolerate failures.
package marketai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const pollURL = "https://text.pollinations.ai/openai"

// Client talks to the free AI endpoint.
type Client struct{ http *http.Client }

func NewClient() *Client { return &Client{http: &http.Client{Timeout: 45 * time.Second}} }

// Analyze sends system + user prompts and returns the assistant reply (trimmed).
func (c *Client) Analyze(system, user string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"model": "openai",
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.5,
		"private":     true, // keep it out of Pollinations' public feed
	})
	req, err := http.NewRequest("POST", pollURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errBad
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Choices) == 0 {
		return "", errBad
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", errBad
	}
	return text, nil
}

type aiErr string

func (e aiErr) Error() string { return string(e) }

const errBad = aiErr("marketai: bad/empty AI response")
