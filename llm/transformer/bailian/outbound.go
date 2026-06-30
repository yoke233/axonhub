package bailian

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/sjson"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/auth"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer"
	anthropictransformer "github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/openai"
)

// Config holds all configuration for the Bailian outbound transformer.
type Config struct {
	BaseURL        string              `json:"base_url,omitempty"`
	APIKeyProvider auth.APIKeyProvider `json:"-"`
}

// OutboundTransformer implements transformer.Outbound for Bailian (OpenAI-compatible) format.
type OutboundTransformer struct {
	transformer.Outbound

	config *Config
}

// NewOutboundTransformer creates a new Bailian OutboundTransformer with legacy parameters.
// Deprecated: Use NewOutboundTransformerWithConfig instead.
func NewOutboundTransformer(baseURL, apiKey string) (transformer.Outbound, error) {
	config := &Config{
		BaseURL:        baseURL,
		APIKeyProvider: auth.NewStaticKeyProvider(apiKey),
	}

	return NewOutboundTransformerWithConfig(config)
}

// NewOutboundTransformerWithConfig creates a new Bailian OutboundTransformer with unified configuration.
func NewOutboundTransformerWithConfig(config *Config) (transformer.Outbound, error) {
	if config == nil {
		return nil, fmt.Errorf("invalid Bailian transformer configuration: config is nil")
	}

	oaiConfig := &openai.Config{
		PlatformType:   openai.PlatformOpenAI,
		BaseURL:        config.BaseURL,
		APIKeyProvider: config.APIKeyProvider,
		ReasoningField: openai.ReasoningFieldContent,
	}

	base, err := openai.NewOutboundTransformerWithConfig(oaiConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid Bailian transformer configuration: %w", err)
	}

	return &OutboundTransformer{Outbound: base, config: config}, nil
}

// TransformRequest applies Bailian-specific request normalization before delegating to OpenAI-compatible transformer.
func (t *OutboundTransformer) TransformRequest(ctx context.Context, llmReq *llm.Request) (*httpclient.Request, error) {
	llmReq = mergeConsecutiveToolCallMessages(llmReq)

	req, err := t.Outbound.TransformRequest(ctx, llmReq)
	if err != nil {
		return nil, err
	}

	applyBailianAnthropicCacheControl(req, llmReq)
	applyBailianOpenAICacheControl(req, llmReq)
	applyBailianToolMessageContentCompatibility(req)

	applyBailianAnthropicThinking(req, llmReq)

	return req, nil
}

const maxBailianCacheControlBreakpoints = 4

func applyBailianAnthropicCacheControl(httpReq *httpclient.Request, llmReq *llm.Request) {
	if httpReq == nil || llmReq == nil || len(httpReq.Body) == 0 || llmReq.APIFormat != llm.APIFormatAnthropicMessage {
		return
	}

	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		return
	}

	delete(body, "prompt_cache_key")

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		updated, err := json.Marshal(body)
		if err == nil {
			httpReq.Body = updated
		}

		return
	}

	applied := 0
	for i, msg := range llmReq.Messages {
		if applied >= maxBailianCacheControlBreakpoints || i >= len(messages) {
			break
		}

		if msg.CacheControl != nil && applyBailianCacheControlToMessage(messages, i, msg.CacheControl) {
			applied++
		}

		for partIdx, part := range msg.Content.MultipleContent {
			if applied >= maxBailianCacheControlBreakpoints {
				break
			}

			if part.CacheControl != nil && applyBailianCacheControlToContentPart(messages, i, partIdx, part, part.CacheControl) {
				applied++
			}
		}
	}

	if applied == 0 {
		if cc := bailianCacheControlFromMetadata(llmReq); cc != nil {
			if applyBailianCacheControlToLastCacheableMessage(messages, cc) {
				applied++
			}
		}
	}

	if applied == 0 {
		if cc := firstToolCacheControl(llmReq); cc != nil {
			_ = applyBailianCacheControlToStructuralAnchor(messages, cc)
		}
	}

	updated, err := json.Marshal(body)
	if err != nil {
		return
	}

	httpReq.Body = updated
}

func applyBailianCacheControlToMessage(messages []any, msgIndex int, cc *llm.CacheControl) bool {
	msg, ok := messageObject(messages, msgIndex)
	if !ok {
		return false
	}

	switch content := msg["content"].(type) {
	case string:
		if content == "" {
			return false
		}

		msg["content"] = []any{map[string]any{
			"type":          "text",
			"text":          content,
			"cache_control": bailianCacheControlPayload(cc),
		}}

		return true
	case []any:
		for i := len(content) - 1; i >= 0; i-- {
			part, ok := content[i].(map[string]any)
			if !ok || !isBailianCacheableContentPart(part) {
				continue
			}

			part["cache_control"] = bailianCacheControlPayload(cc)
			return true
		}
	}

	return false
}

