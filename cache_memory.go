package orm

import (
	"context"
	"sync"
	"time"
)

// MemoryCache is an in-memory LRU cache layer.
type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	maxSize int
}

type cacheEntry struct {
	data      []byte
	expiresAt time.Time
}

var _ CacheLayer = (*MemoryCache)(nil)

// NewMemoryCache creates a new in-memory cache with the given maximum number of entries.
func NewMemoryCache(maxSize int) *MemoryCache {
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &MemoryCache{
		entries: make(map[string]*cacheEntry, maxSize),
		maxSize: maxSize,
	}
}

func (c *MemoryCache) get(key string) ([]byte, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return nil, false
	}

	return entry.data, true
}

func (c *MemoryCache) set(key string, data []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple eviction: if at capacity, drop ~10% of entries
	if len(c.entries) >= c.maxSize {
		count := 0
		target := c.maxSize / 10
		for k := range c.entries {
			delete(c.entries, k)
			count++
			if count >= target {
				break
			}
		}
	}

	c.entries[key] = &cacheEntry{
		data:      data,
		expiresAt: time.Now().Add(ttl),
	}
}

func (c *MemoryCache) del(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
}

func (c *MemoryCache) GetEntity(_ context.Context, kind, id string) ([]byte, bool, error) {
	data, ok := c.get(EntityCacheKey("", kind, id))
	return data, ok, nil
}

func (c *MemoryCache) SetEntity(_ context.Context, kind, id string, data []byte, ttl time.Duration) error {
	c.set(EntityCacheKey("", kind, id), data, ttl)
	return nil
}

func (c *MemoryCache) InvalidateEntity(_ context.Context, kind, id string) error {
	c.mu.Lock()
	delete(c.entries, EntityCacheKey("", kind, id))
	c.mu.Unlock()

	// Also invalidate kind queries
	c.del(QueryCacheKey("", kind, ""))
	return nil
}

func (c *MemoryCache) InvalidateKind(_ context.Context, kind string) error {
	c.del(QueryCacheKey("", kind, ""))
	return nil
}

func (c *MemoryCache) GetQuery(_ context.Context, kind, queryHash string) ([]byte, bool, error) {
	data, ok := c.get(QueryCacheKey("", kind, queryHash))
	return data, ok, nil
}

func (c *MemoryCache) SetQuery(_ context.Context, kind, queryHash string, data []byte, ttl time.Duration) error {
	c.set(QueryCacheKey("", kind, queryHash), data, ttl)
	return nil
}

func (c *MemoryCache) Close() error {
	c.mu.Lock()
	c.entries = make(map[string]*cacheEntry)
	c.mu.Unlock()
	return nil
}

// Len returns the number of entries in the cache.
func (c *MemoryCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}
