package orchestrator

import (
	"context"
	"net/http"
	"strings"

	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
)

const (
	bailianSessionIDHeader      = "X-Session-Id"
	bailianConversationIDHeader = "X-Conversation-Id"
)

var bailianBlockedOpenAIHeaders = []string{
	"Anthropic-Beta",
	"Anthropic-Version",
	"X-Conversation-Id",
	"X-Request-Id",
	"X-Run-Id",
}

func applyBailianRequestHeaders(outbound *PersistentOutboundTransformer) pipeline.Middleware {
	return pipeline.OnRawRequest("bailian-request-headers", func(ctx context.Context, request *httpclient.Request) (*httpclient.Request, error) {
		if outbound == nil || request == nil {
			return request, nil
		}

		currentChannel := outbound.GetCurrentChannel()
		if currentChannel == nil || currentChannel.Channel == nil || currentChannel.Type != channel.TypeBailian {
			return request, nil
		}

		if request.APIFormat != string(llm.APIFormatOpenAIChatCompletion) {
			return request, nil
		}

		if request.Headers == nil {
			request.Headers = make(http.Header)
		}

		for _, header := range bailianBlockedOpenAIHeaders {
			request.Headers.Del(header)
		}

		if outbound.state == nil || outbound.state.LlmRequest == nil || outbound.state.LlmRequest.RawRequest == nil {
			return request, nil
		}

		conversationID := strings.TrimSpace(outbound.state.LlmRequest.RawRequest.Headers.Get(bailianConversationIDHeader))
		if conversationID == "" {
			return request, nil
		}

		request.Headers.Set(bailianSessionIDHeader, conversationID)

		return request, nil
	})
}
