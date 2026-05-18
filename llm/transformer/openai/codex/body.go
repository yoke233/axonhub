package codex

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sanitizeCodexRequestBody keeps caller-supplied fields intact, removes internal
// fallback-only prompt_cache_key values when Codex did not choose one, strips
// fields the ChatGPT Codex backend rejects, and normalizes instructions so the
// payload shape stays acceptable.
func sanitizeCodexRequestBody(body []byte, keepPromptCacheKey bool) []byte {
	if len(body) == 0 {
		return body
	}

	if !keepPromptCacheKey {
		body = deleteCodexPromptCacheKey(body)
	}

	body = stripCodexUnsupportedFields(body)

	return normalizeCodexInstructions(body)
}

func deleteCodexPromptCacheKey(body []byte) []byte {
	body, _ = sjson.DeleteBytes(body, "prompt_cache_key")

	return body
}

// codexUnsupportedRequestFields lists Responses API fields that the ChatGPT
// Codex backend (chatgpt.com/backend-api/codex/responses) refuses with 400
// "Unsupported parameter". The real codex_cli_rs ResponsesApiRequest only
// serializes a fixed whitelist (model, instructions, input, tools, tool_choice,
// parallel_tool_calls, reasoning, store, stream, include, service_tier,
// prompt_cache_key, text, client_metadata); anything outside it gets rejected.
// Reasoning models on this surface control output via reasoning.effort and
// text.verbosity rather than max_output_tokens / sampling knobs.
var codexUnsupportedRequestFields = []string{
	"max_output_tokens",
	"max_tokens",
	"max_tool_calls",
	"temperature",
	"top_p",
	"top_logprobs",
	"truncation",
	"safety_identifier",
	"user",
	"metadata",
	"previous_response_id",
	"background",
	"prompt_cache_retention",
	"stream_options",
}

func stripCodexUnsupportedFields(body []byte) []byte {
	for _, field := range codexUnsupportedRequestFields {
		body, _ = sjson.DeleteBytes(body, field)
	}

	return body
}

func normalizeCodexInstructions(body []byte) []byte {
	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() || instructions.Type == gjson.Null {
		// responses.Request requires a string field here; keep the value empty rather
		// than injecting CLI-specific instructions.
		body, _ = sjson.SetBytes(body, "instructions", "")
	}

	return body
}
