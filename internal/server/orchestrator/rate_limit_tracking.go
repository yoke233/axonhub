package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
)

// codexAuthFailureCooldown is the soft suspension applied to a codex channel after a
// 401/403 from the ChatGPT backend. Long enough to give a refresh / human intervention
// window; short enough that we recover automatically once the credential is fixed.
// Mirrors how real OpenAI clients back off when the subscription is flagged.
const codexAuthFailureCooldown = 6 * time.Hour

// withRateLimitTracking creates a middleware that tracks request counts per channel for rate limiting.
func withRateLimitTracking(outbound *PersistentOutboundTransformer, tracker *ChannelRequestTracker) pipeline.Middleware {
	if tracker == nil {
		return &noopRateLimitTracking{}
	}

	return &rateLimitTracking{
		outbound: outbound,
		tracker:  tracker,
	}
}

// rateLimitTracking is a middleware that increments request count for rate limiting.
type rateLimitTracking struct {
	pipeline.DummyMiddleware

	outbound *PersistentOutboundTransformer
	tracker  *ChannelRequestTracker
}

func (m *rateLimitTracking) Name() string {
	return "track-rate-limit"
}

func (m *rateLimitTracking) OnOutboundRawRequest(ctx context.Context, request *httpclient.Request) (*httpclient.Request, error) {
	channel := m.outbound.GetCurrentChannel()
	if channel == nil {
		return request, nil
	}

	m.tracker.IncrementRequest(channel.ID)

	if log.DebugEnabled(ctx) {
		log.Debug(ctx, "Incremented rate limit request count",
			log.Int("channel_id", channel.ID),
			log.String("channel_name", channel.Name),
			log.Int64("current_rpm", m.tracker.GetRequestCount(channel.ID)),
		)
	}

	return request, nil
}

func (m *rateLimitTracking) OnOutboundLlmResponse(ctx context.Context, response *llm.Response) (*llm.Response, error) {
	channel := m.outbound.GetCurrentChannel()
	if channel == nil || response == nil || response.Usage == nil {
		return response, nil
	}

	totalTokens := response.Usage.TotalTokens
	if totalTokens > 0 {
		m.tracker.AddTokens(channel.ID, totalTokens)

		if log.DebugEnabled(ctx) {
			log.Debug(ctx, "Incremented rate limit token count",
				log.Int("channel_id", channel.ID),
				log.String("channel_name", channel.Name),
				log.Int64("tokens", totalTokens),
				log.Int64("current_tpm", m.tracker.GetTokenCount(channel.ID)),
			)
		}
	}

	return response, nil
}

func (m *rateLimitTracking) OnOutboundLlmStream(ctx context.Context, stream streams.Stream[*llm.Response]) (streams.Stream[*llm.Response], error) {
	return &rateLimitTrackingStream{
		ctx:      ctx,
		stream:   stream,
		tracker:  m.tracker,
		outbound: m.outbound,
	}, nil
}

// OnOutboundRawError handles raw HTTP errors. It captures:
//   - 429 Too Many Requests with Retry-After: applies the upstream-supplied cooldown.
//   - 401/403 on codex channels: applies a long soft-suspend so we don't keep hammering
//     a token that the ChatGPT backend has rejected (often a sign that the subscription
//     was flagged for "API-style usage" — see provider_quota.codex_checker for context).
func (m *rateLimitTracking) OnOutboundRawError(ctx context.Context, err error) {
	// Safety check: outbound might be nil in edge cases
	if m.outbound == nil {
		return
	}

	ch := m.outbound.GetCurrentChannel()
	if ch == nil {
		return
	}

	// 429 with Retry-After (existing behavior).
	if httpclient.HasRetryAfterHeader(err) {
		if cooldown, ok := httpclient.ParseRetryAfter(err); ok {
			m.tracker.SetCooldown(ch.ID, time.Now().Add(cooldown))
			log.Warn(ctx, "channel cooling down due to 429",
				log.Int("channel_id", ch.ID),
				log.String("channel_name", ch.Name),
				log.Duration("cooldown", cooldown),
			)
		}
	}

	// 401/403 on codex channels: long auto-suspend (channel.Type checked because
	// transient auth failures on other providers usually mean a single bad key, not a
	// flagged account, and the per-key disable path handles those better).
	if ch.Channel != nil && ch.Channel.Type == channel.TypeCodex {
		if status, ok := httpStatusFromError(err); ok && (status == 401 || status == 403) {
			m.tracker.SetCooldown(ch.ID, time.Now().Add(codexAuthFailureCooldown))
			log.Warn(ctx, "codex channel auto-suspended after auth failure (likely subscription flag)",
				log.Int("channel_id", ch.ID),
				log.String("channel_name", ch.Name),
				log.Int("status", status),
				log.Duration("cooldown", codexAuthFailureCooldown),
			)
		}
	}
}

// httpStatusFromError extracts the HTTP status code from an httpclient.Error if present.
// Returns (0, false) when the error is not an httpclient.Error or has no status.
func httpStatusFromError(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var hcErr *httpclient.Error
	if errors.As(err, &hcErr) {
		return hcErr.StatusCode, true
	}
	return 0, false
}

// rateLimitTrackingStream wraps a stream to track token usage for rate limiting.
//
//nolint:containedctx // ctx is used for logging.
type rateLimitTrackingStream struct {
	ctx      context.Context
	stream   streams.Stream[*llm.Response]
	tracker  *ChannelRequestTracker
	outbound *PersistentOutboundTransformer
}

func (s *rateLimitTrackingStream) Current() *llm.Response {
	event := s.stream.Current()
	if event == nil {
		return event
	}

	// Track tokens if usage information is present (typically in the last chunk)
	if event.Usage != nil && event.Usage.TotalTokens > 0 {
		channel := s.outbound.GetCurrentChannel()
		if channel != nil {
			s.tracker.AddTokens(channel.ID, event.Usage.TotalTokens)

			if log.DebugEnabled(s.ctx) {
				log.Debug(s.ctx, "Incremented rate limit token count from stream",
					log.Int("channel_id", channel.ID),
					log.String("channel_name", channel.Name),
					log.Int64("tokens", event.Usage.TotalTokens),
					log.Int64("current_tpm", s.tracker.GetTokenCount(channel.ID)),
				)
			}
		}
	}

	return event
}

func (s *rateLimitTrackingStream) Next() bool {
	return s.stream.Next()
}

func (s *rateLimitTrackingStream) Close() error {
	return s.stream.Close()
}

func (s *rateLimitTrackingStream) Err() error {
	return s.stream.Err()
}

// noopRateLimitTracking is a no-op middleware when rate limit tracking is disabled.
type noopRateLimitTracking struct {
	pipeline.DummyMiddleware
}

func (m *noopRateLimitTracking) Name() string {
	return "track-rate-limit-noop"
}

func (m *noopRateLimitTracking) OnOutboundLlmStream(ctx context.Context, stream streams.Stream[*llm.Response]) (streams.Stream[*llm.Response], error) {
	return stream, nil
}
