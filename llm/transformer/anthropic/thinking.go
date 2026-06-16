package anthropic

import (
	"strings"

	"github.com/looplj/axonhub/llm"
)

// minThinkingBudgetTokens is the Anthropic API minimum for thinking budget_tokens.
const minThinkingBudgetTokens = 1024

// supportsAdaptiveThinking returns true if the platform serves the real Claude
// API surface (thinking.type = "adaptive", sampling restrictions, etc.).
func supportsAdaptiveThinking(config *Config) bool {
	if config == nil {
		return true
	}

	//nolint:exhaustive // Checked.
	switch config.Type {
	case PlatformDirect, PlatformClaudeCode, PlatformBedrock, PlatformVertex:
		return true
	default:
		return false
	}
}

// normalizeClaudeModelID lowercases the model and strips provider wrappers so
// the claude- prefix checks work across platforms. Bedrock IDs look like
// "us.anthropic.claude-opus-4-8-20260115-v1:0"; Vertex IDs
// ("claude-opus-4-6@20251101") already match.
func normalizeClaudeModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.Index(model, "anthropic.claude-"); idx >= 0 {
		model = model[idx+len("anthropic."):]
	}

	return model
}

// isClaudeModelID reports whether the model is a Claude model under any
// platform's naming scheme.
func isClaudeModelID(model string) bool {
	return strings.HasPrefix(normalizeClaudeModelID(model), "claude-")
}

// isClaudeAdaptiveOnlyThinkingModel reports models that reject
// thinking.type = "enabled" with budget_tokens entirely (400).
func isClaudeAdaptiveOnlyThinkingModel(model string) bool {
	model = normalizeClaudeModelID(model)
	return strings.HasPrefix(model, "claude-opus-4-7") ||
		strings.HasPrefix(model, "claude-opus-4-8") ||
		strings.HasPrefix(model, "claude-fable") ||
		strings.HasPrefix(model, "claude-mythos")
}

// isClaudeAdaptivePreferredThinkingModel reports models where
// enabled+budget_tokens still works but is deprecated; adaptive thinking with
// output_config.effort is the recommended replacement.
func isClaudeAdaptivePreferredThinkingModel(model string) bool {
	model = normalizeClaudeModelID(model)
	return strings.HasPrefix(model, "claude-opus-4-6") ||
		strings.HasPrefix(model, "claude-sonnet-4-6")
}

func isClaudeAdaptiveThinkingModel(model string) bool {
	model = normalizeClaudeModelID(model)
	return strings.HasPrefix(model, "claude-opus-4") ||
		strings.HasPrefix(model, "claude-sonnet-4") ||
		isClaudeAdaptiveOnlyThinkingModel(model) ||
		isClaudeAdaptivePreferredThinkingModel(model)
}

// isClaudeOmitDisabledThinkingModel reports models that reject an explicit
// thinking.type = "disabled" (400); omitting the thinking field is the only
// way to express "no manual thinking config" there.
func isClaudeOmitDisabledThinkingModel(model string) bool {
	model = normalizeClaudeModelID(model)
	return strings.HasPrefix(model, "claude-fable") ||
		strings.HasPrefix(model, "claude-mythos-5")
}

func isDeepSeekAnthropicOutputConfigModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "deepseek-")
}

// supportsOutputConfig returns true if the platform supports the output_config field
// with effort control. DeepSeek supports output_config.effort but does NOT support
// thinking.type = "adaptive".
func supportsOutputConfig(config *Config) bool {
	if config == nil {
		return true
	}

	//nolint:exhaustive // Checked.
	switch config.Type {
	case PlatformDirect, PlatformClaudeCode, PlatformBedrock, PlatformVertex, PlatformDeepSeek:
		return true
	default:
		return false
	}
}

// normalizeEffortValue canonicalizes a client-supplied reasoning effort for
// comparisons ("None", " high " etc.).
func normalizeEffortValue(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
}

// isZeroReasoningBudget reports a Gemini-style "budget 0 = thinking off" sentinel.
func isZeroReasoningBudget(chatReq *llm.Request) bool {
	return chatReq.ReasoningBudget != nil && *chatReq.ReasoningBudget == 0
}

