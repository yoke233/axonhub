package anthropic

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/llm"
)

func TestThinking_BudgetClampedBelowMaxTokens(t *testing.T) {
	tests := []struct {
		name         string
		chatReq      *llm.Request
		wantThinking bool
		wantBudget   int64
		// Explicit client budgets are passed through and may exceed max_tokens
		// (legitimate with interleaved thinking).
		wantPassthrough bool
	}{
		{
			name: "effort high clamped under explicit max_tokens",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				MaxTokens:       lo.ToPtr(int64(8192)),
				ReasoningEffort: "high",
			},
			wantThinking: true,
			wantBudget:   7168, // 8192 - 1024 output reserve
		},
		{
			name: "effort high clamped under default max_tokens",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				ReasoningEffort: "high",
			},
			wantThinking: true,
			wantBudget:   7168,
		},
		{
			name: "small derived budget stays unchanged",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				MaxTokens:       lo.ToPtr(int64(8192)),
				ReasoningEffort: "low",
			},
			wantThinking: true,
			wantBudget:   5000,
		},
		{
			name: "max_tokens too small for thinking omits the field",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				MaxTokens:       lo.ToPtr(int64(1500)),
				ReasoningEffort: "high",
			},
			wantThinking: false,
		},
		{
			name: "explicit client budget passes through unclamped",
			chatReq: &llm.Request{
				Model:           "claude-sonnet-4-5-20250929",
				MaxTokens:       lo.ToPtr(int64(4096)),
				ReasoningEffort: "high",
				ReasoningBudget: lo.ToPtr(int64(30000)),
			},
			wantThinking:    true,
			wantBudget:      30000,
			wantPassthrough: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := convertToAnthropicRequest(tt.chatReq)

			if !tt.wantThinking {
				require.Nil(t, req.Thinking)
				return
			}

			require.NotNil(t, req.Thinking)
			require.Equal(t, "enabled", req.Thinking.Type)
			require.Equal(t, tt.wantBudget, req.Thinking.BudgetTokens)

			if !tt.wantPassthrough {
				require.Less(t, req.Thinking.BudgetTokens, req.MaxTokens)
			}
		})
	}
}

func TestThinking_StripsSamplingParams(t *testing.T) {
	tests := []struct {
		name         string
		chatReq      *llm.Request
		config       *Config
		wantStripped bool
	}{
		{
			name: "thinking enabled strips temperature and top_p",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				MaxTokens:       lo.ToPtr(int64(16000)),
				ReasoningEffort: "high",
				Temperature:     lo.ToPtr(0.7),
				TopP:            lo.ToPtr(0.9),
			},
			wantStripped: true,
		},
		{
			name: "adaptive-only model strips sampling even without thinking",
			chatReq: &llm.Request{
				Model:       "claude-opus-4-8",
				MaxTokens:   lo.ToPtr(int64(16000)),
				Temperature: lo.ToPtr(0.7),
				TopP:        lo.ToPtr(0.9),
			},
			wantStripped: true,
		},
		{
			name: "fable strips sampling even without thinking",
			chatReq: &llm.Request{
				Model:       "claude-fable-5",
				MaxTokens:   lo.ToPtr(int64(16000)),
				Temperature: lo.ToPtr(0.7),
			},
			wantStripped: true,
		},
		{
			name: "no thinking keeps sampling params",
			chatReq: &llm.Request{
				Model:       "claude-3-sonnet-20240229",
				MaxTokens:   lo.ToPtr(int64(16000)),
				Temperature: lo.ToPtr(0.7),
				TopP:        lo.ToPtr(0.9),
			},
			wantStripped: false,
		},
		{
			name: "non-Claude platform keeps sampling params with thinking",
			chatReq: &llm.Request{
				Model:           "deepseek-v4-pro",
				MaxTokens:       lo.ToPtr(int64(16000)),
				ReasoningEffort: "high",
				Temperature:     lo.ToPtr(0.7),
			},
			config:       &Config{Type: PlatformDeepSeek},
			wantStripped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := convertToAnthropicRequestWithConfig(tt.chatReq, tt.config)

			if tt.wantStripped {
				require.Nil(t, req.Temperature)
				require.Nil(t, req.TopP)
				return
			}

			if tt.chatReq.Temperature != nil {
				require.NotNil(t, req.Temperature)
			}

			if tt.chatReq.TopP != nil {
				require.NotNil(t, req.TopP)
			}
		})
	}
}

