package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/llm/transformer/shared"
)

func TestSessionID_PassthroughFromCallerHeader(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithToken(t, accessToken)
	req := newCodexChatCompletionRequest(t)
	req.Header.Set(SessionHeader, "caller-conversation-uuid")

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	body := readHTTPBody(t, finalReq)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	assert.Equal(t, "caller-conversation-uuid", finalReq.Header.Get(SessionHeader),
		"caller-supplied Session_id must be passed through")
	assert.Equal(t, "caller-conversation-uuid", payload["prompt_cache_key"],
		"prompt_cache_key in body must equal Session_id (real CLI invariant)")
}

func TestSessionID_FromTurnMetadataWhenSessionHeaderMissing(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithToken(t, accessToken)
	req := newCodexChatCompletionRequest(t)
	req.Header.Set(TurnMetadataHeader, `{"session_id":"turn-meta-uuid"}`)

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	body := readHTTPBody(t, finalReq)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	assert.Equal(t, "turn-meta-uuid", finalReq.Header.Get(SessionHeader))
	assert.Equal(t, "turn-meta-uuid", payload["prompt_cache_key"])
}

func TestCacheKey_EqualsSessionIDWhenGenerated(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithToken(t, accessToken)
	req := newCodexChatCompletionRequest(t)
	// no session-related headers; transformer must generate a value

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	body := readHTTPBody(t, finalReq)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	header := finalReq.Header.Get(SessionHeader)
	bodyKey, _ := payload["prompt_cache_key"].(string)
	assert.NotEmpty(t, header, "Session_id must be set even without caller input")
	assert.NotEmpty(t, bodyKey, "prompt_cache_key must be set in body")
	assert.Equal(t, header, bodyKey,
		"Session_id header MUST equal prompt_cache_key body — real CLI invariant")
}

func TestSessionID_AnthropicPromptCacheKeyOutranksTraceSession(t *testing.T) {
	ctx := shared.WithSessionID(context.Background(), "trace-fallback")
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithToken(t, accessToken)
	req := newCodexChatCompletionRequest(t)
	// inject anthropic key the way the inbound bridge would (via header convention is brittle;
	// we exercise the pure helper for direct validation):
	got := resolveConversationKey(ctx, "", "",
		map[string]any{shared.MetaKeyAnthropicPromptCacheKey: "anthropic-stable"},
		nil,
	)
	assert.Equal(t, "anthropic-stable", got,
		"conversation-stable anthropic key must beat per-request trace session")

	// also assert a pipeline run still produces equal header == body when only ctx is set
	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)
	body := readHTTPBody(t, finalReq)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	assert.Equal(t, finalReq.Header.Get(SessionHeader), payload["prompt_cache_key"])
}

func TestResolveConversationKey_LastResortIsFreshUUID(t *testing.T) {
	got := resolveConversationKey(context.Background(), "", "", nil, nil)
	assert.NotEmpty(t, got)
	assert.Len(t, got, 36, "should look like a UUID")
}
