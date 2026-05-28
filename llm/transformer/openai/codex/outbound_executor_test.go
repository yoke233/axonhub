package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer/openai"
)

func TestCodexOutbound_StreamAcceptHeader(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	capturedHeaders := make(chan http.Header, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	outbound, err := NewOutboundTransformer(Params{
		BaseURL: server.URL,
		TokenProvider: staticTokenGetter{
			creds: &oauth.OAuthCredentials{
				AccessToken: accessToken,
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		},
	})
	require.NoError(t, err)

	request := buildCodexStreamRequest(t, ctx, outbound, false)
	executor := httpclient.NewHttpClientWithClient(server.Client())

	stream, err := executor.DoStream(ctx, request)
	require.NoError(t, err)
	defer func() {
		_ = stream.Close()
	}()

	var headers http.Header
	select {
	case headers = <-capturedHeaders:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for captured stream request")
	}

	assert.Equal(t, "text/event-stream", headers.Get("Accept"))
	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, DefaultOriginator, headers.Get("Originator"))
	assert.Equal(t, BuildDefaultCodexUserAgent(), headers.Get("User-Agent"))
	assert.Equal(t, testChatAccountID, headers.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "Bearer "+accessToken, headers.Get("Authorization"))
}

func TestCodexOutbound_StreamAllowsDownstreamIdentityOverrides(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	capturedHeaders := make(chan http.Header, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	outbound, err := NewOutboundTransformer(Params{
		BaseURL: server.URL,
		TokenProvider: staticTokenGetter{
			creds: &oauth.OAuthCredentials{
				AccessToken: accessToken,
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		},
	})
	require.NoError(t, err)

	request := buildCodexStreamRequest(t, ctx, outbound, true)
	executor := httpclient.NewHttpClientWithClient(server.Client())

	stream, err := executor.DoStream(ctx, request)
	require.NoError(t, err)
	defer func() {
		_ = stream.Close()
	}()

	var headers http.Header
	select {
	case headers = <-capturedHeaders:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for captured stream request")
	}

	assert.Equal(t, legacyCodexOriginator(), headers.Get("Originator"))
	assert.Equal(t, legacyCodexUserAgent(), headers.Get("User-Agent"))
	assert.Contains(t, strings.ToLower(headers.Get("User-Agent")), legacyCodexOriginator())
	assert.Equal(t, testChatAccountID, headers.Get("Chatgpt-Account-Id"))
	assert.Equal(t, "Bearer "+accessToken, headers.Get("Authorization"))
}

func TestCodexOutbound_CustomizeExecutorAggregatesNonStreamRequests(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)

	outbound, err := NewOutboundTransformer(Params{
		BaseURL: "https://chatgpt.com/backend-api/codex#",
		TokenProvider: staticTokenGetter{
			creds: &oauth.OAuthCredentials{
				AccessToken: accessToken,
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		},
	})
	require.NoError(t, err)

	request := buildCodexStreamRequest(t, ctx, outbound, false)
	executor := outbound.CustomizeExecutor(&mockCodexExecutor{
		streamEvents: []*httpclient.StreamEvent{
			{Type: "response.created", Data: []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_test_123","object":"response","created_at":1700000000,"model":"gpt-5-codex","status":"in_progress","output":[]}}`)},
			{Type: "response.output_item.added", Data: []byte(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_test_456","type":"message","status":"in_progress","role":"assistant"}}`)},
			{Type: "response.content_part.added", Data: []byte(`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_test_456","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)},
			{Type: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_test_456","output_index":0,"content_index":0,"delta":"Hello"}`)},
			{Type: "response.output_text.done", Data: []byte(`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg_test_456","output_index":0,"content_index":0,"text":"Hello"}`)},
			{Type: "response.output_item.done", Data: []byte(`{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"id":"msg_test_456","type":"message","status":"completed","role":"assistant"}}`)},
			{Type: "response.completed", Data: []byte(`{"type":"response.completed","sequence_number":6,"response":{"id":"resp_test_123","object":"response","created_at":1700000000,"model":"gpt-5-codex","status":"completed","output":[]}}`)},
		},
	})

	response, err := executor.Do(ctx, request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "application/json", response.Headers.Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body, &body))
	assert.Equal(t, "resp_test_123", body["id"])
	assert.Equal(t, "completed", body["status"])
	assert.Equal(t, "gpt-5-codex", body["model"])
}

var _ pipeline.ChannelCustomizedExecutor = (*OutboundTransformer)(nil)

type mockCodexExecutor struct {
	streamEvents []*httpclient.StreamEvent
}

func (m *mockCodexExecutor) Do(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return nil, assert.AnError
}

func (m *mockCodexExecutor) DoStream(_ context.Context, _ *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	return streams.SliceStream(m.streamEvents), nil
}

type codexProxyExecutor struct {
	proxy func(*http.Request) (*url.URL, error)
}

func (m *codexProxyExecutor) Do(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return nil, assert.AnError
}

func (m *codexProxyExecutor) DoStream(_ context.Context, _ *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	return nil, assert.AnError
}

func (m *codexProxyExecutor) ProxyFunc() func(*http.Request) (*url.URL, error) {
	return m.proxy
}

type eagerRefreshTokenGetter struct {
	creds             *oauth.OAuthCredentials
	ensureCalls       int
	getCalls          int
	lastRefreshBefore time.Duration
}

func (g *eagerRefreshTokenGetter) Get(_ context.Context) (*oauth.OAuthCredentials, error) {
	g.getCalls++
	return g.creds, nil
}

func (g *eagerRefreshTokenGetter) EnsureFresh(_ context.Context, refreshBefore time.Duration) (*oauth.OAuthCredentials, error) {
	g.ensureCalls++
	g.lastRefreshBefore = refreshBefore
	return g.creds, nil
}

func TestCodexOutbound_DoesNotInjectCLIInstructions(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Stream: lo.ToPtr(true),
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)

	instructions, hasInstructions := body["instructions"]
	assert.True(t, hasInstructions, "instructions field must always be present for Codex")
	assert.Equal(t, "", instructions)
	assert.NotContains(t, string(hreq.Body), "You are a coding agent running in the Codex CLI")
	assert.NotContains(t, string(hreq.Body), "You are Codex")
	assert.Equal(t, false, body["store"])
}

func TestCodexOutbound_UsesEnsureFreshWhenAvailable(t *testing.T) {
	ctx := context.Background()
	getter := &eagerRefreshTokenGetter{
		creds: &oauth.OAuthCredentials{
			AccessToken:  testAccessTokenWithAccountID(t),
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		},
	}

	outbound, err := NewOutboundTransformer(Params{
		BaseURL:       "https://chatgpt.com/backend-api/codex#",
		TokenProvider: getter,
	})
	require.NoError(t, err)

	_, err = outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Stream: lo.ToPtr(true),
	})
	require.NoError(t, err)

	assert.Equal(t, 1, getter.ensureCalls)
	assert.Equal(t, 0, getter.getCalls)
	assert.Equal(t, RequestRefreshBefore, getter.lastRefreshBefore)
}

func TestCodexOutbound_StripsBodyFieldsCodexBackendRejects(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)
	retention := "24h"
	safetyIdentifier := "user-test"
	previousResponseID := "resp_prev_123"

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Stream:             lo.ToPtr(true),
		SafetyIdentifier:   &safetyIdentifier,
		StreamOptions:      &llm.StreamOptions{IncludeUsage: true},
		PreviousResponseID: &previousResponseID,
		TransformerMetadata: map[string]any{
			"prompt_cache_retention": &retention,
			"include_obfuscation":    true,
		},
		RawRequest: &httpclient.Request{
			Headers: http.Header{
				VersionHeader:   []string{"9.9.9"},
				TurnStateHeader: []string{"turn-state-123"},
			},
		},
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)

	assert.Equal(t, "9.9.9", hreq.Headers.Get(VersionHeader))
	assert.Equal(t, "turn-state-123", hreq.Headers.Get(TurnStateHeader))
	assert.Equal(t, "Keep-Alive", hreq.Headers.Get("Connection"))
	assert.NotContains(t, body, "prompt_cache_retention")
	assert.NotContains(t, body, "safety_identifier")
	assert.Equal(t, previousResponseID, body["previous_response_id"])
	assert.NotContains(t, body, "stream_options")
}

