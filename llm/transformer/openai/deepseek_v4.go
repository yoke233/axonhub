package openai

import "github.com/looplj/axonhub/llm"

func applyDeepSeekV4Thinking(req *Request, src *llm.Request) {
	control, ok := llm.ResolveDeepSeekV4ThinkingControl(src)
	if !ok {
		return
	}

	if llm.ForcesToolUse(src.ToolChoice) {
		req.Thinking = &Thinking{Type: "disabled"}
		req.ReasoningEffort = ""
		return
	}

	if control.Enabled {
		req.Thinking = &Thinking{Type: "enabled"}
		req.ReasoningEffort = control.Effort
		return
	}

	req.Thinking = &Thinking{Type: "disabled"}
	req.ReasoningEffort = ""
}