func applyDeepSeekV4Thinking(req *MessageRequest, chatReq *llm.Request) bool {
	if req == nil || chatReq == nil {
		return false
	}

	control, ok := llm.ResolveDeepSeekV4ThinkingControl(chatReq)
	if !ok {
		return false
	}

	if llm.ForcesToolUse(chatReq.ToolChoice) {
		req.Thinking = &Thinking{Type: "disabled"}
		return true
	}

	if control.Enabled {
		if effort, ok := chatReq.TransformerMetadata[TransformerMetadataKeyOutputConfigEffort].(string); ok && effort != "" {
			control.Effort = effort
		}

		req.Thinking = &Thinking{Type: "enabled"}
		req.OutputConfig = &OutputConfig{Effort: control.Effort}
	} else {
		req.Thinking = &Thinking{Type: "disabled"}
	}

	return true
}

func applyClaudeAdaptiveThinking(req *MessageRequest, chatReq *llm.Request, config *Config) bool {
	if req == nil || chatReq == nil {
		return false
	}

	thinkingType, _ := chatReq.TransformerMetadata[TransformerMetadataKeyThinkingType].(string)
	adaptiveOnly := isClaudeAdaptiveOnlyThinkingModel(chatReq.Model)
	if !supportsAdaptiveThinking(config) ||
		(!adaptiveOnly && !isClaudeAdaptivePreferredThinkingModel(chatReq.Model) &&
			!(thinkingType == "adaptive" && isClaudeAdaptiveThinkingModel(chatReq.Model))) {
		return false
	}

	outputEffort, hasOutputEffort := chatReq.TransformerMetadata[TransformerMetadataKeyOutputConfigEffort].(string)
	effort := normalizeEffortValue(chatReq.ReasoningEffort)

	// Anthropic rejects tool_choice "any"/"tool" combined with thinking; leave
	// the thinking field off entirely. output_config.effort is independent of
	// thinking and stays valid.
	if llm.ForcesToolUse(chatReq.ToolChoice) {
		if hasOutputEffort && outputEffort != "" {
			req.OutputConfig = &OutputConfig{Effort: normalizeClaudeAdaptiveEffort(outputEffort)}
		}

		return true
	}

	if effort == "none" || thinkingType == "disabled" || isZeroReasoningBudget(chatReq) {
		// Adaptive-only models treat an omitted thinking field as off (and
		// Fable/Mythos reject an explicit "disabled"); the 4.6 family keeps the
		// client's explicit disabled config.
		if !adaptiveOnly {
			req.Thinking = &Thinking{Type: "disabled"}
		}

		if hasOutputEffort && outputEffort != "" {
			req.OutputConfig = &OutputConfig{Effort: normalizeClaudeAdaptiveEffort(outputEffort)}
		}

		return true
	}

	// A ReasoningEffort that merely mirrors the client's output_config.effort
	// (Anthropic inbound, no thinking field) is not a thinking request: effort
	// is valid without thinking.
	thinkingRequested := thinkingType == "adaptive" || thinkingType == "enabled" ||
		chatReq.ReasoningBudget != nil ||
		(effort != "" && !hasOutputEffort)
	if !thinkingRequested {
		if hasOutputEffort && outputEffort != "" {
			req.OutputConfig = &OutputConfig{Effort: normalizeClaudeAdaptiveEffort(outputEffort)}
		}

		return true
	}

	// The 4.6 family still accepts enabled+budget_tokens; honor an explicit
	// client budget as-is (generic path) instead of rewriting it to adaptive.
	if !adaptiveOnly && thinkingType != "adaptive" && chatReq.ReasoningBudget != nil {
		return false
	}

	req.Thinking = &Thinking{Type: "adaptive"}

	display, _ := chatReq.TransformerMetadata[TransformerMetadataKeyThinkingDisplay].(string)

	switch {
	case display != "":
		req.Thinking.Display = display
	case thinkingType != "adaptive":
		// Either a non-Anthropic inbound (whose protocol surfaces reasoning
		// text directly, e.g. OpenAI reasoning_content) or a legacy
		// enabled+budget request (whose documented default display was
		// summarized). Newer Claude models omit thinking text unless
		// summarized display is requested, so restore it.
		req.Thinking.Display = "summarized"
	}

	req.OutputConfig = &OutputConfig{Effort: resolveClaudeAdaptiveEffort(chatReq, outputEffort)}

	return true
}

