package llm

import "strings"

// TransformerMetadataKeyThinkingType stores provider thinking mode preserved by inbound transformers.
const TransformerMetadataKeyThinkingType = "thinking_type"

// RequestThinkingType returns the normalized provider thinking mode from TransformerMetadata.
func RequestThinkingType(req *Request) string {
	if req == nil {
		return ""
	}

	thinkingType, _ := req.TransformerMetadata[TransformerMetadataKeyThinkingType].(string)
	return strings.ToLower(strings.TrimSpace(thinkingType))
}