func TestCodexOutbound_PreservesMinimalCompatTransforms(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)
	store := true
	parallelToolCalls := false
	maxTokens := int64(128)
	maxCompletionTokens := int64(256)
	topP := 0.8
	serviceTier := "flex"
	reasoningSummary := "detailed"

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Tools: []llm.Tool{{
			Type: "function",
			Function: llm.Function{
				Name:       "shell",
				Parameters: []byte(`{"type":"object","properties":{}}`),
			},
		}},
		Store:               &store,
		ParallelToolCalls:   &parallelToolCalls,
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &maxCompletionTokens,
		TopP:                &topP,
		ServiceTier:         &serviceTier,
		ReasoningSummary:    &reasoningSummary,
		Metadata:            map[string]string{"source": "caller"},
		TransformerMetadata: map[string]any{},
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)

	assert.Equal(t, true, body["store"])
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, false, body["parallel_tool_calls"])
	assert.Equal(t, serviceTier, body["service_tier"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	// The ChatGPT Codex backend rejects these fields, so they must be stripped
	// even when callers (e.g. the channel-test harness) supply them.
	assert.NotContains(t, body, "max_output_tokens")
	assert.NotContains(t, body, "max_tokens")
	assert.NotContains(t, body, "top_p")
	assert.NotContains(t, body, "metadata")

	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, reasoningSummary, reasoning["summary"])

	assert.NotContains(t, string(hreq.Body), "You are a coding agent running in the Codex CLI")
	assert.NotContains(t, string(hreq.Body), "You are Codex")
}