func resolveClaudeAdaptiveEffort(chatReq *llm.Request, outputEffort string) string {
	if outputEffort != "" {
		return normalizeClaudeAdaptiveEffort(outputEffort)
	}

	if chatReq.ReasoningEffort != "" {
		return normalizeClaudeAdaptiveEffort(chatReq.ReasoningEffort)
	}

	if chatReq.ReasoningBudget != nil && *chatReq.ReasoningBudget > 0 {
		return thinkingBudgetToReasoningEffort(*chatReq.ReasoningBudget)
	}

	return "high"
}

func normalizeClaudeAdaptiveEffort(effort string) string {
	switch normalizeEffortValue(effort) {
	case "low", "medium", "high", "xhigh", "max":
		return normalizeEffortValue(effort)
	case "minimal":
		// OpenAI vocabulary; the closest Anthropic tier.
		return "low"
	default:
		return "high"
	}
}

// stripSamplingForThinking removes sampling parameters that the Claude API
// rejects: the adaptive-only models reject temperature/top_p/top_k
// unconditionally, and with thinking on every Claude model rejects temperature
// and top_k while top_p is only accepted within [0.95, 1]. Applies to Claude
// models only — Anthropic-compatible third-party APIs (MiniMax, GLM, Kimi, …)
// accept sampling params alongside thinking.
func stripSamplingForThinking(req *MessageRequest, chatReq *llm.Request) {
	if !isClaudeModelID(chatReq.Model) {
		return
	}

	if isClaudeAdaptiveOnlyThinkingModel(chatReq.Model) {
		req.Temperature = nil
		req.TopP = nil
		req.TopK = nil

		return
	}

	if req.Thinking == nil || req.Thinking.Type == "disabled" {
		return
	}

	req.Temperature = nil
	req.TopK = nil

	if req.TopP != nil && (*req.TopP < 0.95 || *req.TopP > 1) {
		req.TopP = nil
	}
}

// clampThinkingBudget caps an effort-derived thinking budget so it stays
// strictly below max_tokens (an Anthropic API requirement) while reserving
// room for the visible output. Returns 0 when max_tokens is too small to fit
// the minimum thinking budget.
func clampThinkingBudget(budgetTokens, maxTokens int64) int64 {
	if maxTokens < minThinkingBudgetTokens*2 {
		return 0
	}

	if budgetTokens > maxTokens-minThinkingBudgetTokens {
		budgetTokens = maxTokens - minThinkingBudgetTokens
	}

	if budgetTokens < minThinkingBudgetTokens {
		budgetTokens = minThinkingBudgetTokens
	}

	return budgetTokens
}

// thinkingBudgetToReasoningEffort converts thinking budget tokens to reasoning effort string.
func thinkingBudgetToReasoningEffort(budgetTokens int64) string {
	// Map budget tokens to reasoning effort based on the same logic used in outbound
	if budgetTokens <= 5000 {
		return "low"
	} else if budgetTokens <= 15000 {
		return "medium"
	} else {
		return "high"
	}
}

// defaultReasoningEffortMapping is the default mapping from ReasoningEffort to thinking budget tokens.
var defaultReasoningEffortMapping = map[string]int64{
	"low":    5000,
	"medium": 15000,
	"high":   30000,
	"xhigh":  45000,
	"max":    60000,
}

// getThinkingBudgetTokensWithConfig returns the thinking budget tokens for a given reasoning effort with config.
func getThinkingBudgetTokensWithConfig(reasoningEffort string, config *Config) int64 {
	if config != nil && config.ReasoningEffortToBudget != nil {
		if budget, exists := config.ReasoningEffortToBudget[reasoningEffort]; exists {
			return budget
		}

		if budget, exists := config.ReasoningEffortToBudget[normalizeEffortValue(reasoningEffort)]; exists {
			return budget
		}
	}

	// Fall back to default mapping
	if budget, exists := defaultReasoningEffortMapping[normalizeEffortValue(reasoningEffort)]; exists {
		return budget
	}

	// Default to medium if not found
	return 15000
}
