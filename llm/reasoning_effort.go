package llm

import "strings"

// reasoningEffortAliases maps an outbound API format to its effort alias table.
// Inbound transformers preserve the client's effort vocabulary verbatim (e.g.
// Anthropic output_config.effort = "max"); formats whose vocabulary differs
// declare the cross-protocol aliases here. Formats without a table, and effort
// values without an alias, pass through unchanged so channel-specific
// transformers can still apply their own rules (e.g. DeepSeek v4 maps
// "xhigh" back to "max").
var reasoningEffortAliases = map[APIFormat]map[string]string{
	// OpenAI accepts none/minimal/low/medium/high/xhigh. "max" is Anthropic
	// vocabulary and is rejected by the OpenAI API, so degrade it to the
	// closest OpenAI tier.
	APIFormatOpenAIChatCompletion:  {"max": "xhigh"},
	APIFormatOpenAIResponse:        {"max": "xhigh"},
	APIFormatOpenAIResponseCompact: {"max": "xhigh"},
}

// NormalizeReasoningEffort maps a reasoning effort to the vocabulary of the
// target API format. Values already valid for the format, or formats without
// an alias table, are returned unchanged.
func NormalizeReasoningEffort(format APIFormat, effort string) string {
	if effort == "" {
		return effort
	}

	aliases, ok := reasoningEffortAliases[format]
	if !ok {
		return effort
	}

	if mapped, ok := aliases[strings.ToLower(strings.TrimSpace(effort))]; ok {
		return mapped
	}

	return effort
}
