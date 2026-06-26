package bailian

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/auth"
	"github.com/looplj/axonhub/llm/httpclient"
	anthropictransformer "github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
)

func TestBailianTransformRequest_MergeConsecutiveToolCalls(t *testing.T) {
	transformer, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	userContent := "hi"
	toolOneArgs := "{}"
	toolTwoArgs := "{}"
	out1 := "out1"
	out2 := "out2"
	callOne := "call_1"
	callTwo := "call_2"

	req := &llm.Request{
		Model: "qwen-max",
		Messages: []llm.Message{
			{Role: "user", Content: llm.MessageContent{Content: &userContent}},
			{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{
					{
						ID:   callOne,
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "tool_one",
							Arguments: toolOneArgs,
						},
					},
				},
			},
			{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{
					{
						ID:   callTwo,
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "tool_two",
							Arguments: toolTwoArgs,
						},
					},
				},
			},
			{
				Role:       "tool",
				ToolCallID: &callOne,
				Content:    llm.MessageContent{Content: &out1},
			},
			{
				Role:       "tool",
				ToolCallID: &callTwo,
				Content:    llm.MessageContent{Content: &out2},
			},
		},
	}

	httpReq, err := transformer.TransformRequest(context.Background(), req)
	require.NoError(t, err)

	var oaiReq openai.Request
	require.NoError(t, json.Unmarshal(httpReq.Body, &oaiReq))
	require.Len(t, oaiReq.Messages, 4)
	require.Equal(t, "user", oaiReq.Messages[0].Role)
	require.Equal(t, "assistant", oaiReq.Messages[1].Role)
	require.Len(t, oaiReq.Messages[1].ToolCalls, 2)
	require.Equal(t, callOne, oaiReq.Messages[1].ToolCalls[0].ID)
	require.Equal(t, callTwo, oaiReq.Messages[1].ToolCalls[1].ID)
	require.Equal(t, "tool", oaiReq.Messages[2].Role)
	require.NotNil(t, oaiReq.Messages[2].ToolCallID)
	require.Equal(t, callOne, *oaiReq.Messages[2].ToolCallID)
	require.Equal(t, "tool", oaiReq.Messages[3].Role)
	require.NotNil(t, oaiReq.Messages[3].ToolCallID)
	require.Equal(t, callTwo, *oaiReq.Messages[3].ToolCallID)
}

func TestBailianTransformRequest_ToolMessageImageContentStripped(t *testing.T) {
	transformer, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	callID := "call_view_image"
	req := &llm.Request{
		Model: "qwen3.7-max",
		Messages: []llm.Message{
			{Role: "user", Content: llm.MessageContent{Content: lo.ToPtr("inspect image")}},
			{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID:   callID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "ViewImage",
						Arguments: `{"path":"complaint_p1.png"}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: &callID,
				Content: llm.MessageContent{MultipleContent: []llm.MessageContentPart{
					{Type: "text", Text: lo.ToPtr("Image: complaint_p1.png (image/png, 766 KB)")},
					{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:image/png;base64,AAAA"}},
				}},
			},
		},
	}

	httpReq, err := transformer.TransformRequest(context.Background(), req)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.Equal(t, "Image: complaint_p1.png (image/png, 766 KB)", gjson.Get(body, "messages.2.content").String())
	require.False(t, gjson.Get(body, "messages.2.content.0.type").Exists())
	require.NotContains(t, body, "image_url")
}

func TestBailianTransformRequest_OpenAIToolContentCacheControlPreserved(t *testing.T) {
	inbound := openai.NewInboundTransformer()
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{
			"model": "qwen3.7-max",
			"prompt_cache_key": "should-be-removed-when-explicit-cache-is-used",
			"messages": [
				{"role": "user", "content": "read project context"},
				{
					"role": "assistant",
					"content": null,
					"tool_calls": [
						{
							"id": "call_cache_tool",
							"type": "function",
							"function": {
								"name": "read_context",
								"arguments": "{}"
							}
						}
					]
				},
				{
					"role": "tool",
					"tool_call_id": "call_cache_tool",
					"content": [
						{
							"type": "text",
							"text": "stable tool result",
							"cache_control": {"type": "ephemeral"}
						}
					]
				},
				{"role": "user", "content": "continue"}
			]
		}`),
	})
	require.NoError(t, err)

	outbound, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.False(t, gjson.Get(body, "prompt_cache_key").Exists())
	require.Equal(t, "tool", gjson.Get(body, "messages.2.role").String())
	require.Equal(t, "stable tool result", gjson.Get(body, "messages.2.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.2.content.0.cache_control.type").String())
}