func applyBailianCacheControlToContentPart(messages []any, msgIndex, partIndex int, llmPart llm.MessageContentPart, cc *llm.CacheControl) bool {
	msg, ok := messageObject(messages, msgIndex)
	if !ok {
		return false
	}

	switch content := msg["content"].(type) {
	case string:
		if partIndex != 0 || llmPart.Type != "text" || llmPart.Text == nil || content == "" {
			return false
		}

		msg["content"] = []any{map[string]any{
			"type":          "text",
			"text":          content,
			"cache_control": bailianCacheControlPayload(cc),
		}}

		return true
	case []any:
		if partIndex < 0 || partIndex >= len(content) {
			return false
		}

		part, ok := content[partIndex].(map[string]any)
		if !ok || !isBailianCacheableContentPart(part) {
			return false
		}

		part["cache_control"] = bailianCacheControlPayload(cc)
		return true
	}

	return false
}

func applyBailianOpenAICacheControl(httpReq *httpclient.Request, llmReq *llm.Request) {
	if httpReq == nil || llmReq == nil || len(httpReq.Body) == 0 || llmReq.APIFormat == llm.APIFormatAnthropicMessage {
		return
	}

	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		return
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return
	}

	applied := 0
	for i, msg := range llmReq.Messages {
		if applied >= maxBailianCacheControlBreakpoints || i >= len(messages) {
			break
		}

		if msg.CacheControl != nil && applyBailianCacheControlToMessage(messages, i, msg.CacheControl) {
			applied++
		}

		for partIdx, part := range msg.Content.MultipleContent {
			if applied >= maxBailianCacheControlBreakpoints {
				break
			}

			if part.CacheControl != nil && applyBailianCacheControlToContentPart(messages, i, partIdx, part, part.CacheControl) {
				applied++
			}
		}
	}

	if applied == 0 {
		return
	}

	delete(body, "prompt_cache_key")

	updated, err := json.Marshal(body)
	if err != nil {
		return
	}

	httpReq.Body = updated
}

func applyBailianCacheControlToLastCacheableMessage(messages []any, cc *llm.CacheControl) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if applyBailianCacheControlToMessage(messages, i, cc) {
			return true
		}
	}

	return false
}

func applyBailianCacheControlToStructuralAnchor(messages []any, cc *llm.CacheControl) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messageObject(messages, i)
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}

		if applyBailianCacheControlToMessage(messages, i, cc) {
			return true
		}
	}

	for i := range messages {
		if applyBailianCacheControlToMessage(messages, i, cc) {
			return true
		}
	}

	return false
}

func applyBailianToolMessageContentCompatibility(httpReq *httpclient.Request) {
	if httpReq == nil || len(httpReq.Body) == 0 {
		return
	}

	var body map[string]any
	if err := json.Unmarshal(httpReq.Body, &body); err != nil {
		return
	}

	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}

	changed := false
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		if role != "tool" {
			continue
		}

		parts, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		texts := make([]string, 0, len(parts))
		textParts := make([]any, 0, len(parts))
		hasCacheControl := false
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}

			partType, _ := part["type"].(string)
			if partType != "text" {
				continue
			}

			text, _ := part["text"].(string)
			if text != "" {
				texts = append(texts, text)
				textPart := map[string]any{
					"type": "text",
					"text": text,
				}
				if cc, ok := part["cache_control"]; ok {
					textPart["cache_control"] = cc
					hasCacheControl = true
				}

				textParts = append(textParts, textPart)
			}
		}

		if hasCacheControl {
			msg["content"] = textParts
		} else {
			msg["content"] = strings.Join(texts, "\n")
		}
		changed = true
	}

	if !changed {
		return
	}

	updated, err := json.Marshal(body)
	if err != nil {
		return
	}

	httpReq.Body = updated
}

func messageObject(messages []any, msgIndex int) (map[string]any, bool) {
	if msgIndex < 0 || msgIndex >= len(messages) {
		return nil, false
	}

	msg, ok := messages[msgIndex].(map[string]any)
	return msg, ok
}

func isBailianCacheableContentPart(part map[string]any) bool {
	partType, _ := part["type"].(string)
	if partType == "text" {
		text, _ := part["text"].(string)
		return text != ""
	}

	return partType != ""
}

func bailianCacheControlPayload(cc *llm.CacheControl) map[string]any {
	cacheType := "ephemeral"
	if cc != nil && strings.TrimSpace(cc.Type) != "" {
		cacheType = strings.TrimSpace(cc.Type)
	}

	return map[string]any{"type": cacheType}
}

func bailianCacheControlFromMetadata(req *llm.Request) *llm.CacheControl {
	if req == nil || req.TransformerMetadata == nil {
		return nil
	}

	raw := req.TransformerMetadata[anthropictransformer.TransformerMetadataKeyCacheControl]
	switch cc := raw.(type) {
	case *llm.CacheControl:
		return cc
	case llm.CacheControl:
		return &cc
	case *anthropictransformer.CacheControl:
		if cc == nil {
			return nil
		}

		return &llm.CacheControl{Type: cc.Type, TTL: cc.TTL}
	case anthropictransformer.CacheControl:
		return &llm.CacheControl{Type: cc.Type, TTL: cc.TTL}
	case map[string]any:
		cacheType, _ := cc["type"].(string)
		ttl, _ := cc["ttl"].(string)

		return &llm.CacheControl{Type: cacheType, TTL: ttl}
	default:
		return nil
	}
}