func TestCodexOutbound_AppliesReasoningDefaultsWhenMissing(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Tools: []llm.Tool{{
			Type: "function",
			Function: llm.Function{
				Name:       "shell",
				Parameters: []byte(`{"type":"object","properties":{}}`),
			},
		}},
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)
	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, true, body["parallel_tool_calls"])
	assert.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
	assert.Equal(t, "auto", reasoning["summary"])
	assert.Equal(t, false, body["store"])
	assert.NotContains(t, body, "metadata")
}

// Regression: the channel-test harness (internal/server/orchestrator/tester.go)
// always sets MaxCompletionTokens=256. Without sanitization that surfaces as
// "max_output_tokens" in the Codex payload and the ChatGPT Codex backend
// answers 400 "Unsupported parameter: max_output_tokens". Make sure every
// known-unsupported field gets stripped before the request leaves.
func TestCodexOutbound_StripsAllCodexUnsupportedFields(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	temperature := 0.7
	topP := 0.9
	maxCompletionTokens := int64(256)
	user := "user-abc"
	prevID := "resp_prev"
	truncation := "auto"
	safety := "user-safety"
	retention := "24h"

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Stream:              lo.ToPtr(true),
		Temperature:         &temperature,
		TopP:                &topP,
		MaxCompletionTokens: &maxCompletionTokens,
		User:                &user,
		PreviousResponseID:  &prevID,
		SafetyIdentifier:    &safety,
		StreamOptions:       &llm.StreamOptions{IncludeUsage: true},
		Metadata:            map[string]string{"source": "channel-test"},
		TransformerMetadata: map[string]any{
			"truncation":             &truncation,
			"prompt_cache_retention": &retention,
			"max_tool_calls":         int64(8),
		},
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)

	for _, field := range codexUnsupportedRequestFields {
		assert.NotContainsf(t, body, field, "field %q must be stripped before forwarding to Codex backend", field)
	}
}

func TestCodexOutbound_ForcesArrayInputsForSingleMessage(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	// A single simple user message — without ArrayInputs=true this would be
	// serialized as a plain string "input". With the fix, it must be an array.
	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		Stream: lo.ToPtr(true),
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)

	// The "input" field must be an array of items, not a plain string.
	inputRaw, ok := body["input"]
	require.True(t, ok, "input field must be present")
	inputSlice, ok := inputRaw.([]interface{})
	require.True(t, ok, "input should be an array, got %T", inputRaw)
	assert.NotEmpty(t, inputSlice)

	// Verify the single item has the expected message structure.
	first, ok := inputSlice[0].(map[string]interface{})
	require.True(t, ok, "first input item should be a map, got %T", inputSlice[0])
	assert.Equal(t, "message", first["type"])
	assert.Equal(t, "user", first["role"])
}

func TestCodexOutbound_CodexCallerDoesNotInjectOptionalCodexParametersWhenMissing(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		Model: "gpt-5-codex",
		Messages: []llm.Message{{
			Role:    "user",
			Content: llm.MessageContent{Content: lo.ToPtr("Hello")},
		}},
		RawRequest: &httpclient.Request{
			Headers: http.Header{
				"Originator": []string{DefaultOriginator},
				"User-Agent": []string{BuildDefaultCodexUserAgent()},
			},
		},
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)
	assert.Equal(t, true, body["stream"])
	_, hasParallelToolCalls := body["parallel_tool_calls"]
	_, hasInclude := body["include"]
	_, hasReasoning := body["reasoning"]
	_, hasStore := body["store"]
	_, hasPromptCacheKey := body["prompt_cache_key"]
	assert.False(t, hasParallelToolCalls)
	assert.False(t, hasInclude)
	assert.False(t, hasReasoning)
	assert.False(t, hasStore)
	assert.False(t, hasPromptCacheKey)
	assert.Empty(t, hreq.Headers.Get(SessionHeader))
}

func TestCodexOutbound_ImageRequestUsesHostedTool(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	hreq, err := outbound.TransformRequest(ctx, &llm.Request{
		RequestType: llm.RequestTypeImage,
		Model:       "gpt-5-codex",
		Image: &llm.ImageRequest{
			Prompt:       "Draw a terminal UI",
			Size:         "1024x1024",
			Quality:      "high",
			OutputFormat: "png",
		},
	})
	require.NoError(t, err)

	body := decodeCodexRequestBody(t, hreq)
	require.Equal(t, string(llm.RequestTypeImage), hreq.RequestType)
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, map[string]any{"type": llm.ToolTypeImageGeneration}, body["tool_choice"])

	tools, ok := body["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, llm.ToolTypeImageGeneration, tool["type"])
	assert.Equal(t, "1024x1024", tool["size"])
	assert.Equal(t, "high", tool["quality"])
	assert.Equal(t, "png", tool["output_format"])
}

