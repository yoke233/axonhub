package orchestrator

import (
	"context"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// PersistenceState holds shared state with channel management and retry capabilities.
// TODO: move the dependencies out of the state to make it a real state.
type PersistenceState struct {
	APIKey *ent.APIKey

	RequestService      *biz.RequestService
	UsageLogService     *biz.UsageLogService
	ChannelService      *biz.ChannelService
	PromptProvider      PromptProvider
	PromptProtecter     PromptProtecter
	RetryPolicyProvider RetryPolicyProvider
	CandidateSelector   CandidateSelector
	LoadBalancer        *LoadBalancer

	// Request state
	ModelMapper *ModelMapper
	// Proxy config, will be used to override channel's default proxy config.
	Proxy *httpclient.ProxyConfig

	// OriginalModel is the model after API key profile mapping, used for channel selection
	OriginalModel string
	RawRequest    *httpclient.Request
	LlmRequest    *llm.Request

	// OriginalRequestStream stores the client's original stream intent before any
	// candidate-specific forcing to provider-side streaming happens.
	OriginalRequestStream *bool

	// Persistence state
	Request     *ent.Request
	RequestExec *ent.RequestExecution

	// ChannelModelsCandidates is the primary state for channel selection
	ChannelModelsCandidates []*ChannelModelsCandidate
	// Candidate state - current candidate index of ChannelModelsCandidates
	CurrentCandidateIndex int
	// CurrentCandidate is the currently selected candidate of ChannelModelsCandidates
	CurrentCandidate *ChannelModelsCandidate
	// CurrentModelIndex is the current model index in CurrentCandidate.Models
	CurrentModelIndex int

	// Perf is the performance record for the current request.
	Perf *biz.PerformanceRecord

	// StreamCompleted tracks whether the stream has response successfully completed.
	// This is used to distinguish between a stream that was canceled mid-way
	// versus a stream that completed successfully but the client disconnected
	// immediately after receiving the last chunk.
	StreamCompleted bool

	// RawProviderResponse stores the raw provider response for non-stream response pass-through.
	RawProviderResponse *httpclient.Response

	// RawProviderRequest stores the actual outbound provider request for pass-through checks.
	RawProviderRequest *httpclient.Request

	// RawStreamCh receives raw provider stream events for stream response pass-through.
	RawStreamCh chan *httpclient.StreamEvent

	// RawStreamErrRef points to the current attempt's local error variable used by the
	// captureRawProviderStream fan-out goroutine. Using a per-attempt pointer (instead of
	// a single shared field) prevents data races when retries spawn a new goroutine before
	// the previous one has exited.
	RawStreamErrRef *error

	// RawStreamCancel cancels the current attempt's fan-out goroutine started by
	// captureRawProviderStream. Must be called in PrepareForRetry and NextChannel so the
	// abandoned goroutine exits promptly and releases its upstream HTTP connection.
	RawStreamCancel context.CancelFunc

	// PassThroughApplied records whether the inbound request body was substituted during pass-through.
	PassThroughApplied bool

	// OutboundStreamError records an error surfaced by the outbound transform layer for the
	// current attempt (e.g. an in-stream SSE `error` event parsed by the outbound transformer).
	// The raw provider stream underneath OutboundPersistentStream ends cleanly in that case, so
	// without this field the execution would be persisted with a generic "stream ended without
	// terminal event" error instead of the real upstream error.
	OutboundStreamError error
}

func (s *PersistenceState) ReleaseRequestPayloads() {
	if s == nil {
		return
	}

	s.ReleaseRawRequestBody()

	if s.LlmRequest != nil && s.LlmRequest.RawRequest != nil {
		s.LlmRequest.RawRequest.RawRequest = nil
	}

	s.RawRequest = nil
	s.LlmRequest = nil
}

func (s *PersistenceState) ReleaseRawRequestBody() {
	if s == nil {
		return
	}

	if s.RawRequest != nil {
		s.RawRequest.Body = nil
		s.RawRequest.JSONBody = nil
	}

	if s.LlmRequest != nil && s.LlmRequest.RawRequest != nil {
		s.LlmRequest.RawRequest.Body = nil
		s.LlmRequest.RawRequest.JSONBody = nil
	}
}

func (s *PersistenceState) NeedsRawRequestBodyForPassThrough() bool {
	if s == nil {
		return false
	}

	for _, candidate := range s.ChannelModelsCandidates {
		if candidate == nil || candidate.Channel == nil || candidate.Channel.Settings == nil {
			continue
		}

		if candidate.Channel.Settings.PassThroughBody != nil && *candidate.Channel.Settings.PassThroughBody {
			return true
		}
	}

	return false
}
