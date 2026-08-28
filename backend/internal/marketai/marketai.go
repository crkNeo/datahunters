// Package marketai turns a market-data snapshot into a short zh-TW commentary via
// a free AI. It prefers Groq when GROQ_API_KEY is set — Groq is server-friendly
// (no datacenter-IP block) with a generous free tier, so it works from a VPS.
// Falls back to Google Gemini only when no Groq key is set; note Gemini rejects
// datacenter/VPS IPs, so on a cloud host you want GROQ_API_KEY. Callers must
// tolerate failures.
package marketai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// errGeminiGeoBlocked marks Gemini refusing the request because of WHERE it
// came from, not who sent it. Google rejects many cloud/VPS IP ranges with
// FAILED_PRECONDITION even in countries the API supports, so a valid key with
// plenty of quota still fails every single call from a datacenter host. This is
// exactly why Groq is the preferred backend on a server.
var errGeminiGeoBlocked = errors.New("gemini unavailable from this host's location")

// Client talks to whichever free AI backend is configured (Groq preferred).
type Client struct {
	http      *http.Client
	groqKey   string
	geminiKey string

	mu          sync.RWMutex
	groqModel   string
	geminiModel string
	// geoBlockedUntil suppresses Gemini attempts after a location rejection.
	// Sticky but not permanent: Google re-classifies IP ranges over time, so the
	// block is re-probed rather than latched for the process lifetime.
	geoBlockedUntil time.Time
}

// geoBlockRetry is how long to stay off Gemini before probing it again.
const geoBlockRetry = 6 * time.Hour

func NewClient() *Client {
	gModel := os.Getenv("GEMINI_MODEL")
	if gModel == "" {
		gModel = "gemini-flash-lite-latest"
	}
	qModel := os.Getenv("GROQ_MODEL")
	if qModel == "" {
		// 70B versatile: strong zh-TW, on Groq's free tier. Override with GROQ_MODEL.
		qModel = "llama-3.3-70b-versatile"
	}
	return &Client{
		http:        &http.Client{Timeout: 30 * time.Second},
		groqKey:     os.Getenv("GROQ_API_KEY"),
		groqModel:   qModel,
		geminiKey:   os.Getenv("GEMINI_API_KEY"),
		geminiModel: gModel,
	}
}

// Provider names the backend actually in use, so a status label never claims one
// backend while another is really serving requests.
func (c *Client) Provider() string {
	if c.groqKey != "" {
		return "Groq"
	}
	if c.geminiKey != "" && !c.geoBlocked() {
		return "Gemini"
	}
	return "none"
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
			"set GROQ_API_KEY in .env (works from a VPS); retrying Gemini in %s", geoBlockRetry)
	}
}

// Analyze sends system + user prompts and returns the assistant reply (trimmed).
// Groq is primary when its key is set; Gemini is the no-Groq fallback.
func (c *Client) Analyze(system, user string) (string, error) {
	if c.groqKey != "" {
		return c.groq(system, user)
	}
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
	return "", errors.New("market-AI: no working provider — set GROQ_API_KEY in .env")
}

// ---- Groq (OpenAI-compatible chat completions) ----

// groq tries the configured model, then a smaller fallback on a model error
// (404/400 = decommissioned/unknown model). Stops early on an auth error
// (401/403 = key problem). Remembers the first model that works.
func (c *Client) groq(system, user string) (string, error) {
	tried := map[string]bool{}
	var lastErr error
	for _, m := range c.groqCandidates() {
		if m == "" || tried[m] {
			continue
		}
		tried[m] = true
		text, status, err := c.groqOnce(m, system, user)
		if err == nil {
			c.mu.Lock()
			c.groqModel = m
			c.mu.Unlock()
			return text, nil
		}
		lastErr = err
		if status == 401 || status == 403 {
			break // key/permission — no other model will help
		}
	}
	return "", lastErr
}

func (c *Client) groqCandidates() []string {
	c.mu.RLock()
	current := c.groqModel
	c.mu.RUnlock()
	// Unknown/decommissioned ids just error and fall through to the next.
	return []string{current, "llama-3.3-70b-versatile", "llama-3.1-8b-instant"}
}

func (c *Client) groqOnce(model, system, user string) (string, int, error) {
	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.5,
	})
	req, err := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.groqKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("groq(%s) HTTP %d: %s", model, resp.StatusCode, snippet(body))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &out) != nil {
		return "", 200, fmt.Errorf("groq parse err: %s", snippet(body))
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", 200, fmt.Errorf("groq empty content: %s", snippet(body))
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), 200, nil
}

// ---- Gemini (no-Groq fallback; blocked from datacenter IPs) ----

// gemini tries the configured model, then other free-tier Flash models on a
// per-model failure (404/429 — separate quota buckets). Stops early on an auth
// error (400/403 = key problem). Remembers the first working model.
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
			c.geminiModel = m
			c.mu.Unlock()
			return text, nil
		}
		lastErr = err
		if status == 400 || status == 403 {
			if isGeoBlock(err) { // recoverable elsewhere — its own error
				return "", fmt.Errorf("%w: %v", errGeminiGeoBlocked, err)
			}
			break
		}
	}
	return "", lastErr
}

// isGeoBlock recognises Google refusing the request because of where it came
// from (an ordinary 400, indistinguishable by status from a bad key).
func isGeoBlock(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "location is not supported") ||
		strings.Contains(s, "failed_precondition")
}

func (c *Client) geminiCandidates() []string {
	c.mu.RLock()
	current := c.geminiModel
	c.mu.RUnlock()
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
	if resp.StatusCode != http.StatusOK {
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