func TestBailianTransformRequest_AnthropicToolResultCacheControlPreservedOnToolMessage(t *testing.T) {
	inbound := anthropictransformer.NewInboundTransformer()
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{
			"model": "claude-sonnet-4-5",
			"max_tokens": 1024,
			"messages": [
				{"role": "user", "content": "read project context"},
				{
					"role": "assistant",
					"content": [
						{
							"type": "tool_use",
							"id": "toolu_cache_context",
							"name": "read_context",
							"input": {"target": "project"}
						}
					]
				},
				{
					"role": "user",
					"content": [
						{
							"type": "tool_result",
							"tool_use_id": "toolu_cache_context",
							"content": "stable tool result",
							"cache_control": {"type": "ephemeral"}
						}
					]
				},
				{"role": "user", "content": "continue"}
			]
		}`),
	})
	require.NoError(t, err)
	llmReq.Model = "qwen3.7-max"

	outbound, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.False(t, gjson.Get(body, "prompt_cache_key").Exists())
	require.Equal(t, "tool", gjson.Get(body, "messages.2.role").String())
	require.Equal(t, "toolu_cache_context", gjson.Get(body, "messages.2.tool_call_id").String())
	require.Equal(t, "stable tool result", gjson.Get(body, "messages.2.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.2.content.0.cache_control.type").String())
}

func TestBailianTransformRequest_ToolMessageCacheControlConvertsStringContentToParts(t *testing.T) {
	transformer, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	callID := "call_cache_tool"
	req := &llm.Request{
		Model:          "qwen3.7-max",
		PromptCacheKey: lo.ToPtr("should-be-removed-when-explicit-cache-is-used"),
		Messages: []llm.Message{
			{Role: "user", Content: llm.MessageContent{Content: lo.ToPtr("read project context")}},
			{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID:   callID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "read_context",
						Arguments: "{}",
					},
				}},
			},
			{
				Role:         "tool",
				ToolCallID:   &callID,
				Content:      llm.MessageContent{Content: lo.ToPtr("stable tool result")},
				CacheControl: &llm.CacheControl{Type: "ephemeral"},
			},
		},
	}

	httpReq, err := transformer.TransformRequest(context.Background(), req)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.False(t, gjson.Get(body, "prompt_cache_key").Exists())
	require.Equal(t, "stable tool result", gjson.Get(body, "messages.2.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.2.content.0.cache_control.type").String())
}

func TestBailianTransformRequest_DeepSeekV4Thinking(t *testing.T) {
	transformer, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	tests := []struct {
		name                 string
		reasoningEffort      string
		withTools            bool
		wantEnableThinking   bool
		wantReasoningEffort  string
		wantReasoningPresent bool
	}{
		{
			name:                 "plain request defaults to high",
			wantEnableThinking:   true,
			wantReasoningEffort:  "high",
			wantReasoningPresent: true,
		},
		{
			name:                 "agentic request defaults to max",
			withTools:            true,
			wantEnableThinking:   true,
			wantReasoningEffort:  "max",
			wantReasoningPresent: true,
		},
		{
			name:                 "medium maps to high",
			reasoningEffort:      "medium",
			wantEnableThinking:   true,
			wantReasoningEffort:  "high",
			wantReasoningPresent: true,
		},
		{
			name:                 "none disables thinking",
			reasoningEffort:      "none",
			wantEnableThinking:   false,
			wantReasoningPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &llm.Request{
				Model:           "deepseek-v4-pro",
				ReasoningEffort: tt.reasoningEffort,
				Messages:        []llm.Message{{Role: "user", Content: llm.MessageContent{Content: lo.ToPtr("hi")}}},
			}
			if tt.withTools {
				req.Tools = []llm.Tool{{
					Type: llm.ToolTypeFunction,
					Function: llm.Function{
						Name:       "get_weather",
						Parameters: []byte(`{"type":"object"}`),
					},
				}}
			}

			httpReq, err := transformer.TransformRequest(context.Background(), req)
			require.NoError(t, err)

			body := string(httpReq.Body)
			require.False(t, gjson.Get(body, "thinking").Exists())
			require.Equal(t, tt.wantEnableThinking, gjson.Get(body, "enable_thinking").Bool())
			if tt.wantReasoningPresent {
				require.Equal(t, tt.wantReasoningEffort, gjson.Get(body, "reasoning_effort").String())
			} else {
				require.False(t, gjson.Get(body, "reasoning_effort").Exists())
			}
		})
	}
}

func TestBailianTransformRequest_AnthropicThinkingMetadata(t *testing.T) {
	transformer, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	tests := []struct {
		name               string
		thinkingType       string
		reasoningEffort    string
		wantExists         bool
		wantEnableThinking bool
	}{
		{
			name:               "enabled thinking maps to DashScope enable_thinking",
			thinkingType:       "enabled",
			reasoningEffort:    "high",
			wantExists:         true,
			wantEnableThinking: true,
		},
		{
			name:               "adaptive thinking maps to DashScope enable_thinking",
			thinkingType:       "adaptive",
			reasoningEffort:    "high",
			wantExists:         true,
			wantEnableThinking: true,
		},
		{
			name:               "disabled thinking maps to DashScope disable thinking",
			thinkingType:       "disabled",
			reasoningEffort:    "none",
			wantExists:         true,
			wantEnableThinking: false,
		},
		{
			name:            "missing Anthropic thinking metadata leaves DashScope field absent",
			reasoningEffort: "high",
			wantExists:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &llm.Request{
				Model:           "qwen3.7-max-2026-06-08",
				ReasoningEffort: tt.reasoningEffort,
				Messages:        []llm.Message{{Role: "user", Content: llm.MessageContent{Content: lo.ToPtr("hi")}}},
			}
			if tt.thinkingType != "" {
				req.TransformerMetadata = map[string]any{
					anthropictransformer.TransformerMetadataKeyThinkingType: tt.thinkingType,
				}
			}

			httpReq, err := transformer.TransformRequest(context.Background(), req)
			require.NoError(t, err)

			body := string(httpReq.Body)
			enableThinking := gjson.Get(body, "enable_thinking")
			require.Equal(t, tt.wantExists, enableThinking.Exists())
			if tt.wantExists {
				require.Equal(t, tt.wantEnableThinking, enableThinking.Bool())
				require.False(t, gjson.Get(body, "reasoning_effort").Exists())
			}
		})
	}
}

func TestBailianTransformRequest_AnthropicInboundThinkingToDashScope(t *testing.T) {
	inbound := anthropictransformer.NewInboundTransformer()
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{
			"model": "claude-sonnet-4-5",
			"max_tokens": 1024,
			"thinking": {
				"type": "enabled",
				"budget_tokens": 1024
			},
			"messages": [
				{"role": "user", "content": "hi"}
			]
		}`),
	})
	require.NoError(t, err)

	llmReq.Model = "qwen3.7-max-2026-06-08"

	outbound, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.True(t, gjson.Get(body, "enable_thinking").Bool())
	require.False(t, gjson.Get(body, "reasoning_effort").Exists())
}

