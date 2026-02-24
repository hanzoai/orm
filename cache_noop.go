package orm

import (
	"context"
	"time"
)

// NoopCache is a cache layer that does nothing. Used when caching is disabled.
type NoopCache struct{}

var _ CacheLayer = (*NoopCache)(nil)

func (c *NoopCache) GetEntity(_ context.Context, _, _ string) ([]byte, bool, error) {
	return nil, false, nil
}

func (c *NoopCache) SetEntity(_ context.Context, _, _ string, _ []byte, _ time.Duration) error {
	return nil
}

func (c *NoopCache) InvalidateEntity(_ context.Context, _, _ string) error {
	return nil
}

func (c *NoopCache) InvalidateKind(_ context.Context, _ string) error {
	return nil
}

func (c *NoopCache) GetQuery(_ context.Context, _, _ string) ([]byte, bool, error) {
	return nil, false, nil
}

func (c *NoopCache) SetQuery(_ context.Context, _, _ string, _ []byte, _ time.Duration) error {
	return nil
}

func (c *NoopCache) Close() error {
	return nil
}
