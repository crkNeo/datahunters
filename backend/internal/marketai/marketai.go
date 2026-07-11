// Package marketai turns a market-data snapshot into a short zh-TW commentary via
// a free AI. It prefers Google Gemini when GEMINI_API_KEY is set (stable, free
// tier); otherwise it falls back to Pollinations' keyless endpoint (best-effort —
// a community service that can be slow or down). Callers must tolerate failures.
package marketai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client talks to whichever free AI backend is configured.
type Client struct {
	http      *http.Client
	geminiKey string
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, geminiKey: os.Getenv("GEMINI_API_KEY")}
}

// Provider names the active backend (for status labels).
func (c *Client) Provider() string {
	if c.geminiKey != "" {
		return "Gemini"
	}
	return "Pollinations"
}

// Analyze sends system + user prompts and returns the assistant reply (trimmed).
func (c *Client) Analyze(system, user string) (string, error) {
	if c.geminiKey != "" {
		return c.gemini(system, user)
	}
	return c.pollinations(system, user)
}

// gemini calls Google's free-tier Gemini Flash.
func (c *Client) gemini(system, user string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]string{{"text": system}}},
		"contents":           []map[string]any{{"parts": []map[string]string{{"text": user}}}},
		"generationConfig":   map[string]any{"temperature": 0.5},
	})
	u := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=" + c.geminiKey
	req, err := http.NewRequest("POST", u, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", errBad
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", errBad
	}
	return strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text), nil
}

// pollinations calls the keyless legacy GET endpoint (the POST /openai one is
// deprecated and has been returning 5xx). Returns plain text.
func (c *Client) pollinations(system, user string) (string, error) {
	u := "https://text.pollinations.ai/" + url.PathEscape(user) +
		"?model=openai&private=true&system=" + url.QueryEscape(system)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", errBad
	}
	text := strings.TrimSpace(string(body))
	if text == "" || strings.HasPrefix(text, "{\"error") {
		return "", errBad // JSON error envelope, not a completion
	}
	return text, nil
}

type aiErr string

func (e aiErr) Error() string { return string(e) }

const errBad = aiErr("marketai: bad/empty AI response")