func firstToolCacheControl(req *llm.Request) *llm.CacheControl {
	if req == nil {
		return nil
	}

	for _, tool := range req.Tools {
		if tool.CacheControl != nil {
			return tool.CacheControl
		}
	}

	for _, msg := range req.Messages {
		for _, toolCall := range msg.ToolCalls {
			if toolCall.CacheControl != nil {
				return toolCall.CacheControl
			}
		}
	}

	return nil
}

func applyBailianAnthropicThinking(httpReq *httpclient.Request, llmReq *llm.Request) {
	if httpReq == nil || llmReq == nil || len(httpReq.Body) == 0 {
		return
	}

	body := httpReq.Body
	var err error

	body, err = sjson.DeleteBytes(body, "enable_thinking")
	if err != nil {
		return
	}

	thinkingType, _ := llmReq.TransformerMetadata[anthropictransformer.TransformerMetadataKeyThinkingType].(string)
	thinkingType = strings.ToLower(strings.TrimSpace(thinkingType))
	outputEffort, _ := llmReq.TransformerMetadata[anthropictransformer.TransformerMetadataKeyOutputConfigEffort].(string)
	outputEffort = strings.TrimSpace(outputEffort)

	if thinkingType == "" && outputEffort == "" {
		body, err = sjson.DeleteBytes(body, "thinking")
		if err != nil {
			return
		}

		body, err = sjson.DeleteBytes(body, "output_config")
		if err != nil {
			return
		}

		if llmReq.ReasoningEffort == "" {
			body, err = sjson.DeleteBytes(body, "reasoning_effort")
		} else {
			body, err = sjson.SetBytes(body, "reasoning_effort", llmReq.ReasoningEffort)
		}
		if err != nil {
			return
		}

		httpReq.Body = body

		return
	}

	body, err = sjson.DeleteBytes(body, "thinking")
	if err != nil {
		return
	}

	body, err = sjson.DeleteBytes(body, "output_config")
	if err != nil {
		return
	}

	switch thinkingType {
	case "enabled", "adaptive":
		body, err = sjson.SetBytes(body, "enable_thinking", true)
		if err != nil {
			return
		}
	case "disabled":
		body, err = sjson.SetBytes(body, "enable_thinking", false)
		if err != nil {
			return
		}
	}

	if outputEffort != "" {
		body, err = sjson.SetBytes(body, "reasoning_effort", outputEffort)
		if err != nil {
			return
		}
	} else {
		body, err = sjson.DeleteBytes(body, "reasoning_effort")
		if err != nil {
			return
		}
	}

	httpReq.Body = body
}

func mergeConsecutiveToolCallMessages(req *llm.Request) *llm.Request {
	if req == nil || len(req.Messages) < 2 {
		return req
	}

	changed := false
	messages := make([]llm.Message, 0, len(req.Messages))

	var pending *llm.Message

	for i := range req.Messages {
		msg := req.Messages[i]

		if isMergeableToolCallMessage(msg) {
			if pending == nil {
				pendingMsg := msg
				pending = &pendingMsg
			} else {
				pending.ToolCalls = append(pending.ToolCalls, msg.ToolCalls...)
				changed = true
			}

			continue
		}

		if pending != nil {
			messages = append(messages, *pending)
			pending = nil
		}

		messages = append(messages, msg)
	}

	if pending != nil {
		messages = append(messages, *pending)
	}

	if !changed {
		return req
	}

	updated := *req
	updated.Messages = messages

	return &updated
}

func isMergeableToolCallMessage(msg llm.Message) bool {
	if !strings.EqualFold(msg.Role, "assistant") {
		return false
	}

	if len(msg.ToolCalls) == 0 {
		return false
	}

	if msg.ToolCallID != nil || msg.Name != nil || msg.Refusal != "" || msg.MessageIndex != nil || msg.ToolCallName != nil || msg.ToolCallIsError != nil {
		return false
	}

	if msg.ReasoningContent != nil || msg.ReasoningSignature != nil || msg.RedactedReasoningContent != nil || msg.CacheControl != nil {
		return false
	}

	return isEmptyMessageContent(msg.Content)
}

func isEmptyMessageContent(content llm.MessageContent) bool {
	if content.Content != nil && *content.Content != "" {
		return false
	}

	return len(content.MultipleContent) == 0
}

// TransformStream applies Bailian-specific streaming normalization on top of OpenAI-compatible stream.
func (t *OutboundTransformer) TransformStream(
	ctx context.Context,
	req *httpclient.Request,
	stream streams.Stream[*httpclient.StreamEvent],
) (streams.Stream[*llm.Response], error) {
	baseStream, err := t.Outbound.TransformStream(ctx, req, stream)
	if err != nil {
		return nil, err
	}

	return newBailianStreamFilter(baseStream), nil
}
