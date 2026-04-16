package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/pkg/xcache"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer/shared"
)

const (
	// defaultAffinityTTL is the duration a channel affinity mapping stays valid.
	// This should be longer than provider-side cache TTLs (e.g. Anthropic's 5-min prompt cache)
	// because keeping the same channel allows the provider cache to be re-warmed on expiry,
	// while switching channels guarantees a cold start.
	defaultAffinityTTL = 3 * time.Hour
	// affinityCleanupInterval controls how often expired entries are purged.
	affinityCleanupInterval = 10 * time.Minute
)

// ChannelAffinityStore maintains a mapping from session/cache keys to channel IDs
// so that subsequent requests with the same key can be routed to the same channel,
// preserving provider-side caches (e.g. Anthropic prompt cache, Codex session context).
//
// The underlying cache is pluggable: memory by default, Redis when configured.
type ChannelAffinityStore struct {
	cache xcache.Cache[int]
}

// NewChannelAffinityStore creates a store backed by an in-memory cache.
func NewChannelAffinityStore() *ChannelAffinityStore {
	return &ChannelAffinityStore{
		cache: xcache.NewMemoryWithOptions[int](defaultAffinityTTL, affinityCleanupInterval),
	}
}

// NewChannelAffinityStoreFromCache creates a store backed by the provided cache,
// allowing callers to supply a Redis or two-level cache for distributed deployments.
func NewChannelAffinityStoreFromCache(cache xcache.Cache[int]) *ChannelAffinityStore {
	return &ChannelAffinityStore{cache: cache}
}

// Lookup returns the preferred channel ID for the given request, if one exists.
func (s *ChannelAffinityStore) Lookup(ctx context.Context, model string, req *llm.Request) (int, bool) {
	key := s.buildKey(ctx, model, req)
	if key == "" {
		return 0, false
	}

	channelID, err := s.cache.Get(ctx, key)
	if err != nil {
		return 0, false
	}

	if log.DebugEnabled(ctx) {
		log.Debug(ctx, "channel affinity cache hit",
			log.String("affinity_key", key),
			log.Int("channel_id", channelID),
		)
	}

	return channelID, true
}

// Record stores the affinity mapping for the given request to the specified channel.
func (s *ChannelAffinityStore) Record(ctx context.Context, model string, req *llm.Request, channelID int) {
	key := s.buildKey(ctx, model, req)
	if key == "" {
		return
	}

	if err := s.cache.Set(ctx, key, channelID); err != nil {
		log.Warn(ctx, "failed to record channel affinity",
			log.String("affinity_key", key),
			log.Cause(err),
		)
	}
}

// buildKey extracts the best available session/cache key for affinity.
// Priority: SessionID (session-level, stable across conversation) > PromptCacheKey (content-derived).
//
// SessionID is preferred because it stays constant throughout a session, while
// PromptCacheKey can shift as cache_control markers move during conversation growth.
// The key is scoped by model since channel selection is model-dependent.
func (s *ChannelAffinityStore) buildKey(ctx context.Context, model string, req *llm.Request) string {
	var sessionKey string

	// Prefer context session ID — it's set by trace middleware from Claude Code's
	// metadata.user_id.session_id or Codex's Session_id header, and stays stable
	// across the entire session regardless of conversation growth.
	if sid, ok := shared.GetSessionID(ctx); ok && sid != "" {
		sessionKey = sid
	}

	// Fall back to PromptCacheKey for stateless API calls where no session exists.
	if sessionKey == "" {
		if req != nil && req.PromptCacheKey != nil && *req.PromptCacheKey != "" {
			sessionKey = *req.PromptCacheKey
		}
	}

	if sessionKey == "" {
		return ""
	}

	return fmt.Sprintf("affinity:%s:%s", model, sessionKey)
}
