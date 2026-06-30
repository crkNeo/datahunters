package cache

import (
	"sync"
	"time"
)

// ttlCache is a small shared cache for the per-request endpoints (home / coin
// detail / klines) so they serve all users from ONE upstream fetch instead of
// hitting the exchange on every request — essential before going public, or the
// machine IP gets rate-limit-banned by Binance.
//
// It provides two guarantees:
//   - singleflight: N concurrent requests for the same key trigger ONE fetch
//     (the rest wait and share the result).
//   - stale-on-error: if the upstream fails (e.g. an IP ban), the last good
//     value is served instead of an error.
type ttlCache struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[string]*ttlItem
}

type ttlItem struct {
	mu   sync.Mutex // per-key lock: serialises fetches for the same key
	data any
	exp  time.Time
	have bool
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, items: map[string]*ttlItem{}}
}

// get returns the cached value for key if fresh, else calls fetch once (other
// callers for the same key block until it returns, then share the result).
func (c *ttlCache) get(key string, fetch func() (any, error)) (any, error) {
	c.mu.Lock()
	it := c.items[key]
	if it == nil {
		it = &ttlItem{}
		c.items[key] = it
	}
	c.mu.Unlock()

	it.mu.Lock()
	defer it.mu.Unlock()
	if it.have && time.Now().Before(it.exp) {
		return it.data, nil
	}
	data, err := fetch()
	if err != nil {
		if it.have {
			return it.data, nil // serve stale rather than fail
		}
		return nil, err
	}
	it.data, it.exp, it.have = data, time.Now().Add(c.ttl), true
	return data, nil
}
