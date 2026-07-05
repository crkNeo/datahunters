// Package push sends Web Push (VAPID) notifications to browser/PWA subscribers.
// VAPID keys are generated once and persisted via the Backend so they survive
// restarts. Subscriptions are stored by the Backend too.
package push

import (
	"encoding/json"
	"io"
	"log"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Backend is the persistence the manager needs (implemented by cache.Store).
type Backend interface {
	GetConfig(k string) string
	SetConfig(k, v string)
	AllSubs() []string      // JSON-encoded webpush.Subscription rows
	DelSub(endpoint string) // prune a dead subscription
}

// Manager holds the VAPID keypair and sends notifications.
type Manager struct {
	mu    sync.RWMutex
	pub   string
	priv  string
	store Backend
}

// New loads (or generates + persists) the VAPID keypair.
func New(store Backend) *Manager {
	m := &Manager{store: store}
	m.pub = store.GetConfig("vapid_pub")
	m.priv = store.GetConfig("vapid_priv")
	if m.pub == "" || m.priv == "" {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			log.Printf("web-push: VAPID generation failed: %v", err)
			return m
		}
		m.priv, m.pub = priv, pub
		store.SetConfig("vapid_pub", pub)
		store.SetConfig("vapid_priv", priv)
		log.Printf("web-push: generated VAPID keys")
	}
	return m
}

// Reset generates a fresh VAPID keypair and persists it. Existing browser
// subscriptions (made with the old public key) become invalid and must be
// recreated — the caller should also clear stored subscriptions.
func (m *Manager) Reset() error {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.priv, m.pub = priv, pub
	m.mu.Unlock()
	m.store.SetConfig("vapid_pub", pub)
	m.store.SetConfig("vapid_priv", priv)
	log.Printf("web-push: VAPID keys reset")
	return nil
}

// PublicKey returns the VAPID public key for the browser to subscribe with.
func (m *Manager) PublicKey() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pub
}

func (m *Manager) enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pub != "" && m.priv != ""
}

// Send delivers a notification to every stored subscription (non-blocking).
func (m *Manager) Send(title, body, url string) {
	if !m.enabled() {
		return
	}
	if url == "" {
		url = "/"
	}
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body, "url": url})
	subs := m.store.AllSubs()
	log.Printf("web-push: sending %q to %d subscriber(s)", title, len(subs))
	for _, raw := range subs {
		var sub webpush.Subscription
		if json.Unmarshal([]byte(raw), &sub) != nil || sub.Endpoint == "" {
			continue
		}
		go m.sendOne(payload, sub)
	}
}

func (m *Manager) sendOne(payload []byte, sub webpush.Subscription) {
	resp, err := webpush.SendNotification(payload, &sub, &webpush.Options{
		// NOTE: pass the bare address — webpush-go prepends "mailto:" itself. Passing
		// "mailto:…" here yields a double "mailto:mailto:…" sub claim, which Apple
		// rejects with 403 BadJwtToken (FCM ignores it, which masked the bug).
		Subscriber:      "admin@jmchcompass.com",
		VAPIDPublicKey:  m.pub,
		VAPIDPrivateKey: m.priv,
		TTL:             60,
	})
	if err != nil {
		log.Printf("web-push: send error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("web-push: push service returned %d: %s", resp.StatusCode, string(body))
	}
	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		m.store.DelSub(sub.Endpoint) // subscription expired/gone → prune
	}
}