func TestThinking_GenericForcedToolChoiceOmitsThinking(t *testing.T) {
	weatherTool := llm.Tool{
		Type: llm.ToolTypeFunction,
		Function: llm.Function{
			Name:       "get_weather",
			Parameters: []byte(`{"type":"object"}`),
		},
	}

	tests := []struct {
		name    string
		chatReq *llm.Request
	}{
		{
			name: "effort with required tool choice",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				MaxTokens:       lo.ToPtr(int64(16000)),
				ReasoningEffort: "high",
				Tools:           []llm.Tool{weatherTool},
				ToolChoice:      &llm.ToolChoice{ToolChoice: lo.ToPtr("required")},
			},
		},
		{
			name: "explicit budget with named tool choice",
			chatReq: &llm.Request{
				Model:           "claude-sonnet-4-5-20250929",
				MaxTokens:       lo.ToPtr(int64(16000)),
				ReasoningEffort: "high",
				ReasoningBudget: lo.ToPtr(int64(10000)),
				Tools:           []llm.Tool{weatherTool},
				ToolChoice: &llm.ToolChoice{
					NamedToolChoice: &llm.NamedToolChoice{
						Type:     "function",
						Function: llm.ToolFunction{Name: "get_weather"},
					},
				},
			},
		},
		{
			name: "metadata adaptive with required tool choice",
			chatReq: &llm.Request{
				Model:           "claude-3-sonnet-20240229",
				MaxTokens:       lo.ToPtr(int64(16000)),
				ReasoningEffort: "high",
				Tools:           []llm.Tool{weatherTool},
				ToolChoice:      &llm.ToolChoice{ToolChoice: lo.ToPtr("required")},
				TransformerMetadata: map[string]any{
					TransformerMetadataKeyThinkingType: "adaptive",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := convertToAnthropicRequest(tt.chatReq)
			require.Nil(t, req.Thinking)
		})
	}
}

func TestThinking_FableAdaptiveOnly(t *testing.T) {
	t.Run("effort maps to adaptive with output_config", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-fable-5",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "high",
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "adaptive", req.Thinking.Type)
		require.Zero(t, req.Thinking.BudgetTokens)
		require.NotNil(t, req.OutputConfig)
		require.Equal(t, "high", req.OutputConfig.Effort)
	})

	t.Run("effort none omits thinking instead of disabled", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-fable-5",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "none",
		})

		require.Nil(t, req.Thinking)
	})

	t.Run("explicit disabled from anthropic inbound omits thinking", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-fable-5",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "none",
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyThinkingType: "disabled",
			},
		})

		require.Nil(t, req.Thinking)
	})

	t.Run("non-Claude platform falls back to generic path without explicit disabled", func(t *testing.T) {
		req := convertToAnthropicRequestWithConfig(&llm.Request{
			Model:           "claude-fable-5",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "none",
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyThinkingType: "disabled",
			},
		}, &Config{Type: "moonshot"})

		require.Nil(t, req.Thinking)
	})
}

func TestThinking_Claude46PrefersAdaptive(t *testing.T) {
	t.Run("effort only maps to adaptive with output_config", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-sonnet-4-6",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "high",
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "adaptive", req.Thinking.Type)
		require.Zero(t, req.Thinking.BudgetTokens)
		require.NotNil(t, req.OutputConfig)
		require.Equal(t, "high", req.OutputConfig.Effort)
	})

	t.Run("explicit client budget keeps enabled thinking", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-opus-4-6",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "medium",
			ReasoningBudget: lo.ToPtr(int64(10000)),
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "enabled", req.Thinking.Type)
		require.Equal(t, int64(10000), req.Thinking.BudgetTokens)
	})

	t.Run("effort none keeps explicit disabled", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-sonnet-4-6",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "none",
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "disabled", req.Thinking.Type)
	})
}