func TestBailianTransformRequest_AnthropicInboundCacheControl(t *testing.T) {
	inbound := anthropictransformer.NewInboundTransformer()
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{
			"model": "claude-sonnet-4-5",
			"max_tokens": 1024,
			"system": [
				{"type": "text", "text": "stable system prompt", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
			],
			"messages": [
				{
					"role": "user",
					"content": [
						{"type": "text", "text": "stable user context", "cache_control": {"type": "ephemeral"}},
						{"type": "text", "text": "dynamic question"}
					]
				}
			]
		}`),
	})
	require.NoError(t, err)

	llmReq.Model = "qwen3.7-max"

	outbound, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.False(t, gjson.Get(body, "prompt_cache_key").Exists())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.0.content.0.cache_control.type").String())
	require.False(t, gjson.Get(body, "messages.0.content.0.cache_control.ttl").Exists())
	require.Equal(t, "stable system prompt", gjson.Get(body, "messages.0.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.1.content.0.cache_control.type").String())
	require.Equal(t, "stable user context", gjson.Get(body, "messages.1.content.0.text").String())
	require.False(t, gjson.Get(body, "messages.1.content.1.cache_control").Exists())
}

func TestBailianTransformRequest_AnthropicTopLevelCacheControlFallsBackToLastMessage(t *testing.T) {
	inbound := anthropictransformer.NewInboundTransformer()
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{
			"model": "claude-sonnet-4-5",
			"max_tokens": 1024,
			"cache_control": {"type": "ephemeral"},
			"system": "stable system prompt",
			"messages": [
				{"role": "user", "content": "latest question"}
			]
		}`),
	})
	require.NoError(t, err)

	llmReq.Model = "qwen3.7-max"

	outbound, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.False(t, gjson.Get(body, "prompt_cache_key").Exists())
	require.Equal(t, "latest question", gjson.Get(body, "messages.1.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.1.content.0.cache_control.type").String())
}

func TestBailianTransformRequest_AnthropicToolCacheControlUsesMessageAnchor(t *testing.T) {
	inbound := anthropictransformer.NewInboundTransformer()
	llmReq, err := inbound.TransformRequest(context.Background(), &httpclient.Request{
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body: []byte(`{
			"model": "claude-sonnet-4-5",
			"max_tokens": 1024,
			"system": "stable system prompt",
			"tools": [
				{
					"name": "get_weather",
					"description": "Get weather",
					"input_schema": {"type": "object"},
					"cache_control": {"type": "ephemeral"}
				}
			],
			"messages": [
				{"role": "user", "content": "latest question"}
			]
		}`),
	})
	require.NoError(t, err)

	llmReq.Model = "qwen3.7-max"

	outbound, err := NewOutboundTransformerWithConfig(&Config{
		BaseURL:        "https://example.com",
		APIKeyProvider: auth.NewStaticKeyProvider("test-key"),
	})
	require.NoError(t, err)

	httpReq, err := outbound.TransformRequest(context.Background(), llmReq)
	require.NoError(t, err)

	body := string(httpReq.Body)
	require.Equal(t, "stable system prompt", gjson.Get(body, "messages.0.content.0.text").String())
	require.Equal(t, "ephemeral", gjson.Get(body, "messages.0.content.0.cache_control.type").String())
	require.False(t, gjson.Get(body, "tools.0.cache_control").Exists())
}
