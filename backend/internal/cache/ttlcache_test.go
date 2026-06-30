package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 50 concurrent callers for the same key must trigger exactly ONE fetch.
func TestTTLCacheSingleflight(t *testing.T) {
	c := newTTLCache(time.Minute)
	var calls int32
	fetch := func() (any, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(30 * time.Millisecond) // simulate a slow upstream
		return 42, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.get("k", fetch)
			if err != nil || v.(int) != 42 {
				t.Errorf("got %v, %v", v, err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("expected 1 upstream fetch, got %d", calls)
	}
}

// After the TTL expires, the next call refetches; within it, it does not.
func TestTTLCacheExpiry(t *testing.T) {
	c := newTTLCache(40 * time.Millisecond)
	var calls int32
	fetch := func() (any, error) { atomic.AddInt32(&calls, 1); return 1, nil }
	c.get("k", fetch)
	c.get("k", fetch) // still fresh → no refetch
	if calls != 1 {
		t.Fatalf("within TTL expected 1, got %d", calls)
	}
	time.Sleep(60 * time.Millisecond)
	c.get("k", fetch) // expired → refetch
	if calls != 2 {
		t.Fatalf("after TTL expected 2, got %d", calls)
	}
}

// On upstream error, the last good value is served instead of failing.
func TestTTLCacheStaleOnError(t *testing.T) {
	c := newTTLCache(time.Millisecond)
	c.get("k", func() (any, error) { return "good", nil })
	time.Sleep(2 * time.Millisecond) // expire
	v, err := c.get("k", func() (any, error) { return nil, errors.New("banned") })
	if err != nil || v.(string) != "good" {
		t.Fatalf("expected stale 'good', got %v, %v", v, err)
	}
}
