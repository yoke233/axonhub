package codex

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/samber/lo"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/auth"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

const (
	codexBaseURL = "https://chatgpt.com/backend-api/codex#"
	codexAPIURL  = "https://chatgpt.com/backend-api/codex/responses"
)

// OutboundTransformer implements transformer.Outbound for Codex proxy.
// It always talks to the Codex Responses upstream and adapts requests accordingly.
//
//nolint:containedctx // It is used as a transformer.
type OutboundTransformer struct {
	tokens    oauth.TokenGetter
	transport string

	// installationID is the channel-scoped fallback used when the caller did not
	// supply 'x-codex-installation-id'. Mirrors codex_cli_rs ~/.codex/installation_id.
	installationID string

	// reuse existing Responses outbound for payload building.
	responsesOutbound *responses.OutboundTransformer
	sessionIDCache    SessionIDCache
	sessionIDScopeKey string

	executorMu         sync.Mutex
	webSocketExecutors map[pipeline.Executor]*responses.WebSocketExecutor
}

var (
	_ transformer.Outbound               = (*OutboundTransformer)(nil)
	_ pipeline.ChannelCustomizedExecutor = (*OutboundTransformer)(nil)
)

type Params struct {
	TokenProvider   oauth.TokenGetter
	BaseURL         string
	AccountIdentity string
	SessionIDCache  SessionIDCache
	Transport       string
	// InstallationID is the channel-scoped fallback "device" UUID. Mirrors the value
	// real codex_cli_rs reads from ~/.codex/installation_id and sends in both the
	// 'x-codex-installation-id' header and body.client_metadata.
	// When the inbound caller already supplies the header, the caller value wins.
	InstallationID string
}

func NewOutboundTransformer(params Params) (*OutboundTransformer, error) {
	if params.TokenProvider == nil {
		return nil, errors.New("token provider is required")
	}

	baseURL := params.BaseURL
	// Compatibility with old codex channel base url.
	if baseURL == "" || baseURL == "https://api.openai.com/v1" {
		baseURL = codexBaseURL
	}

	// The underlying responses outbound requires baseURL/apiKey. We only need its request body logic.
	// Use a dummy config and then override URL/auth.
	ro, err := responses.NewOutboundTransformerWithConfig(&responses.Config{
		BaseURL:        baseURL,
		APIKeyProvider: auth.NewStaticKeyProvider("dummy"),
		Transport:      params.Transport,
	})
	if err != nil {
		return nil, err
	}

	return &OutboundTransformer{
		tokens:            params.TokenProvider,
		transport:         params.Transport,
		installationID:    params.InstallationID,
		responsesOutbound: ro,
		sessionIDCache:    resolveSessionIDCache(params.SessionIDCache),
		sessionIDScopeKey: resolveSessionIDScopeKey(params.AccountIdentity),
	}, nil
}

func (t *OutboundTransformer) APIFormat() llm.APIFormat {
	return llm.APIFormatOpenAIResponse
}

func (t *OutboundTransformer) TokenProvider() oauth.TokenGetter {
	if t == nil {
		return nil
	}

	return t.tokens
}

func (t *OutboundTransformer) TransformError(ctx context.Context, rawErr *httpclient.Error) *llm.ResponseError {
	return t.responsesOutbound.TransformError(ctx, rawErr)
}

func (t *OutboundTransformer) CanRetryUnauthorized(err error) bool {
	return supportsUnauthorizedRetry(t.tokens, err)
}

func (t *OutboundTransformer) PrepareForUnauthorizedRetry(ctx context.Context) error {
	return refreshUnauthorizedCredentials(ctx, t.tokens)
}

