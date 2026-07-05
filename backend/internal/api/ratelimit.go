package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a small fixed-window limiter for abuse-prone endpoints
// (login brute force, register disk-fill). Keys are pruned lazily.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{window: window, limit: limit, hits: map[string][]time.Time{}}
}

// allow records an attempt for key and reports whether it is within the limit.
func (rl *rateLimiter) allow(key string) bool {
	now := time.Now()
	cut := now.Add(-rl.window)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	kept := rl.hits[key][:0]
	for _, t := range rl.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.limit {
		rl.hits[key] = kept
		return false
	}
	rl.hits[key] = append(kept, now)
	// lazy global prune so the map can't grow unbounded
	if len(rl.hits) > 4096 {
		for k, v := range rl.hits {
			alive := false
			for _, t := range v {
				if t.After(cut) {
					alive = true
					break
				}
			}
			if !alive {
				delete(rl.hits, k)
			}
		}
	}
	return true
}

// clientIP extracts the caller's IP. The Go server terminates TLS itself (no
// reverse proxy), so RemoteAddr is the real peer address.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
