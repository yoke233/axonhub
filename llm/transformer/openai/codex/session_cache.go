package codex

import (
	"context"
	"sync"
	"time"
)

// SessionIDCache stores the stable mapping from internal conversation seeds to
// generated Codex Session_id values. Implementations can be backed by memory,
// Redis, or any shared cache.
type SessionIDCache interface {
	Get(ctx context.Context, key string) (string, bool)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
}

type memorySessionIDCache struct {
	entries sync.Map
}

type memorySessionIDCacheEntry struct {
	value     string
	expiresAt time.Time
}

func newMemorySessionIDCache() SessionIDCache {
	return &memorySessionIDCache{}
}

func (c *memorySessionIDCache) Get(_ context.Context, key string) (string, bool) {
	raw, ok := c.entries.Load(key)
	if !ok {
		return "", false
	}

	entry, ok := raw.(memorySessionIDCacheEntry)
	if !ok {
		c.entries.Delete(key)
		return "", false
	}

	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		c.entries.Delete(key)
		return "", false
	}

	return entry.value, true
}

func (c *memorySessionIDCache) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	entry := memorySessionIDCacheEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}

	c.entries.Store(key, entry)
	return nil
}