func (t *OutboundTransformer) TransformRequest(ctx context.Context, llmReq *llm.Request) (*httpclient.Request, error) {
	if llmReq == nil {
		return nil, errors.New("request is nil")
	}

	rawSessionID := ""
	rawOriginator := ""
	rawUserAgent := ""
	rawTurnMetadata := ""

	var rawHeaders http.Header

	if llmReq.RawRequest != nil && llmReq.RawRequest.Headers != nil {
		rawHeaders = llmReq.RawRequest.Headers
		rawSessionID = llmReq.RawRequest.Headers.Get(SessionHeader)
		rawOriginator = llmReq.RawRequest.Headers.Get("Originator")
		rawUserAgent = llmReq.RawRequest.Headers.Get("User-Agent")
		rawTurnMetadata = llmReq.RawRequest.Headers.Get(TurnMetadataHeader)
	}
	isCodexCaller := HasCodexCallerTraits(rawHeaders)

	creds, err := getRequestCredentials(ctx, t.tokens)
	if err != nil {
		return nil, err
	}

	// Parse account ID from access token JWT.
	accountID := ExtractChatGPTAccountIDFromJWT(creds.AccessToken)

	// Clone request so we do not mutate upstream pipeline state.
	reqCopy := *llmReq
	originalRequestType := reqCopy.RequestType
	if reqCopy.RequestType == llm.RequestTypeImage {
		normalizeCodexImageRequest(&reqCopy)
	}

	// For non-Codex callers we add Codex-compatible defaults. For requests that
	// already look like real Codex clients, stay in passthrough-first mode.
	if !isCodexCaller {
		//nolint:exhaustive // We only care about compact requests.
		switch reqCopy.RequestType {
		case llm.RequestTypeCompact:
			if reqCopy.Stream == nil {
				reqCopy.Stream = lo.ToPtr(false)
			}
		default:
			if reqCopy.Stream == nil {
				reqCopy.Stream = lo.ToPtr(true)
			}
		}
		if reqCopy.Store == nil {
			reqCopy.Store = lo.ToPtr(false)
		}
		if reqCopy.ParallelToolCalls == nil {
			reqCopy.ParallelToolCalls = lo.ToPtr(true)
		}
		if reqCopy.TransformerMetadata == nil {
			reqCopy.TransformerMetadata = map[string]any{}
		}
		if _, ok := reqCopy.TransformerMetadata["include"]; !ok {
			reqCopy.TransformerMetadata["include"] = []string{"reasoning.encrypted_content"}
		}
		if reqCopy.ReasoningSummary == nil || *reqCopy.ReasoningSummary == "" {
			reqCopy.ReasoningSummary = lo.ToPtr("auto")
		}
	} else if reqCopy.Stream == nil && reqCopy.RequestType != llm.RequestTypeCompact {
		// Stream transport is still required for non-compact Codex requests.
		reqCopy.Stream = lo.ToPtr(true)
	}

	// Preserve caller-supplied conversation fields; only synthesize fallback
	// values when the inbound request does not already look like Codex.
	sessionID, promptCacheKey := resolveConversationFields(
		ctx,
		rawSessionID,
		rawTurnMetadata,
		reqCopy.TransformerMetadata,
		reqCopy.PromptCacheKey,
		t.sessionIDCache,
		t.sessionIDScopeKey,
		!isCodexCaller,
	)
	reqCopy.PromptCacheKey = promptCacheKey

	// Codex Responses rejects token limit fields and user metadata.
	reqCopy.MaxCompletionTokens = nil
	reqCopy.MaxTokens = nil
	reqCopy.Metadata = nil

	reqCopy.TransformOptions.ArrayInputs = lo.ToPtr(true)

	hreq, err := t.responsesOutbound.TransformRequest(ctx, &reqCopy)
	if err != nil {
		return nil, err
	}

	keepPreviousResponseID := reqCopy.RequestType != llm.RequestTypeCompact && reqCopy.PreviousResponseID != nil
	hreq.Body = sanitizeCodexRequestBody(hreq.Body, promptCacheKey != nil, keepPreviousResponseID)
	if originalRequestType == llm.RequestTypeImage {
		hreq.Body = injectCodexImageToolChoice(hreq.Body)
		hreq.RequestType = string(llm.RequestTypeImage)
	}

	// Overwrite auth.
	hreq.Auth = &httpclient.AuthConfig{Type: httpclient.AuthTypeBearer, APIKey: creds.AccessToken}
	// Compact requests expect JSON response, others expect SSE stream.
	if llmReq.RequestType == llm.RequestTypeCompact {
		hreq.Headers.Set("Accept", "application/json")
	} else {
		hreq.Headers.Set("Accept", "text/event-stream")
	}
	hreq.Headers.Set("Connection", "Keep-Alive")
	hreq.Headers.Del("User-Agent")

	if rawOriginator != "" {
		hreq.Headers.Set("Originator", rawOriginator)
	} else {
		hreq.Headers.Set("Originator", DefaultOriginator)
	}

	// Passthrough-first: real codex CLI users already have a proper UA. Fall back to a
	// codex_cli_rs-shaped UA only when the caller did not supply one. Avoid leaking the
	// Go default UA "Go-http-client/1.1".
	if rawUserAgent != "" {
		hreq.Headers.Set("User-Agent", rawUserAgent)
	} else {
		hreq.Headers.Set("User-Agent", BuildDefaultCodexUserAgent())
	}

	for _, header := range PassthroughHeaders {
		if value := rawHeaders.Get(header); value != "" {
			hreq.Headers.Set(header, value)
		}
	}

	if sessionID != "" {
		hreq.Headers.Set(SessionHeader, sessionID)
	}

	if accountID != "" {
		hreq.Headers.Set("Chatgpt-Account-Id", accountID)
	}

	// Resolve installation id (passthrough-first, channel fallback) and ensure both the
	// header and body.client_metadata carry the same value, matching real codex_cli_rs.
	installationID := resolveInstallationID(rawHeaders.Get(InstallationIDHeader), t.installationID)
	if installationID != "" {
		hreq.Headers.Set(InstallationIDHeader, installationID)
		hreq.Body = injectInstallationIDIntoBody(hreq.Body, installationID)
	}

	return hreq, nil
}

