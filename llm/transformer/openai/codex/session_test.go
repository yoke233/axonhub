package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/llm"
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
		"missing prompt_cache_key should be backfilled from caller Session_id")
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

func TestPromptCacheKey_PreservedFromCallerBodyWhenSessionHeaderMissing(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)
	explicitKey := "caller-body-cache-key"

	finalReq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model:          "gpt-5-codex",
		RequestType:    "",
		PromptCacheKey: &explicitKey,
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(finalReq.Body, &payload))

	assert.Equal(t, explicitKey, payload["prompt_cache_key"])
	assertUUIDSessionID(t, finalReq.Headers.Get(SessionHeader))
	assert.NotEqual(t, explicitKey, finalReq.Headers.Get(SessionHeader))

	finalReq2, err := outbound.TransformRequest(ctx, &llm.Request{
		Model:          "gpt-5-codex",
		RequestType:    "",
		PromptCacheKey: &explicitKey,
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, finalReq.Headers.Get(SessionHeader), finalReq2.Headers.Get(SessionHeader))
}

func TestConversationFields_PreserveCallerMismatchWhenBothSidesExplicit(t *testing.T) {
	sessionID, promptCacheKey := resolveConversationFields(
		context.Background(),
		"caller-session-id",
		`{"session_id":"turn-meta-uuid"}`,
		nil,
		lo.ToPtr("caller-prompt-cache-key"),
		nil,
		"test-scope",
		false,
	)

	require.NotNil(t, promptCacheKey)
	assert.Equal(t, "caller-session-id", sessionID)
	assert.Equal(t, "caller-prompt-cache-key", *promptCacheKey)
}

func TestConversationFields_GeneratesConversationIdentityForGenericCaller(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithToken(t, accessToken)
	req := newCodexChatCompletionRequest(t)

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	body := readHTTPBody(t, finalReq)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))

	header := finalReq.Header.Get(SessionHeader)
	bodyKey, _ := payload["prompt_cache_key"].(string)
	assertUUIDSessionID(t, header)
	assert.Equal(t, header, bodyKey)
}

func TestConversationFields_CodexCallerOmitsConversationIdentityWhenMissing(t *testing.T) {
	ctx := shared.WithSessionID(context.Background(), "trace-fallback")
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithToken(t, accessToken)
	req := newCodexChatCompletionRequest(t)
	req.Header.Set("Originator", DefaultOriginator)
	req.Header.Set("User-Agent", BuildDefaultCodexUserAgent())

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	body := readHTTPBody(t, finalReq)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	assert.Empty(t, finalReq.Header.Get(SessionHeader))
	_, hasBodyKey := payload["prompt_cache_key"]
	assert.False(t, hasBodyKey)
}

func TestSessionID_UsesAnthropicPromptCacheKeyWhenPresent(t *testing.T) {
	ctx := shared.WithSessionID(context.Background(), "trace-fallback")
	sessionID, promptCacheKey := resolveConversationFields(ctx, "", "",
		map[string]any{shared.MetaKeyAnthropicPromptCacheKey: "anthropic-stable"},
		nil,
		newMemorySessionIDCache(),
		"test-scope",
		true,
	)
	require.NotNil(t, promptCacheKey)
	assert.Equal(t, "anthropic-stable", *promptCacheKey)
	assertUUIDSessionID(t, sessionID)
	assert.NotEqual(t, "anthropic-stable", sessionID)
}

func TestResolveConversationFields_OmitsMissingValuesForCodexCaller(t *testing.T) {
	sessionID, promptCacheKey := resolveConversationFields(context.Background(), "", "", nil, nil, nil, "test-scope", false)
	assert.Empty(t, sessionID)
	assert.Nil(t, promptCacheKey)
}

func TestResolveConversationFields_UsesAnthropicPromptCacheKeyWithoutSessionHeader(t *testing.T) {
	outbound := newTestCodexOutbound(t)

	finalReq, err := outbound.TransformRequest(context.Background(), &llm.Request{
		Model:       "gpt-5-codex",
		RequestType: llm.RequestTypeChat,
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		TransformerMetadata: map[string]any{
			shared.MetaKeyAnthropicPromptCacheKey: "anthropic-stable",
		},
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(finalReq.Body, &payload))
	assertUUIDSessionID(t, finalReq.Headers.Get(SessionHeader))
	assert.NotEqual(t, "anthropic-stable", finalReq.Headers.Get(SessionHeader))
	assert.Equal(t, "anthropic-stable", payload["prompt_cache_key"])
}

func TestResolveConversationFields_UsesTraceSessionFallbackForGenericCaller(t *testing.T) {
	outbound := newTestCodexOutbound(t)
	ctx := shared.WithSessionID(context.Background(), "trace-fallback")

	finalReq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model:       "gpt-5-codex",
		RequestType: llm.RequestTypeChat,
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(finalReq.Body, &payload))
	assertUUIDSessionID(t, finalReq.Headers.Get(SessionHeader))
	assert.NotEqual(t, "trace-fallback", finalReq.Headers.Get(SessionHeader))
	assert.Equal(t, finalReq.Headers.Get(SessionHeader), payload["prompt_cache_key"])
}

func assertCodexStyleSessionID(t *testing.T, value string) {
	t.Helper()

	_, err := uuid.Parse(value)
	require.NoError(t, err)
}

func assertUUIDSessionID(t *testing.T, value string) {
	t.Helper()

	require.NotEmpty(t, value)
	_, err := uuid.Parse(value)
	require.NoError(t, err)
}
