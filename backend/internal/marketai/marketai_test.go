package marketai

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// The real payload that took the feature down: a plain 400 that is about the
// host's location, not the key. Misreading it as an auth failure is what kept
// the fallback from ever running.
const geoBody = `gemini(gemini-flash-lite-latest) HTTP 400: {
  "error": {
    "code": 400,
    "message": "User location is not supported for the API use.",
    "status": "FAILED_PRECONDITION"
  }
}`

func TestIsGeoBlock(t *testing.T) {
	blocked := []string{
		geoBody,
		`gemini(x) HTTP 400: {"error":{"status":"FAILED_PRECONDITION"}}`,
		`HTTP 400: user location is not supported`,
	}
	for _, s := range blocked {
		if !isGeoBlock(errors.New(s)) {
			t.Errorf("isGeoBlock(%.60q) = false, want true", s)
		}
	}
	// A genuine key problem must NOT be mistaken for a geo block: silently
	// falling back would hide a misconfiguration the operator needs to fix.
	notBlocked := []string{
		`gemini(x) HTTP 400: {"error":{"message":"API key not valid","status":"INVALID_ARGUMENT"}}`,
		`gemini(x) HTTP 403: {"error":{"message":"PERMISSION_DENIED"}}`,
		`gemini(x) HTTP 429: quota exceeded`,
	}
	for _, s := range notBlocked {
		if isGeoBlock(errors.New(s)) {
			t.Errorf("isGeoBlock(%.60q) = true, want false", s)
		}
	}
}

func TestGeoBlockedErrorIsIdentifiable(t *testing.T) {
	err := fmt.Errorf("%w: %v", errGeminiGeoBlocked, errors.New(geoBody))
	if !errors.Is(err, errGeminiGeoBlocked) {
		t.Fatal("wrapped geo-block error is not detectable with errors.Is")
	}
}

// Provider is a user-facing status label, so it must name the backend actually
// serving requests rather than the one that is merely configured.
func TestProviderReflectsActiveBackend(t *testing.T) {
	c := &Client{geminiKey: "k"}
	if got := c.Provider(); got != "Gemini" {
		t.Errorf("Provider() = %q, want Gemini", got)
	}
	c.markGeoBlocked()
	if got := c.Provider(); got != "none" {
		t.Errorf("after geo block (no Groq key) Provider() = %q, want none", got)
	}
	if !c.geoBlocked() {
		t.Error("geoBlocked() = false right after markGeoBlocked()")
	}
}

func TestGroqPreferredWhenKeySet(t *testing.T) {
	c := &Client{groqKey: "k", geminiKey: "g"}
	if got := c.Provider(); got != "Groq" {
		t.Errorf("Provider() with Groq key = %q, want Groq", got)
	}
}

func TestNoKeyReportsNone(t *testing.T) {
	c := &Client{}
	if got := c.Provider(); got != "none" {
		t.Errorf("Provider() with no key = %q, want none", got)
	}
}

// The block is deliberately not latched for the process lifetime: Google
// re-classifies IP ranges, so a blocked host must probe again rather than stay
// on the fallback until someone restarts the server.
func TestGeoBlockExpires(t *testing.T) {
	c := &Client{geminiKey: "k"}
	c.geoBlockedUntil = time.Now().Add(-time.Second)
	if c.geoBlocked() {
		t.Error("an elapsed block must not still report as blocked")
	}
	if got := c.Provider(); got != "Gemini" {
		t.Errorf("Provider() after expiry = %q, want Gemini", got)
	}
	if geoBlockRetry <= 0 || geoBlockRetry > 24*time.Hour {
		t.Errorf("geoBlockRetry = %v, want a positive sub-day interval", geoBlockRetry)
	}
}