func TestThinking_DisplayDefaultsForNonAnthropicInbound(t *testing.T) {
	t.Run("openai-style effort request defaults to summarized display", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-opus-4-8",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "high",
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "adaptive", req.Thinking.Type)
		require.Equal(t, "summarized", req.Thinking.Display)
	})

	t.Run("anthropic adaptive inbound without display keeps upstream default", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-opus-4-8",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "high",
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyThinkingType: "adaptive",
			},
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "adaptive", req.Thinking.Type)
		require.Empty(t, req.Thinking.Display)
	})

	t.Run("explicit display always wins", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-opus-4-8",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "high",
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyThinkingType:    "adaptive",
				TransformerMetadataKeyThinkingDisplay: "omitted",
			},
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "omitted", req.Thinking.Display)
	})
}

func TestThinking_EffortBudgetDefaults(t *testing.T) {
	tests := []struct {
		effort     string
		wantBudget int64
	}{
		{"low", 5000},
		{"medium", 15000},
		{"high", 30000},
		{"xhigh", 45000},
		{"max", 60000},
	}

	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			req := convertToAnthropicRequest(&llm.Request{
				Model:           "claude-sonnet-4-5-20250929",
				MaxTokens:       lo.ToPtr(int64(64000)),
				ReasoningEffort: tt.effort,
			})

			require.NotNil(t, req.Thinking)
			require.Equal(t, "enabled", req.Thinking.Type)
			require.Equal(t, tt.wantBudget, req.Thinking.BudgetTokens)
		})
	}
}

func TestThinking_BedrockModelIDs(t *testing.T) {
	bedrockCfg := &Config{Type: PlatformBedrock}

	t.Run("bedrock fable id maps effort to adaptive", func(t *testing.T) {
		req := convertToAnthropicRequestWithConfig(&llm.Request{
			Model:           "us.anthropic.claude-fable-5-20260601-v1:0",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "high",
			Temperature:     lo.ToPtr(0.7),
		}, bedrockCfg)

		require.NotNil(t, req.Thinking)
		require.Equal(t, "adaptive", req.Thinking.Type)
		require.Nil(t, req.Temperature)
	})

	t.Run("bedrock opus-4-8 id strips sampling without thinking", func(t *testing.T) {
		req := convertToAnthropicRequestWithConfig(&llm.Request{
			Model:       "anthropic.claude-opus-4-8-20260115-v1:0",
			MaxTokens:   lo.ToPtr(int64(16000)),
			Temperature: lo.ToPtr(0.7),
			TopP:        lo.ToPtr(0.9),
		}, bedrockCfg)

		require.Nil(t, req.Temperature)
		require.Nil(t, req.TopP)
	})

	t.Run("bedrock fable id omits explicit disabled", func(t *testing.T) {
		req := convertToAnthropicRequestWithConfig(&llm.Request{
			Model:           "us.anthropic.claude-fable-5-20260601-v1:0",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "none",
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyThinkingType: "disabled",
			},
		}, bedrockCfg)

		require.Nil(t, req.Thinking)
	})
}

func TestThinking_SentinelReasoningBudgets(t *testing.T) {
	t.Run("zero budget disables thinking on generic models", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-sonnet-4-5-20250929",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "low",
			ReasoningBudget: lo.ToPtr(int64(0)),
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "disabled", req.Thinking.Type)
	})

	t.Run("zero budget keeps thinking off on adaptive models", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-opus-4-8",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "low",
			ReasoningBudget: lo.ToPtr(int64(0)),
		})

		require.Nil(t, req.Thinking)
	})

	t.Run("negative budget falls back to effort-derived budget", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-sonnet-4-5-20250929",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "low",
			ReasoningBudget: lo.ToPtr(int64(-1)),
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "enabled", req.Thinking.Type)
		require.Equal(t, int64(5000), req.Thinking.BudgetTokens)
	})

	t.Run("sub-minimum budget raised to API minimum", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-sonnet-4-5-20250929",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "low",
			ReasoningBudget: lo.ToPtr(int64(512)),
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "enabled", req.Thinking.Type)
		require.Equal(t, int64(1024), req.Thinking.BudgetTokens)
	})
}

