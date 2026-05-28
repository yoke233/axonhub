package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
)

const (
	codexWebsocketIdleTimeout     = 5 * time.Minute
	codexWebsocketHandshakeTO     = 30 * time.Second
	codexWebsocketRequestType     = "response.create"
	codexWebsocketCompletedEvent  = "response.completed"
	codexWebsocketDoneEvent       = "response.done"
	codexWebsocketDefaultErrorTyp = "websocket_error"
)

func (e *codexExecutor) doWebsocketStream(ctx context.Context, request *httpclient.Request) (streams.Stream[*httpclient.StreamEvent], error) {
	if request == nil {
		return nil, fmt.Errorf("codex websocket: request is nil")
	}
	defer request.ReleaseBody()

	wsURL, err := buildCodexResponsesWebsocketURL(request.URL)
	if err != nil {
		return nil, err
	}

	headers := request.Headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	if err := applyCodexWebsocketAuth(headers, request.Auth); err != nil {
		return nil, err
	}
	applyCodexResponsesWebsocketHeaders(headers)

	dialer := websocket.Dialer{
		HandshakeTimeout:  codexWebsocketHandshakeTO,
		EnableCompression: true,
		Proxy:             e.websocketProxyFunc(),
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return nil, websocketDialError(resp, err)
	}
	conn.EnableWriteCompression(false)

	body := buildCodexWebsocketRequestBody(request.Body)
	if err := conn.WriteMessage(websocket.TextMessage, body); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("codex websocket: write request: %w", err)
	}

	return &codexWebsocketStream{
		ctx:  ctx,
		conn: conn,
		req:  request,
	}, nil
}

func buildCodexResponsesWebsocketURL(httpURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(httpURL))
	if err != nil {
		return "", err
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("codex websocket: unsupported responses url scheme %q", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("codex websocket: responses url host is empty")
	}

	return parsed.String(), nil
}

func buildCodexWebsocketRequestBody(body []byte) []byte {
	if len(body) == 0 {
		body = []byte("{}")
	}

	out := bytes.Clone(body)
	out, _ = sjson.SetBytes(out, "type", codexWebsocketRequestType)
	out, _ = sjson.SetBytes(out, "stream", true)

	return out
}

func applyCodexResponsesWebsocketHeaders(headers http.Header) {
	if headers == nil {
		return
	}

	beta := strings.TrimSpace(headers.Get("OpenAI-Beta"))
	if beta == "" || !strings.Contains(beta, "responses_websockets=") {
		headers.Set("OpenAI-Beta", ResponsesWebsocketBetaHeaderValue)
	}
	if strings.TrimSpace(headers.Get("Accept")) == "" {
		headers.Set("Accept", "application/json")
	}
	headers.Del("Connection")
}

func applyCodexWebsocketAuth(headers http.Header, auth *httpclient.AuthConfig) error {
	if auth == nil {
		return nil
	}

	switch auth.Type {
	case httpclient.AuthTypeBearer:
		if auth.APIKey == "" {
			return fmt.Errorf("codex websocket: bearer token is required")
		}
		headers.Set("Authorization", "Bearer "+auth.APIKey)
	case httpclient.AuthTypeAPIKey:
		if strings.TrimSpace(auth.HeaderKey) == "" {
			return fmt.Errorf("codex websocket: api key header is required")
		}
		headers.Set(auth.HeaderKey, auth.APIKey)
	default:
		return fmt.Errorf("codex websocket: unsupported auth type %q", auth.Type)
	}

	return nil
}

type codexWebsocketProxyProvider interface {
	ProxyFunc() func(*http.Request) (*url.URL, error)
}

func (e *codexExecutor) websocketProxyFunc() func(*http.Request) (*url.URL, error) {
	if e != nil {
		if provider, ok := e.inner.(codexWebsocketProxyProvider); ok {
			return provider.ProxyFunc()
		}
	}

	return http.ProxyFromEnvironment
}

func websocketDialError(resp *http.Response, cause error) error {
	if resp == nil {
		return fmt.Errorf("codex websocket: dial: %w", cause)
	}
	defer resp.Body.Close()

	rawURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		rawURL = resp.Request.URL.String()
	}

	return &httpclient.Error{
		Method:     http.MethodGet,
		URL:        rawURL,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Headers:    resp.Header.Clone(),
		Body:       []byte(cause.Error()),
	}
}

type codexWebsocketStream struct {
	ctx       context.Context
	conn      *websocket.Conn
	req       *httpclient.Request
	current   *httpclient.StreamEvent
	err       error
	completed bool
	closed    bool
}

var _ streams.Stream[*httpclient.StreamEvent] = (*codexWebsocketStream)(nil)

func (s *codexWebsocketStream) Next() bool {
	if s.err != nil || s.completed || s.closed {
		return false
	}

	for {
		if s.ctx != nil && s.ctx.Err() != nil {
			s.err = s.ctx.Err()
			return false
		}

		_ = s.conn.SetReadDeadline(time.Now().Add(codexWebsocketIdleTimeout))
		msgType, payload, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) && s.completed {
				return false
			}
			s.err = fmt.Errorf("codex websocket: read response: %w", err)
			return false
		}
		if msgType != websocket.TextMessage {
			continue
		}

		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 {
			continue
		}
		if wsErr, ok := parseCodexWebsocketError(payload); ok {
			s.err = wsErr
			return false
		}

		eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		if eventType == "" {
			eventType = "message"
		}
		s.current = &httpclient.StreamEvent{
			Type: eventType,
			Data: payload,
		}
		if eventType == codexWebsocketCompletedEvent || eventType == codexWebsocketDoneEvent {
			s.completed = true
		}

		return true
	}
}

func (s *codexWebsocketStream) Current() *httpclient.StreamEvent {
	return s.current
}

func (s *codexWebsocketStream) Err() error {
	return s.err
}

func (s *codexWebsocketStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn == nil {
		return nil
	}

	return s.conn.Close()
}

func parseCodexWebsocketError(payload []byte) (error, bool) {
	if len(payload) == 0 {
		return nil, false
	}

	dataType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if dataType != "error" && dataType != "response.failed" && dataType != "response.error" {
		return nil, false
	}

	message := firstNonEmptyString(
		gjson.GetBytes(payload, "error.message").String(),
		gjson.GetBytes(payload, "message").String(),
		string(payload),
	)
	errType := firstNonEmptyString(
		gjson.GetBytes(payload, "error.type").String(),
		gjson.GetBytes(payload, "error.code").String(),
		dataType,
		codexWebsocketDefaultErrorTyp,
	)

	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    gjson.GetBytes(payload, "error.code").String(),
		},
	})

	return &httpclient.Error{
		Method:     http.MethodGet,
		URL:        "",
		StatusCode: http.StatusBadGateway,
		Status:     http.StatusText(http.StatusBadGateway),
		Body:       body,
	}, true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}

	return ""
}
