// Package marketai turns a market-data snapshot into a short zh-TW commentary via
// a free AI. It prefers Google Gemini when GEMINI_API_KEY is set (stable, free
// tier); otherwise it falls back to Pollinations' keyless endpoint (best-effort —
// a community service that can be slow or down). Callers must tolerate failures.
package marketai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client talks to whichever free AI backend is configured.
type Client struct {
	http        *http.Client
	geminiKey   string
	geminiModel string
}

func NewClient() *Client {
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.0-flash"
	}
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, geminiKey: os.Getenv("GEMINI_API_KEY"), geminiModel: model}
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

// gemini calls Google's free-tier Gemini Flash. safetySettings are relaxed so
// financial commentary isn't blocked as "dangerous content".
func (c *Client) gemini(system, user string) (string, error) {
	relax := func(cat string) map[string]string { return map[string]string{"category": cat, "threshold": "BLOCK_NONE"} }
	payload, _ := json.Marshal(map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]string{{"text": system}}},
		"contents":           []map[string]any{{"parts": []map[string]string{{"text": user}}}},
		"generationConfig":   map[string]any{"temperature": 0.5},
		"safetySettings": []map[string]string{
			relax("HARM_CATEGORY_HARASSMENT"), relax("HARM_CATEGORY_HATE_SPEECH"),
			relax("HARM_CATEGORY_SEXUALLY_EXPLICIT"), relax("HARM_CATEGORY_DANGEROUS_CONTENT"),
		},
	})
	u := "https://generativelanguage.googleapis.com/v1beta/models/" + c.geminiModel + ":generateContent?key=" + c.geminiKey
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
	if resp.StatusCode != http.StatusOK { // surface the real error (invalid key / model / quota…)
		return "", fmt.Errorf("gemini(%s) HTTP %d: %s", c.geminiModel, resp.StatusCode, snippet(body))
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
	if json.Unmarshal(body, &out) != nil {
		return "", fmt.Errorf("gemini parse err: %s", snippet(body))
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini no content (safety/empty): %s", snippet(body))
	}
	return strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text), nil
}

// snippet returns a trimmed, length-capped view of a response body for error logs.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		s = s[:240]
	}
	return s
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
		return "", fmt.Errorf("pollinations HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	text := strings.TrimSpace(string(body))
	if text == "" || strings.HasPrefix(text, "{\"error") {
		return "", fmt.Errorf("pollinations bad body: %s", snippet(body))
	}
	return text, nil
}