func TestCodexOutbound_ImageResponseMapsToImageResult(t *testing.T) {
	ctx := context.Background()
	outbound := newTestCodexOutbound(t)

	resp, err := outbound.TransformResponse(ctx, &httpclient.Response{
		StatusCode: http.StatusOK,
		Request: &httpclient.Request{
			RequestType: string(llm.RequestTypeImage),
		},
		Body: []byte(`{
			"id": "resp_image",
			"object": "response",
			"created_at": 1700000000,
			"status": "completed",
			"model": "gpt-5-codex",
			"output": [
				{
					"id": "img_1",
					"type": "image_generation_call",
					"status": "completed",
					"result": "aW1hZ2U="
				}
			]
		}`),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Image)
	require.Len(t, resp.Image.Data, 1)
	assert.Equal(t, "aW1hZ2U=", resp.Image.Data[0].B64JSON)
}

func TestCodexWebsocketAuthAppliesBearer(t *testing.T) {
	headers := http.Header{}

	err := applyCodexWebsocketAuth(headers, &httpclient.AuthConfig{
		Type:   httpclient.AuthTypeBearer,
		APIKey: "access-token",
	})
	require.NoError(t, err)

	assert.Equal(t, "Bearer access-token", headers.Get("Authorization"))
}

func TestCodexWebsocketUsesExecutorProxyFunc(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.local:8080")
	require.NoError(t, err)

	executor := &codexProxyExecutor{
		proxy: func(*http.Request) (*url.URL, error) {
			return proxyURL, nil
		},
	}

	wrapped := (&codexExecutor{inner: executor}).websocketProxyFunc()
	resolved, err := wrapped(&http.Request{})
	require.NoError(t, err)
	require.Equal(t, proxyURL, resolved)
}

func newTestCodexOutbound(t *testing.T) *OutboundTransformer {
	t.Helper()

	accessToken := testAccessTokenWithAccountID(t)

	outbound, err := NewOutboundTransformer(Params{
		BaseURL: "https://chatgpt.com/backend-api/codex#",
		TokenProvider: staticTokenGetter{
			creds: &oauth.OAuthCredentials{
				AccessToken: accessToken,
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		},
	})
	require.NoError(t, err)

	return outbound
}

func decodeCodexRequestBody(t *testing.T, hreq *httpclient.Request) map[string]any {
	t.Helper()

	var body map[string]any
	require.NoError(t, json.Unmarshal(hreq.Body, &body))

	return body
}

func buildCodexStreamRequest(t *testing.T, ctx context.Context, outbound *OutboundTransformer, withInboundIdentity bool) *httpclient.Request {
	t.Helper()

	bodyBytes, err := json.Marshal(map[string]any{
		"model":  "gpt-5-codex",
		"stream": true,
		"messages": []map[string]any{{
			"role":    "user",
			"content": "Hello",
		}},
	})
	require.NoError(t, err)

	rawReq, err := http.NewRequest(http.MethodPost, "http://localhost:8090/v1/chat/completions", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	rawReq.Header.Set("Accept", "application/json")
	rawReq.Header.Set("Connection", "keep-alive")
	rawReq.Header.Set("Content-Type", "application/json")
	rawReq.Header.Set("Conversation_id", "legacy-conversation")
	rawReq.Header.Set("Openai-Beta", "responses=experimental")
	rawReq.Header.Set("Session_id", "provided-session")
	rawReq.Header.Set("Version", "9.9.9")
	if withInboundIdentity {
		rawReq.Header.Set("Originator", legacyCodexOriginator())
		rawReq.Header.Set("User-Agent", legacyCodexUserAgent())
	}

	inbound := openai.NewInboundTransformer()
	inboundRequest, err := httpclient.ReadHTTPRequest(rawReq)
	require.NoError(t, err)

	llmReq, err := inbound.TransformRequest(ctx, inboundRequest)
	require.NoError(t, err)
	llmReq.RawRequest = inboundRequest

	outboundRequest, err := outbound.TransformRequest(ctx, llmReq)
	require.NoError(t, err)

	outboundRequest = httpclient.MergeInboundRequest(outboundRequest, inboundRequest)
	outboundRequest, err = httpclient.FinalizeAuthHeaders(outboundRequest)
	require.NoError(t, err)

	return outboundRequest
}

func legacyCodexOriginator() string {
	return "codex" + "_cli_rs"
}

func legacyCodexUserAgent() string {
	return legacyCodexOriginator() + "/0.50.0 (macOS 14.0.0; arm64) Terminal"
}
