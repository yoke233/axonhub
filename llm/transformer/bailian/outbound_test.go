package bailian

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/auth"
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
