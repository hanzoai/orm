package orm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// CacheLayer provides read-through/write-through caching for entities and queries.
type CacheLayer interface {
	// GetEntity returns a cached entity, or false if not found.
	GetEntity(ctx context.Context, kind, id string) ([]byte, bool, error)

	// SetEntity caches an entity.
	SetEntity(ctx context.Context, kind, id string, data []byte, ttl time.Duration) error

	// InvalidateEntity removes a cached entity and all queries for its kind.
	InvalidateEntity(ctx context.Context, kind, id string) error

	// InvalidateKind removes all cached queries for a kind.
	InvalidateKind(ctx context.Context, kind string) error

	// GetQuery returns cached query results, or false if not found.
	GetQuery(ctx context.Context, kind, queryHash string) ([]byte, bool, error)

	// SetQuery caches query results.
	SetQuery(ctx context.Context, kind, queryHash string, data []byte, ttl time.Duration) error

	// Close releases resources.
	Close() error
}

// DefaultEntityTTL is the default TTL for cached entities.
const DefaultEntityTTL = 5 * time.Minute

// DefaultQueryTTL is the default TTL for cached query results.
const DefaultQueryTTL = 1 * time.Minute

// CacheKeyPrefix is prepended to all cache keys.
const CacheKeyPrefix = "orm"

// EntityCacheKey returns the cache key for an entity.
func EntityCacheKey(namespace, kind, id string) string {
	if namespace == "" {
		return fmt.Sprintf("%s:%s:%s", CacheKeyPrefix, kind, id)
	}
	return fmt.Sprintf("%s:%s:%s:%s", CacheKeyPrefix, namespace, kind, id)
}

// QueryCacheKey returns the cache key for a query.
func QueryCacheKey(namespace, kind, queryHash string) string {
	if namespace == "" {
		return fmt.Sprintf("%s:%s:q:%s", CacheKeyPrefix, kind, queryHash)
	}
	return fmt.Sprintf("%s:%s:%s:q:%s", CacheKeyPrefix, namespace, kind, queryHash)
}

// HashQuery produces a deterministic hash of a query for cache keying.
func HashQuery(queryStr string) string {
	h := sha256.Sum256([]byte(queryStr))
	return fmt.Sprintf("%x", h[:8])
}

// global cache layer (nil = caching disabled)
var globalCache CacheLayer

// SetCache sets the global cache layer.
func SetCache(cache CacheLayer) {
	globalCache = cache
}

// GetCache returns the global cache layer (may be nil).
func GetCache() CacheLayer {
	return globalCache
}
