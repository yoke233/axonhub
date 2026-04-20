package orchestrator

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/ent/channel"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm/httpclient"
)

func newCodexChannel(id int) *biz.Channel {
	return &biz.Channel{
		Channel: &ent.Channel{
			ID:   id,
			Name: "codex-test",
			Type: channel.TypeCodex,
		},
	}
}

func newOpenAIChannel(id int) *biz.Channel {
	return &biz.Channel{
		Channel: &ent.Channel{
			ID:   id,
			Name: "openai-test",
			Type: channel.TypeOpenai,
		},
	}
}

// stubOutbound builds a minimal PersistentOutboundTransformer wrapping the given channel
// so GetCurrentChannel returns it. We bypass real transformer construction since the
// rateLimitTracking middleware only consults outbound.GetCurrentChannel().
func newStubOutbound(ch *biz.Channel) *PersistentOutboundTransformer {
	return &PersistentOutboundTransformer{
		state: &PersistenceState{
			CurrentCandidate: &ChannelModelsCandidate{Channel: ch},
		},
	}
}

func TestCodexAutoSuspend_On401SetsLongCooldown(t *testing.T) {
	tracker := NewChannelRequestTracker()
	ch := newCodexChannel(1)

	mw := withRateLimitTracking(newStubOutbound(ch), tracker)
	rl, ok := mw.(*rateLimitTracking)
	require.True(t, ok)

	rl.OnOutboundRawError(context.Background(), &httpclient.Error{
		Method:     http.MethodPost,
		URL:        "https://chatgpt.com/backend-api/codex/responses",
		StatusCode: http.StatusUnauthorized,
		Status:     "401 Unauthorized",
		Headers:    http.Header{},
	})

	until, cooling := tracker.GetCooldownUntil(ch.ID)
	require.True(t, cooling, "401 on codex must set a cooldown")
	assert.True(t, until.After(time.Now().Add(5*time.Hour)),
		"cooldown should be on the order of hours, got %v", time.Until(until))
}

func TestCodexAutoSuspend_On403SetsLongCooldown(t *testing.T) {
	tracker := NewChannelRequestTracker()
	ch := newCodexChannel(2)
	rl := withRateLimitTracking(newStubOutbound(ch), tracker).(*rateLimitTracking)

	rl.OnOutboundRawError(context.Background(), &httpclient.Error{
		StatusCode: http.StatusForbidden,
		Headers:    http.Header{},
	})

	_, cooling := tracker.GetCooldownUntil(ch.ID)
	assert.True(t, cooling, "403 on codex must set a cooldown")
}

func TestCodexAutoSuspend_OnNon401SkipsCooldown(t *testing.T) {
	tracker := NewChannelRequestTracker()
	ch := newCodexChannel(3)
	rl := withRateLimitTracking(newStubOutbound(ch), tracker).(*rateLimitTracking)

	rl.OnOutboundRawError(context.Background(), &httpclient.Error{
		StatusCode: http.StatusInternalServerError,
		Headers:    http.Header{},
	})

	_, cooling := tracker.GetCooldownUntil(ch.ID)
	assert.False(t, cooling, "5xx must not auto-suspend codex; only 401/403 should")
}

func TestCodexAutoSuspend_NotAppliedToNonCodexChannels(t *testing.T) {
	tracker := NewChannelRequestTracker()
	ch := newOpenAIChannel(4)
	rl := withRateLimitTracking(newStubOutbound(ch), tracker).(*rateLimitTracking)

	rl.OnOutboundRawError(context.Background(), &httpclient.Error{
		StatusCode: http.StatusUnauthorized,
		Headers:    http.Header{},
	})

	_, cooling := tracker.GetCooldownUntil(ch.ID)
	assert.False(t, cooling,
		"non-codex 401 should not trigger long auto-suspend (per-key disable handles those)")
}

func TestCodexAutoSuspend_StrategySkipsCooldownChannel(t *testing.T) {
	tracker := NewChannelRequestTracker()
	ch := newCodexChannel(5)
	rl := withRateLimitTracking(newStubOutbound(ch), tracker).(*rateLimitTracking)
	strategy := NewRateLimitAwareStrategy(tracker, nil)

	rl.OnOutboundRawError(context.Background(), &httpclient.Error{
		StatusCode: http.StatusUnauthorized,
		Headers:    http.Header{},
	})

	score := strategy.Score(context.Background(), ch)
	assert.Equal(t, float64(rateLimitExhaustedScore), score,
		"after auto-suspend the load balancer must rank the channel last")
}
