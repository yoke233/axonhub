package codex

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sanitizeCodexRequestBody keeps caller-supplied fields intact, removes internal
// fallback-only prompt_cache_key values when Codex did not choose one, and only
// normalizes instructions so the payload shape stays acceptable to the Codex backend.
func sanitizeCodexRequestBody(body []byte, keepPromptCacheKey bool) []byte {
	if len(body) == 0 {
		return body
	}

	if !keepPromptCacheKey {
		body = deleteCodexPromptCacheKey(body)
	}

	return normalizeCodexInstructions(body)
}

func deleteCodexPromptCacheKey(body []byte) []byte {
	body, _ = sjson.DeleteBytes(body, "prompt_cache_key")

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
