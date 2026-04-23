package codex

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/looplj/axonhub/llm/transformer/shared"
)

const derivedSessionIDCacheTTL = 24 * time.Hour

// resolveConversationFields preserves caller-supplied Session_id and prompt_cache_key,
// and only fills whichever side is missing.
//
// Fallback priority for missing values:
//
//  1. raw 'Session_id' header (real codex CLI sends this)
//  2. session id embedded in 'X-Codex-Turn-Metadata' (real codex CLI sends this)
//  3. a stable UUID-shaped session id derived from a non-Codex prompt cache key
//     or trace session seed (only when fallback synthesis is enabled)
//  4. fresh UUID as last resort (only when fallback synthesis is enabled)
//
// Real Codex callers already express their own conversation identity, so callers
// with Codex traits should disable fallback synthesis and rely on passthrough.
func resolveConversationFields(
	ctx context.Context,
	rawSessionID string,
	rawTurnMetadata string,
	transformerMetadata map[string]any,
	existingPromptCacheKey *string,
	sessionIDCache SessionIDCache,
	sessionIDScopeKey string,
	allowFallbackSynthesis bool,
) (string, *string) {
	sessionID := strings.TrimSpace(rawSessionID)
	promptCacheKey := ""
	turnMetadataSessionID := strings.TrimSpace(ExtractSessionIDFromTurnMetadata(rawTurnMetadata))
	anthropicPromptCacheKey := ""
	if transformerMetadata != nil {
		if anthropic, ok := transformerMetadata[shared.MetaKeyAnthropicPromptCacheKey].(string); ok {
			anthropicPromptCacheKey = strings.TrimSpace(anthropic)
		}
	}
	if existingPromptCacheKey != nil {
		if v := strings.TrimSpace(*existingPromptCacheKey); v != "" {
			promptCacheKey = v
		}
	}

	if sessionID == "" {
		switch {
		case turnMetadataSessionID != "":
			sessionID = turnMetadataSessionID
		case allowFallbackSynthesis && promptCacheKey != "":
			sessionID = deriveCodexSessionID(ctx, sessionIDCache, sessionIDScopeKey, promptCacheKey)
		case allowFallbackSynthesis && anthropicPromptCacheKey != "":
			sessionID = deriveCodexSessionID(ctx, sessionIDCache, sessionIDScopeKey, anthropicPromptCacheKey)
		}
	}

	if sessionID == "" && allowFallbackSynthesis {
		if v, ok := shared.GetSessionID(ctx); ok {
			sessionID = deriveCodexSessionID(ctx, sessionIDCache, sessionIDScopeKey, v)
		}
	}
	if sessionID == "" && allowFallbackSynthesis {
		sessionID = newGeneratedCodexSessionID()
	}

	if promptCacheKey == "" {
		switch {
		case anthropicPromptCacheKey != "" && turnMetadataSessionID == "":
			promptCacheKey = anthropicPromptCacheKey
		case sessionID != "":
			promptCacheKey = sessionID
		}
	}

	if promptCacheKey == "" {
		return sessionID, nil
	}

	return sessionID, &promptCacheKey
}

func deriveCodexSessionID(ctx context.Context, cache SessionIDCache, scopeKey string, seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	if looksCodexSessionID(seed) {
		return seed
	}

	cacheKey := buildSessionIDCacheKey(scopeKey, seed)
	if cache != nil {
		if cached, ok := cache.Get(ctx, cacheKey); ok && cached != "" {
			return cached
		}
	}

	id := newGeneratedCodexSessionID()
	if cache != nil {
		_ = cache.Set(ctx, cacheKey, id, derivedSessionIDCacheTTL)
	}

	return id
}

func newGeneratedCodexSessionID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}

	return id.String()
}

func looksCodexSessionID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	_, err := uuid.Parse(value)
	return err == nil
}

func buildSessionIDCacheKey(scopeKey string, seed string) string {
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		scopeKey = "default"
	}

	return "codex:session-id:" + scopeKey + ":" + seed
}
