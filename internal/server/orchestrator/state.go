package orchestrator

import (
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

		if candidate.Channel.Settings.PassThroughBody {
			return true
		}
	}

	return false
}
