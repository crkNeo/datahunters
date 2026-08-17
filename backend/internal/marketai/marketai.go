// Package marketai turns a market-data snapshot into a short zh-TW commentary via
// a free AI. It prefers Google Gemini when GEMINI_API_KEY is set (stable, free
// tier); otherwise it falls back to Pollinations' keyless endpoint (best-effort —
// a community service that can be slow or down). Callers must tolerate failures.
package marketai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// errGeminiGeoBlocked marks Gemini refusing the request because of WHERE it
// came from, not who sent it. Google rejects many cloud/VPS IP ranges with
// FAILED_PRECONDITION even in countries the API supports, so a valid key with
// plenty of quota still fails every single call from a datacenter host.
//
// It is a 400, but it is emphatically not a key problem, and treating it as one
// is what turned a recoverable condition into a permanently dead feature: the
// model loop broke out, the Pollinations fallback was never reached (it only
// ran when no key was set at all), and the hourly job just failed forever.
var errGeminiGeoBlocked = errors.New("gemini unavailable from this host's location")

// Client talks to whichever free AI backend is configured.
type Client struct {
	http      *http.Client
	geminiKey string

	mu          sync.RWMutex
	geminiModel string
	// geoBlockedUntil suppresses Gemini attempts after a location rejection.
	// Sticky but not permanent: Google re-classifies IP ranges over time, so the
	// block is re-probed rather than latched for the process lifetime.
	geoBlockedUntil time.Time
}

// geoBlockRetry is how long to stay on the fallback before probing Gemini
// again. Long enough that a blocked host is not re-asking every few minutes,
// short enough to pick up a reclassification the same day.
const geoBlockRetry = 6 * time.Hour

func NewClient() *Client {
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		// flash-lite-latest → Gemini 3.1 Flash Lite: 500 RPD free vs 20 RPD on the
		// 2.x/3.x Flash tiers. The hourly market-AI job (24 calls/day) blows past a
		// 20-RPD cap and 429s mid-day, so we default to the high-daily-quota model.
		model = "gemini-flash-lite-latest"
	}
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, geminiKey: os.Getenv("GEMINI_API_KEY"), geminiModel: model}
}

// Provider names the backend actually in use, so a status label never claims
// Gemini while every call is really being served by the fallback.
func (c *Client) Provider() string {
	if c.geminiKey != "" && !c.geoBlocked() {
		return "Gemini"
	}
	return "Pollinations"
}

func (c *Client) geoBlocked() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Now().Before(c.geoBlockedUntil)
}

func (c *Client) markGeoBlocked() {
	c.mu.Lock()
	first := !time.Now().Before(c.geoBlockedUntil)
	c.geoBlockedUntil = time.Now().Add(geoBlockRetry)
	c.mu.Unlock()
	if first {
		log.Printf("market-AI: Gemini refuses this host's location (datacenter IP) — "+
			"falling back to Pollinations, retrying Gemini in %s", geoBlockRetry)
	}
}

// Analyze sends system + user prompts and returns the assistant reply (trimmed).
func (c *Client) Analyze(system, user string) (string, error) {
	if c.geminiKey != "" && !c.geoBlocked() {
		text, err := c.gemini(system, user)
		if err == nil {
			return text, nil
		}
		if !errors.Is(err, errGeminiGeoBlocked) {
			return "", err // a real Gemini failure — surface it, do not mask it
		}
		c.markGeoBlocked()
	}
	return c.pollinations(system, user)
}

// gemini tries the configured model, then falls back through other free-tier Flash
// models on a per-model failure (404 not-found / 429 quota) — different models have
// separate quota buckets. Stops early only on an auth error (400/403 = key problem).
// The first model that works is remembered for subsequent calls.
func (c *Client) gemini(system, user string) (string, error) {
	tried := map[string]bool{}
	var lastErr error
	for _, m := range c.geminiCandidates() {
		if m == "" || tried[m] {
			continue
		}
		tried[m] = true
		text, status, err := c.geminiOnce(m, system, user)
		if err == nil {
			c.mu.Lock()
			c.geminiModel = m // stick with the working model
			c.mu.Unlock()
			return text, nil
		}
		lastErr = err
		if status == 400 || status == 403 {
			// Key, permission or host location — no other model will help. Only
			// the location case is recoverable elsewhere, so it gets its own
			// error and sends the caller to the keyless fallback.
			if isGeoBlock(err) {
				return "", fmt.Errorf("%w: %v", errGeminiGeoBlocked, err)
			}
			break
		}
	}
	return "", lastErr
}

// isGeoBlock recognises Google refusing the request because of where it came
// from. Matched on the response text because the API returns an ordinary 400
// for this, indistinguishable by status from a bad key.
func isGeoBlock(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "location is not supported") ||
		strings.Contains(s, "failed_precondition")
}

func (c *Client) geminiCandidates() []string {
	c.mu.RLock()
	current := c.geminiModel
	c.mu.RUnlock()
	// High-daily-quota (500 RPD) flash-lite aliases first, then the 20-RPD Flash
	// tiers as last-resort. Unknown ids just 404 and fall through harmlessly.
	return []string{current, "gemini-flash-lite-latest", "gemini-3.1-flash-lite", "gemini-2.5-flash-lite", "gemini-2.0-flash", "gemini-2.5-flash", "gemini-flash-latest"}
}

// geminiOnce makes one generateContent call. safetySettings are relaxed so
// financial commentary isn't blocked as "dangerous content".
func (c *Client) geminiOnce(model, system, user string) (string, int, error) {
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
	u := "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":generateContent?key=" + c.geminiKey
	req, err := http.NewRequest("POST", u, bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK { // surface the real error (invalid key / model / quota…)
		return "", resp.StatusCode, fmt.Errorf("gemini(%s) HTTP %d: %s", model, resp.StatusCode, snippet(body))
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
		return "", 200, fmt.Errorf("gemini parse err: %s", snippet(body))
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", 200, fmt.Errorf("gemini no content (safety/empty): %s", snippet(body))
	}
	return strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text), 200, nil
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