func TestThinking_OutputConfigOnlyDoesNotForceThinking(t *testing.T) {
	t.Run("anthropic output_config-only request on 4.6 keeps thinking off", func(t *testing.T) {
		// Mirrors the anthropic inbound: output_config.effort copied into both
		// the metadata and ReasoningEffort, no thinking field.
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-opus-4-6",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "low",
			Temperature:     lo.ToPtr(0.7),
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyOutputConfigEffort: "low",
			},
		})

		require.Nil(t, req.Thinking)
		require.NotNil(t, req.OutputConfig)
		require.Equal(t, "low", req.OutputConfig.Effort)
		require.NotNil(t, req.Temperature)
	})

	t.Run("anthropic output_config-only request on generic model keeps thinking off", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-sonnet-4-5-20250929",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "max",
			TransformerMetadata: map[string]any{
				TransformerMetadataKeyOutputConfigEffort: "max",
			},
		})

		require.Nil(t, req.Thinking)
		require.NotNil(t, req.OutputConfig)
		require.Equal(t, "max", req.OutputConfig.Effort)
	})
}

func TestThinking_EffortCaseInsensitive(t *testing.T) {
	t.Run("None disables thinking", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-3-sonnet-20240229",
			MaxTokens:       lo.ToPtr(int64(16000)),
			ReasoningEffort: "None",
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, "disabled", req.Thinking.Type)
	})

	t.Run("High maps to the high budget", func(t *testing.T) {
		req := convertToAnthropicRequest(&llm.Request{
			Model:           "claude-3-sonnet-20240229",
			MaxTokens:       lo.ToPtr(int64(64000)),
			ReasoningEffort: "High",
		})

		require.NotNil(t, req.Thinking)
		require.Equal(t, int64(30000), req.Thinking.BudgetTokens)
	})
}

func TestThinking_MinimalEffortMapsToLow(t *testing.T) {
	req := convertToAnthropicRequest(&llm.Request{
		Model:           "claude-fable-5",
		MaxTokens:       lo.ToPtr(int64(16000)),
		ReasoningEffort: "minimal",
	})

	require.NotNil(t, req.Thinking)
	require.Equal(t, "adaptive", req.Thinking.Type)
	require.NotNil(t, req.OutputConfig)
	require.Equal(t, "low", req.OutputConfig.Effort)
}

func TestThinking_TopPWithinThinkingRangeKept(t *testing.T) {
	req := convertToAnthropicRequest(&llm.Request{
		Model:           "claude-sonnet-4-5-20250929",
		MaxTokens:       lo.ToPtr(int64(16000)),
		ReasoningEffort: "low",
		Temperature:     lo.ToPtr(0.7),
		TopP:            lo.ToPtr(0.95),
	})

	require.NotNil(t, req.Thinking)
	require.Equal(t, "enabled", req.Thinking.Type)
	require.Nil(t, req.Temperature)
	// top_p within [0.95, 1] is accepted alongside thinking.
	require.NotNil(t, req.TopP)
}

func TestThinking_ForcedToolGuardClaudeModelsOnly(t *testing.T) {
	req := convertToAnthropicRequestWithConfig(&llm.Request{
		Model:           "glm-4.7",
		MaxTokens:       lo.ToPtr(int64(16000)),
		ReasoningEffort: "high",
		ReasoningBudget: lo.ToPtr(int64(10000)),
		Tools: []llm.Tool{{
			Type: llm.ToolTypeFunction,
			Function: llm.Function{
				Name:       "get_weather",
				Parameters: []byte(`{"type":"object"}`),
			},
		}},
		ToolChoice: &llm.ToolChoice{ToolChoice: lo.ToPtr("required")},
	}, &Config{Type: "zhipu"})

	// Third-party anthropic-format providers accept thinking + forced tools;
	// the guard only applies to Claude models.
	require.NotNil(t, req.Thinking)
	require.Equal(t, "enabled", req.Thinking.Type)
	require.Equal(t, int64(10000), req.Thinking.BudgetTokens)
}

func TestThinking_AnthropicEnabledInboundGetsSummarizedDisplay(t *testing.T) {
	// Anthropic client sent thinking:{type:"enabled"} (legacy default display is
	// summarized); converted to adaptive on adaptive-only models.
	req := convertToAnthropicRequest(&llm.Request{
		Model:           "claude-opus-4-8",
		MaxTokens:       lo.ToPtr(int64(16000)),
		ReasoningEffort: "high",
		ReasoningBudget: lo.ToPtr(int64(30000)),
		TransformerMetadata: map[string]any{
			TransformerMetadataKeyThinkingType: "enabled",
		},
	})

	require.NotNil(t, req.Thinking)
	require.Equal(t, "adaptive", req.Thinking.Type)
	require.Equal(t, "summarized", req.Thinking.Display)
}
