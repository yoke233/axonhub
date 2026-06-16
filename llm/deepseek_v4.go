package llm

import "strings"

// DeepSeekV4ThinkingControl describes the normalized thinking controls used by
// DeepSeek v4 compatible models.
type DeepSeekV4ThinkingControl struct {
	Enabled bool
	Effort  string
}

// IsDeepSeekV4Model reports whether model should follow DeepSeek v4 thinking controls.
func IsDeepSeekV4Model(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "deepseek-v4-pro") || strings.HasPrefix(model, "deepseek-v4-flash")
}

// ResolveDeepSeekV4ThinkingControl returns DeepSeek v4 thinking settings.
func ResolveDeepSeekV4ThinkingControl(req *Request) (DeepSeekV4ThinkingControl, bool) {
	if req == nil || !IsDeepSeekV4Model(req.Model) {
		return DeepSeekV4ThinkingControl{}, false
	}

	switch RequestThinkingType(req) {
	case "disabled":
		return DeepSeekV4ThinkingControl{Enabled: false}, true
	case "enabled", "adaptive":
		effort := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
		return DeepSeekV4ThinkingControl{
			Enabled: true,
			Effort:  normalizeDeepSeekV4ReasoningEffort(effort, isAgenticRequest(req)),
		}, true
	}

	effort := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	if effort == "none" {
		return DeepSeekV4ThinkingControl{Enabled: false}, true
	}

	return DeepSeekV4ThinkingControl{
		Enabled: true,
		Effort:  normalizeDeepSeekV4ReasoningEffort(effort, isAgenticRequest(req)),
	}, true
}

func normalizeDeepSeekV4ReasoningEffort(effort string, agentic bool) string {
	switch effort {
	case "":
		if agentic {
			return "max"
		}

		return "high"
	case "low", "medium", "high":
		return "high"
	case "xhigh", "max":
		return "max"
	default:
		return "high"
	}
}

func isAgenticRequest(req *Request) bool {
	if len(req.Tools) > 0 || req.ToolChoice != nil || req.ParallelToolCalls != nil {
		return true
	}

	for _, msg := range req.Messages {
		if strings.EqualFold(msg.Role, "tool") || len(msg.ToolCalls) > 0 || msg.ToolCallID != nil {
			return true
		}
	}

	return false
}
