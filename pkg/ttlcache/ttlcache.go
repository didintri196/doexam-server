// Package ttlcache is a small in-memory map whose entries expire, for short-lived
// secrets that must never be persisted (e.g. one-time credential downloads).
package ttlcache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// Cache is safe for concurrent use. Expired entries are dropped lazily on access.
type Cache[V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]entry[V]
}

// New returns a Cache whose entries live for ttl.
func New[V any](ttl time.Duration) *Cache[V] {
	return &Cache[V]{ttl: ttl, now: time.Now, items: map[string]entry[V]{}}
}

// Put stores v under key, replacing any previous value.
func (c *Cache[V]) Put(key string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	c.items[key] = entry[V]{value: v, expires: c.now().Add(c.ttl)}
}

// Take returns and removes the value under key (one-time read).
func (c *Cache[V]) Take(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	e, ok := c.items[key]
	delete(c.items, key)
	return e.value, ok
}

// Has reports whether an unexpired value exists under key.
func (c *Cache[V]) Has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweep()
	_, ok := c.items[key]
	return ok
}

func (c *Cache[V]) sweep() {
	now := c.now()
	for k, e := range c.items {
		if !now.Before(e.expires) {
			delete(c.items, k)
		}
	}
}
