package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		name   string
		format APIFormat
		effort string
		want   string
	}{
		{
			name:   "anthropic max maps to xhigh for chat completions",
			format: APIFormatOpenAIChatCompletion,
			effort: "max",
			want:   "xhigh",
		},
		{
			name:   "anthropic max maps to xhigh for responses",
			format: APIFormatOpenAIResponse,
			effort: "max",
			want:   "xhigh",
		},
		{
			name:   "anthropic max maps to xhigh for compact responses",
			format: APIFormatOpenAIResponseCompact,
			effort: "max",
			want:   "xhigh",
		},
		{
			name:   "max is case and whitespace insensitive",
			format: APIFormatOpenAIChatCompletion,
			effort: " MAX ",
			want:   "xhigh",
		},
		{
			name:   "valid openai effort passes through",
			format: APIFormatOpenAIChatCompletion,
			effort: "high",
			want:   "high",
		},
		{
			name:   "none passes through for chat completions",
			format: APIFormatOpenAIChatCompletion,
			effort: "none",
			want:   "none",
		},
		{
			name:   "empty effort passes through",
			format: APIFormatOpenAIChatCompletion,
			effort: "",
			want:   "",
		},
		{
			name:   "anthropic outbound keeps max",
			format: APIFormatAnthropicMessage,
			effort: "max",
			want:   "max",
		},
		{
			name:   "unknown format passes through",
			format: APIFormatGeminiContents,
			effort: "max",
			want:   "max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NormalizeReasoningEffort(tt.format, tt.effort))
		})
	}
}
