package shared

// Metadata keys used in TransformerMetadata to pass data between inbound and outbound transformers.
const (
	// MetaKeyAnthropicPromptCacheKey is the key for storing the Anthropic prompt cache key
	// computed during inbound transformation, so outbound transformers can reuse it
	// (e.g. as a session ID for Codex).
	MetaKeyAnthropicPromptCacheKey = "anthropic_prompt_cache_key"

	// MetaKeyAnthropicMetadataUserID is the key for storing the Anthropic metadata.user_id
	// from the original request.
	MetaKeyAnthropicMetadataUserID = "anthropic_metadata_user_id"
)
