// Package bytecache stores immutable byte values by key: an in-memory LRU bounded by
// size (the default) or Redis (shared between nodes). Callers treat it as a cache only:
// a miss or an error means "read from the source of truth".
package bytecache

import (
	"container/list"
	"context"
	"sync"
)

// Store is a batch key/value cache. GetMulti returns only the keys it found.
type Store interface {
	GetMulti(ctx context.Context, keys []string) (map[string][]byte, error)
	SetMulti(ctx context.Context, items map[string][]byte) error
}

// entryOverhead approximates the bookkeeping of one entry (map slot, list element, key header).
const entryOverhead = 96

type memEntry struct {
	key   string
	value []byte
}

// Memory is an LRU cache bounded by the total size of its keys and values. It is safe
// for concurrent use. Values are stored and returned as is: callers must not modify them.
type Memory struct {
	mu       sync.Mutex
	maxBytes int
	size     int
	order    *list.List // front = most recently used
	items    map[string]*list.Element
}

var _ Store = (*Memory)(nil)

// NewMemory returns an LRU holding at most maxBytes (values larger than that are skipped).
func NewMemory(maxBytes int) *Memory {
	return &Memory{maxBytes: maxBytes, order: list.New(), items: map[string]*list.Element{}}
}

func cost(key string, value []byte) int { return len(key) + len(value) + entryOverhead }

// GetMulti returns the cached values of keys and marks them recently used.
func (m *Memory) GetMulti(_ context.Context, keys []string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		if el, ok := m.items[k]; ok {
			m.order.MoveToFront(el)
			out[k] = el.Value.(*memEntry).value
		}
	}
	return out, nil
}

// SetMulti stores items, evicting the least recently used entries to stay within the bound.
func (m *Memory) SetMulti(_ context.Context, items map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range items {
		c := cost(k, v)
		if c > m.maxBytes {
			continue
		}
		if el, ok := m.items[k]; ok {
			e := el.Value.(*memEntry)
			m.size += c - cost(e.key, e.value)
			e.value = v
			m.order.MoveToFront(el)
		} else {
			m.items[k] = m.order.PushFront(&memEntry{key: k, value: v})
			m.size += c
		}
		for m.size > m.maxBytes {
			el := m.order.Back()
			e := el.Value.(*memEntry)
			m.order.Remove(el)
			delete(m.items, e.key)
			m.size -= cost(e.key, e.value)
		}
	}
	return nil
}

// Len reports the number of entries (for tests and metrics).
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}
