package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/internal/server/orchestrator"
	"github.com/looplj/axonhub/llm/httpclient"
)

const (
	responsesWebsocketRequestCreate = "response.create"
	responsesWebsocketRequestAppend = "response.append"
	responsesWebsocketDoneMarker    = "[DONE]"
)

var responsesWebsocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(*http.Request) bool {
		return true
	},
}

// ResponsesWebsocket handles OpenAI Responses websocket clients.
func (handlers *OpenAIHandlers) ResponsesWebsocket(c *gin.Context) {
	conn, err := responsesWebsocketUpgrader.Upgrade(c.Writer, c.Request, websocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	defer conn.Close()

	ctx := c.Request.Context()
	var lastRequest []byte

	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) {
				log.Debug(ctx, "responses websocket client disconnected", log.Cause(err))
			} else {
				log.Warn(ctx, "responses websocket read failed", log.Cause(err))
			}
			return
		}
		if msgType != websocket.TextMessage && msgType != websocket.BinaryMessage {
			continue
		}

		requestBody, updatedLastRequest, normalizeErr := normalizeResponsesWebsocketRequest(payload, lastRequest)
		if normalizeErr != nil {
			if writeErr := writeResponsesWebsocketError(conn, http.StatusBadRequest, normalizeErr); writeErr != nil {
				log.Warn(ctx, "responses websocket write error failed", log.Cause(writeErr))
				return
			}
			continue
		}
		lastRequest = updatedLastRequest

		genericReq := &httpclient.Request{
			Method:  http.MethodPost,
			URL:     "/v1/responses",
			Path:    "/v1/responses",
			Headers: mergeWebsocketRequestHeaders(c.Request.Header),
			Body:    requestBody,
		}

		result, err := handlers.ResponseCompletionHandlers.ChatCompletionOrchestrator.Process(ctx, genericReq)
		if err != nil {
			httpErr := handlers.ResponseCompletionHandlers.ChatCompletionOrchestrator.Inbound.TransformError(ctx, err)
			if writeErr := conn.WriteMessage(websocket.TextMessage, httpErr.Body); writeErr != nil {
				log.Warn(ctx, "responses websocket write transformed error failed", log.Cause(writeErr))
				return
			}
			continue
		}

		if err := forwardResponsesWebsocketResult(ctx, conn, result); err != nil {
			log.Warn(ctx, "responses websocket forward failed", log.Cause(err))
			return
		}
	}
}

func websocketUpgradeHeaders(req *http.Request) http.Header {
	headers := http.Header{}
	if req == nil {
		return headers
	}
	if turnState := strings.TrimSpace(req.Header.Get("x-codex-turn-state")); turnState != "" {
		headers.Set("x-codex-turn-state", turnState)
	}

	return headers
}

func mergeWebsocketRequestHeaders(src http.Header) http.Header {
	headers := src.Clone()
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	headers.Del("Connection")
	headers.Del("Upgrade")
	headers.Del("Sec-Websocket-Key")
	headers.Del("Sec-Websocket-Version")
	headers.Del("Sec-Websocket-Extensions")

	return headers
}

func normalizeResponsesWebsocketRequest(rawJSON []byte, lastRequest []byte) ([]byte, []byte, error) {
	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return nil, lastRequest, errors.New("empty websocket payload")
	}
	if !gjson.ValidBytes(rawJSON) {
		return nil, lastRequest, errors.New("websocket payload must be valid JSON")
	}

	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case responsesWebsocketRequestCreate, responsesWebsocketRequestAppend:
	default:
		return nil, lastRequest, fmt.Errorf("unsupported websocket request type: %s", requestType)
	}

	normalized, err := sjson.DeleteBytes(rawJSON, "type")
	if err != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)

	if !gjson.GetBytes(normalized, "input").Exists() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte("[]"))
	}

	if strings.TrimSpace(gjson.GetBytes(normalized, "model").String()) == "" {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName == "" {
			return nil, lastRequest, errors.New("missing model in response.create request")
		}
		normalized, _ = sjson.SetBytes(normalized, "model", modelName)
	}

	if !gjson.GetBytes(normalized, "instructions").Exists() {
		if instructions := gjson.GetBytes(lastRequest, "instructions"); instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}

	return normalized, bytes.Clone(normalized), nil
}

func forwardResponsesWebsocketResult(ctx context.Context, conn *websocket.Conn, result orchestrator.ChatCompletionResult) error {
	if result.ChatCompletion != nil {
		return conn.WriteMessage(websocket.TextMessage, result.ChatCompletion.Body)
	}
	if result.ChatCompletionStream == nil {
		return nil
	}
	defer result.ChatCompletionStream.Close()

	for result.ChatCompletionStream.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		event := result.ChatCompletionStream.Current()
		if event == nil || bytes.Equal(bytes.TrimSpace(event.Data), []byte(responsesWebsocketDoneMarker)) {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, event.Data); err != nil {
			return err
		}
	}

	return result.ChatCompletionStream.Err()
}

func writeResponsesWebsocketError(conn *websocket.Conn, statusCode int, err error) error {
	body, marshalErr := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"message": err.Error(),
			"type":    "invalid_request_error",
			"code":    statusCode,
		},
	})
	if marshalErr != nil {
		return marshalErr
	}

	return conn.WriteMessage(websocket.TextMessage, body)
}