func resolveSessionIDCache(cache SessionIDCache) SessionIDCache {
	if cache != nil {
		return cache
	}

	return newMemorySessionIDCache()
}

func resolveSessionIDScopeKey(accountIdentity string) string {
	accountIdentity = strings.TrimSpace(accountIdentity)
	if accountIdentity == "" {
		return "default"
	}

	return accountIdentity
}

func (t *OutboundTransformer) TransformResponse(ctx context.Context, httpResp *httpclient.Response) (*llm.Response, error) {
	// Codex upstream returns Responses API response.
	resp, err := t.responsesOutbound.TransformResponse(ctx, httpResp)
	if err != nil {
		return nil, err
	}
	if httpResp != nil && httpResp.Request != nil && httpResp.Request.RequestType == string(llm.RequestTypeImage) {
		convertCodexResponseToImage(resp)
	}

	return resp, nil
}

func (t *OutboundTransformer) TransformStream(ctx context.Context, req *httpclient.Request, streamIn streams.Stream[*httpclient.StreamEvent]) (streams.Stream[*llm.Response], error) {
	return t.responsesOutbound.TransformStream(ctx, req, streamIn)
}

func (t *OutboundTransformer) AggregateStreamChunks(ctx context.Context, req *httpclient.Request, chunks []*httpclient.StreamEvent) ([]byte, llm.ResponseMeta, error) {
	return t.responsesOutbound.AggregateStreamChunks(ctx, req, chunks)
}

func (t *OutboundTransformer) CustomizeExecutor(executor pipeline.Executor) pipeline.Executor {
	inner := executor
	if t != nil && t.transport == responses.TransportWebSocket {
		inner = t.customizeWebSocketExecutor(inner)
	}

	return &codexExecutor{
		inner:       inner,
		transformer: t,
	}
}

func (t *OutboundTransformer) customizeWebSocketExecutor(executor pipeline.Executor) pipeline.Executor {
	if !responses.ExecutorComparable(executor) {
		return responses.NewWebSocketExecutor(executor)
	}

	t.executorMu.Lock()
	defer t.executorMu.Unlock()

	if t.webSocketExecutors == nil {
		t.webSocketExecutors = make(map[pipeline.Executor]*responses.WebSocketExecutor)
	}
	if cached, ok := t.webSocketExecutors[executor]; ok {
		return cached
	}

	webSocketExecutor := responses.NewWebSocketExecutor(executor)
	t.webSocketExecutors[executor] = webSocketExecutor

	return webSocketExecutor
}

func (t *OutboundTransformer) Stop() {
	if t == nil {
		return
	}

	t.executorMu.Lock()
	executors := make([]*responses.WebSocketExecutor, 0, len(t.webSocketExecutors))
	for _, executor := range t.webSocketExecutors {
		executors = append(executors, executor)
	}
	t.webSocketExecutors = nil
	t.executorMu.Unlock()

	for _, executor := range executors {
		_ = executor.Close()
	}
}

type codexExecutor struct {
	inner       pipeline.Executor
	transformer *OutboundTransformer
}

func (e *codexExecutor) Do(ctx context.Context, request *httpclient.Request) (*httpclient.Response, error) {
	if request.RequestType == string(llm.RequestTypeCompact) {
		return e.inner.Do(ctx, request)
	}

	stream, err := e.inner.DoStream(ctx, request)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = stream.Close()
	}()

	var chunks []*httpclient.StreamEvent

	for stream.Next() {
		ev := stream.Current()
		if ev == nil {
			continue
		}

		chunks = append(chunks, &httpclient.StreamEvent{
			Type:        ev.Type,
			LastEventID: ev.LastEventID,
			Data:        append([]byte(nil), ev.Data...),
		})
	}

	if err := stream.Err(); err != nil {
		return nil, err
	}
	if err := responses.TopLevelWebSocketError(chunks); err != nil {
		return nil, err
	}

	body, _, err := e.transformer.AggregateStreamChunks(ctx, request, chunks)
	if err != nil {
		return nil, err
	}

	return &httpclient.Response{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:    body,
		Request: request,
	}, nil
}

func (e *codexExecutor) DoStream(ctx context.Context, request *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	if request != nil && request.RequestType != string(llm.RequestTypeCompact) {
		return e.doWebsocketStream(ctx, request)
	}

	return e.inner.DoStream(ctx, request)
}
