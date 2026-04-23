package biz

import (
	"context"
	"time"

	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

type codexSessionIDCacheAdapter struct {
	cache xcache.Cache[string]
}

func newCodexSessionIDCache(cfg xcache.Config) codex.SessionIDCache {
	if cfg.Mode == "" {
		cfg.Mode = xcache.ModeMemory
	}

	return &codexSessionIDCacheAdapter{
		cache: xcache.NewFromConfig[string](cfg),
	}
}

func (c *codexSessionIDCacheAdapter) Get(ctx context.Context, key string) (string, bool) {
	value, err := c.cache.Get(ctx, key)
	if err != nil || value == "" {
		return "", false
	}

	return value, true
}

func (c *codexSessionIDCacheAdapter) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if ttl > 0 {
		return c.cache.Set(ctx, key, value, xcache.WithExpiration(ttl))
	}

	return c.cache.Set(ctx, key, value)
}
