package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

)

type promptCacheKeyMode string

const (
	promptCacheKeyModeNone     promptCacheKeyMode = "none"
	promptCacheKeyModeExplicit promptCacheKeyMode = "explicit"
	promptCacheKeyModeDerived  promptCacheKeyMode = "derived"
)

type promptCacheKeySeed struct {
	Version  string               `json:"version"`
	Model    string               `json:"model,omitempty"`
	User     string               `json:"user,omitempty"`
	Mode     promptCacheKeyMode   `json:"mode,omitempty"`
	System   []string             `json:"system,omitempty"`
	Tools    []string             `json:"tools,omitempty"`
	Messages []promptCacheKeyPart `json:"messages,omitempty"`
}

type promptCacheKeyPart struct {
	Role  string `json:"role,omitempty"`
	Block string `json:"block,omitempty"`
}

// BuildPromptCacheKey derives a stable prompt cache key from Anthropic request content.
func BuildPromptCacheKey(req *MessageRequest) string {
	if req == nil {
		return ""
	}

	seed, ok := buildPromptCacheKeySeed(req)
	if !ok {
		return ""
	}

	payload, err := json.Marshal(seed)
	if err != nil {
		return ""
	}

	return buildStablePromptCacheKey("anthropic-cache-v2", payload)
}

func buildPromptCacheKeySeed(req *MessageRequest) (promptCacheKeySeed, bool) {
	cloned, ok := cloneMessageRequest(req)
	if !ok {
		return promptCacheKeySeed{}, false
	}

	normalizeMessageContents(cloned)

	mode := promptCacheKeyModeDerived
	if countCacheControls(cloned) > 0 {
		mode = promptCacheKeyModeExplicit
	}

	seed := promptCacheKeySeed{
		Version: "anthropic-cache-v2",
		Model:   strings.TrimSpace(cloned.Model),
		User:    anthropicPromptCacheUserIdentity(cloned),
		Mode:    mode,
		System:  collectPromptCacheSystemParts(cloned),
		Tools:   collectPromptCacheToolParts(cloned),
	}

	if mode == promptCacheKeyModeExplicit {
		seed.Messages = collectExplicitPromptCacheMessageParts(cloned)
	} else {
		seed.Messages = collectDerivedPromptCacheMessageParts(cloned)
	}

	if len(seed.System) == 0 && len(seed.Tools) == 0 && len(seed.Messages) == 0 {
		return promptCacheKeySeed{}, false
	}

	return seed, true
}

func cloneMessageRequest(req *MessageRequest) (*MessageRequest, bool) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false
	}

	var cloned MessageRequest
	if err := json.Unmarshal(body, &cloned); err != nil {
		return nil, false
	}

	return &cloned, true
}

func anthropicPromptCacheUserIdentity(req *MessageRequest) string {
	if req == nil || req.Metadata == nil || strings.TrimSpace(req.Metadata.UserID) == "" {
		return "anonymous"
	}

	return strings.TrimSpace(req.Metadata.UserID)
}

func collectPromptCacheSystemParts(req *MessageRequest) []string {
	if req == nil || req.System == nil {
		return nil
	}

	var parts []string
	if req.System.Prompt != nil {
		if prompt := strings.TrimSpace(*req.System.Prompt); prompt != "" {
			parts = append(parts, prompt)
		}
	}

	for _, part := range req.System.MultiplePrompts {
		if text := strings.TrimSpace(part.Text); text != "" {
			parts = append(parts, text)
		}
	}

	return parts
}

func collectPromptCacheToolParts(req *MessageRequest) []string {
	if req == nil || len(req.Tools) == 0 {
		return nil
	}

	parts := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		toolCopy := tool
		toolCopy.CacheControl = nil
		parts = append(parts, marshalPromptCachePart(toolCopy))
	}

	return parts
}

func collectExplicitPromptCacheMessageParts(req *MessageRequest) []promptCacheKeyPart {
	if req == nil {
		return nil
	}

	var parts []promptCacheKeyPart
	for _, msg := range req.Messages {
		for _, block := range msg.Content.MultipleContent {
			if block.CacheControl == nil || !isCacheableMessageBlock(block) {
				continue
			}

			parts = append(parts, promptCacheKeyPart{
				Role:  msg.Role,
				Block: marshalPromptCacheBlock(block),
			})
		}
	}

	return parts
}

func collectDerivedPromptCacheMessageParts(req *MessageRequest) []promptCacheKeyPart {
	if req == nil {
		return nil
	}

	var parts []promptCacheKeyPart
	for _, msg := range req.Messages {
		if msg.Role == "assistant" {
			break
		}

		if msg.Role == "system" || msg.Role == "developer" {
			continue
		}

		for _, block := range msg.Content.MultipleContent {
			if !isCacheableMessageBlock(block) {
				continue
			}

			parts = append(parts, promptCacheKeyPart{
				Role:  msg.Role,
				Block: marshalPromptCacheBlock(block),
			})
		}
		if msg.Content.Content != nil && strings.TrimSpace(*msg.Content.Content) != "" {
			parts = append(parts, promptCacheKeyPart{
				Role:  msg.Role,
				Block: marshalPromptCachePart(strings.TrimSpace(*msg.Content.Content)),
			})
		}
	}

	return parts
}

func marshalPromptCacheBlock(block MessageContentBlock) string {
	blockCopy := block
	blockCopy.CacheControl = nil
	return marshalPromptCachePart(blockCopy)
}

func marshalPromptCachePart(v any) string {
	body, err := json.Marshal(v)
	if err != nil {
		return ""
	}

	return string(body)
}

func PromptCacheKeyModeForRequest(req *MessageRequest) string {
	if req == nil {
		return string(promptCacheKeyModeNone)
	}

	if countCacheControls(req) > 0 {
		return string(promptCacheKeyModeExplicit)
	}

	seed, ok := buildPromptCacheKeySeed(req)
	if !ok || len(seed.System) == 0 && len(seed.Tools) == 0 && len(seed.Messages) == 0 {
		return string(promptCacheKeyModeNone)
	}

	return string(promptCacheKeyModeDerived)
}

func buildStablePromptCacheKey(prefix string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return prefix + "-" + hex.EncodeToString(sum[:16])
}
