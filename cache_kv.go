package orm

import (
	"context"
	"time"

	kv "github.com/hanzoai/kv-go/v9"
)

// KVCache is a Redis/Valkey-backed cache layer using hanzoai/kv-go.
type KVCache struct {
	client *kv.Client
	prefix string
}

var _ CacheLayer = (*KVCache)(nil)

// KVCacheConfig configures the Redis/Valkey cache backend.
type KVCacheConfig struct {
	Addr     string
	Password string
	DB       int
	Prefix   string // key prefix, default "orm"
	PoolSize int
}

// NewKVCache creates a new Redis/Valkey cache layer.
func NewKVCache(cfg KVCacheConfig) (*KVCache, error) {
	if cfg.PoolSize == 0 {
		cfg.PoolSize = 10
	}
	if cfg.Prefix == "" {
		cfg.Prefix = CacheKeyPrefix
	}

	client := kv.NewClient(&kv.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
		PoolSize: cfg.PoolSize,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, err
	}

	return &KVCache{
		client: client,
		prefix: cfg.Prefix,
	}, nil
}

// NewKVCacheFromClient creates a KVCache from an existing kv.Client.
func NewKVCacheFromClient(client *kv.Client, prefix string) *KVCache {
	if prefix == "" {
		prefix = CacheKeyPrefix
	}
	return &KVCache{client: client, prefix: prefix}
}

func (c *KVCache) key(parts ...string) string {
	k := c.prefix
	for _, p := range parts {
		k += ":" + p
	}
	return k
}

func (c *KVCache) GetEntity(ctx context.Context, kind, id string) ([]byte, bool, error) {
	val, err := c.client.Get(ctx, c.key(kind, id)).Bytes()
	if err == kv.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

func (c *KVCache) SetEntity(ctx context.Context, kind, id string, data []byte, ttl time.Duration) error {
	return c.client.Set(ctx, c.key(kind, id), data, ttl).Err()
}

func (c *KVCache) InvalidateEntity(ctx context.Context, kind, id string) error {
	// Delete the entity
	c.client.Del(ctx, c.key(kind, id))
	// Invalidate all queries for this kind
	return c.InvalidateKind(ctx, kind)
}

func (c *KVCache) InvalidateKind(ctx context.Context, kind string) error {
	pattern := c.key(kind, "q", "*")
	iter := c.client.Scan(ctx, 0, pattern, 100).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if len(keys) > 0 {
		c.client.Del(ctx, keys...)
	}
	return iter.Err()
}

func (c *KVCache) GetQuery(ctx context.Context, kind, queryHash string) ([]byte, bool, error) {
	val, err := c.client.Get(ctx, c.key(kind, "q", queryHash)).Bytes()
	if err == kv.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return val, true, nil
}

func (c *KVCache) SetQuery(ctx context.Context, kind, queryHash string, data []byte, ttl time.Duration) error {
	return c.client.Set(ctx, c.key(kind, "q", queryHash), data, ttl).Err()
}

func (c *KVCache) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}
