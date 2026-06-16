package bailian

import (
	"context"
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

	if applyBailianDeepSeekV4Thinking(req, llmReq) {
		return req, nil
	}

	applyBailianAnthropicThinking(req, llmReq)

	return req, nil
}

func applyBailianDeepSeekV4Thinking(httpReq *httpclient.Request, llmReq *llm.Request) bool {
	control, ok := llm.ResolveDeepSeekV4ThinkingControl(llmReq)
	if !ok || httpReq == nil || len(httpReq.Body) == 0 {
		return false
	}

	body, err := sjson.DeleteBytes(httpReq.Body, "thinking")
	if err != nil {
		return true
	}

	body, err = sjson.SetBytes(body, "enable_thinking", control.Enabled)
	if err != nil {
		return true
	}

	if control.Enabled {
		body, err = sjson.SetBytes(body, "reasoning_effort", control.Effort)
	} else {
		body, err = sjson.DeleteBytes(body, "reasoning_effort")
	}
	if err != nil {
		return true
	}

	httpReq.Body = body

	return true
}

func applyBailianAnthropicThinking(httpReq *httpclient.Request, llmReq *llm.Request) {
	if httpReq == nil || llmReq == nil || len(httpReq.Body) == 0 {
		return
	}

	thinkingType, _ := llmReq.TransformerMetadata[anthropictransformer.TransformerMetadataKeyThinkingType].(string)
	switch strings.ToLower(strings.TrimSpace(thinkingType)) {
	case "enabled", "adaptive":
		body, err := sjson.SetBytes(httpReq.Body, "enable_thinking", true)
		if err != nil {
			return
		}

		body, err = sjson.DeleteBytes(body, "reasoning_effort")
		if err != nil {
			return
		}

		httpReq.Body = body
	case "disabled":
		body, err := sjson.SetBytes(httpReq.Body, "enable_thinking", false)
		if err != nil {
			return
		}

		body, err = sjson.DeleteBytes(body, "reasoning_effort")
		if err != nil {
			return
		}

		httpReq.Body = body
	}
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
