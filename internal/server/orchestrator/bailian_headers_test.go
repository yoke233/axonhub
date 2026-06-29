package orchestrator

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestBailianRequestHeaders_OpenAIChatCompletions(t *testing.T) {
	outbound := newTestOutbound(&biz.Channel{
		Channel: &ent.Channel{
			Type: channel.TypeBailian,
		},
	})
	outbound.state.LlmRequest = &llm.Request{
		RawRequest: &httpclient.Request{
			Headers: http.Header{
				"X-Conversation-Id": []string{"conv-123"},
			},
		},
	}

	request := &httpclient.Request{
		APIFormat: string(llm.APIFormatOpenAIChatCompletion),
		Headers: http.Header{
			"Anthropic-Beta":    []string{"beta"},
			"Anthropic-Version": []string{"2023-06-01"},
			"X-Conversation-Id": []string{"conv-123"},
			"X-Request-Id":      []string{"req-123"},
			"X-Run-Id":          []string{"run-123"},
			"X-Session-Id":      []string{"old-session"},
		},
	}

	got, err := applyBailianRequestHeaders(outbound).OnOutboundRawRequest(t.Context(), request)
	require.NoError(t, err)
	require.Empty(t, got.Headers.Get("Anthropic-Beta"))
	require.Empty(t, got.Headers.Get("Anthropic-Version"))
	require.Empty(t, got.Headers.Get("X-Conversation-Id"))
	require.Empty(t, got.Headers.Get("X-Request-Id"))
	require.Empty(t, got.Headers.Get("X-Run-Id"))
	require.Equal(t, "conv-123", got.Headers.Get("X-Session-Id"))
}

func TestBailianRequestHeaders_OnlyOpenAIChatCompletions(t *testing.T) {
	outbound := newTestOutbound(&biz.Channel{
		Channel: &ent.Channel{
			Type: channel.TypeBailian,
		},
	})
	outbound.state.LlmRequest = &llm.Request{
		RawRequest: &httpclient.Request{
			Headers: http.Header{
				"X-Conversation-Id": []string{"conv-123"},
			},
		},
	}

	request := &httpclient.Request{
		APIFormat: string(llm.APIFormatAnthropicMessage),
		Headers: http.Header{
			"Anthropic-Version": []string{"2023-06-01"},
			"X-Conversation-Id": []string{"conv-123"},
		},
	}

	got, err := applyBailianRequestHeaders(outbound).OnOutboundRawRequest(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, "2023-06-01", got.Headers.Get("Anthropic-Version"))
	require.Equal(t, "conv-123", got.Headers.Get("X-Conversation-Id"))
	require.Empty(t, got.Headers.Get("X-Session-Id"))
}
