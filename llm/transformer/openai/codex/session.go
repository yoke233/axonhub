package codex

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/looplj/axonhub/llm/transformer/shared"
)

// resolveConversationKey picks the single value that becomes BOTH:
//   - the body's prompt_cache_key (real CLI: codex-rs/core/src/client.rs:853)
//   - the outgoing 'Session_id' header (real CLI: codex-api/src/requests/headers.rs:8)
//
// Real codex_cli_rs sends them equal because both derive from conversation_id.
// We mirror that invariant in passthrough-first order so that a real codex caller
// gets its own value preserved end-to-end. Conversation-stable sources (caller-supplied
// or bridged from anthropic) outrank the per-request trace session id:
//
//  1. raw 'Session_id' header (real codex CLI sends this)
//  2. session id embedded in 'X-Codex-Turn-Metadata' (real codex CLI sends this)
//  3. anthropic prompt cache key from TransformerMetadata (stable across turns when bridged from anthropic inbound)
//  4. existing PromptCacheKey on the request body (covers passthrough-body callers and explicit OpenAI prompt_cache_key)
//  5. axonhub trace session id from context (per-request fallback)
//  6. fresh UUID as last resort
//
// The function never returns "".
func resolveConversationKey(
	ctx context.Context,
	rawSessionID string,
	rawTurnMetadata string,
	transformerMetadata map[string]any,
	existingPromptCacheKey *string,
) string {
	if v := strings.TrimSpace(rawSessionID); v != "" {
		return v
	}
	if v := strings.TrimSpace(ExtractSessionIDFromTurnMetadata(rawTurnMetadata)); v != "" {
		return v
	}
	if transformerMetadata != nil {
		if anthropic, ok := transformerMetadata[shared.MetaKeyAnthropicPromptCacheKey].(string); ok {
			if v := strings.TrimSpace(anthropic); v != "" {
				return v
			}
		}
	}
	if existingPromptCacheKey != nil {
		if v := strings.TrimSpace(*existingPromptCacheKey); v != "" {
			return v
		}
	}
	if v, ok := shared.GetSessionID(ctx); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return uuid.NewString()
}
