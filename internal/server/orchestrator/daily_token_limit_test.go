package orchestrator

import (
	"context"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/server/biz"
)

func TestDailyTokens_AddAndGet(t *testing.T) {
	tracker := NewChannelRequestTracker()
	const channelID = 42

	require.Equal(t, int64(0), tracker.GetDailyTokenCount(channelID))

	tracker.AddDailyTokens(channelID, 100)
	tracker.AddDailyTokens(channelID, 250)
	assert.Equal(t, int64(350), tracker.GetDailyTokenCount(channelID))

	// Negative or zero increments are no-ops.
	tracker.AddDailyTokens(channelID, 0)
	tracker.AddDailyTokens(channelID, -5)
	assert.Equal(t, int64(350), tracker.GetDailyTokenCount(channelID))
}

func TestAddTokens_AlsoUpdatesDailyWindow(t *testing.T) {
	tracker := NewChannelRequestTracker()
	const channelID = 7

	tracker.AddTokens(channelID, 1000)

	assert.Equal(t, int64(1000), tracker.GetTokenCount(channelID),
		"per-minute window should advance")
	assert.Equal(t, int64(1000), tracker.GetDailyTokenCount(channelID),
		"daily window should advance from the same call")
}

func TestRateLimitStrategy_ExhaustsOnDailyTokenLimit(t *testing.T) {
	tracker := NewChannelRequestTracker()
	strategy := NewRateLimitAwareStrategy(tracker, nil)

	limit := int64(5_000)
	channel := &biz.Channel{
		Channel: &ent.Channel{
			ID:   1,
			Name: "codex-subscription",
			Settings: &objects.ChannelSettings{
				RateLimit: &objects.ChannelRateLimit{
					DailyTokenLimit: lo.ToPtr(limit),
				},
			},
		},
	}

	tracker.AddTokens(channel.ID, limit) // hit the limit exactly
	score := strategy.Score(context.Background(), channel)
	assert.Equal(t, float64(rateLimitExhaustedScore), score,
		"channel should be exhausted when at or above the daily token limit")
}

func TestRateLimitStrategy_ScalesWithDailyUsageRatio(t *testing.T) {
	tracker := NewChannelRequestTracker()
	strategy := NewRateLimitAwareStrategy(tracker, nil)

	channel := &biz.Channel{
		Channel: &ent.Channel{
			ID:   2,
			Name: "codex-subscription",
			Settings: &objects.ChannelSettings{
				RateLimit: &objects.ChannelRateLimit{
					DailyTokenLimit: lo.ToPtr(int64(10_000)),
				},
			},
		},
	}

	tracker.AddTokens(channel.ID, 5_000)
	score := strategy.Score(context.Background(), channel)
	assert.Greater(t, score, float64(0), "should not be exhausted at 50%")
	assert.Less(t, score, strategy.maxScore, "score should be reduced from max at 50% usage")
}

func TestRateLimitStrategy_DailyLimitIndependentOfTPM(t *testing.T) {
	tracker := NewChannelRequestTracker()
	strategy := NewRateLimitAwareStrategy(tracker, nil)

	// TPM huge, daily small. Daily exhaustion should still kick in.
	channel := &biz.Channel{
		Channel: &ent.Channel{
			ID:   3,
			Name: "codex",
			Settings: &objects.ChannelSettings{
				RateLimit: &objects.ChannelRateLimit{
					TPM:             lo.ToPtr(int64(1_000_000)),
					DailyTokenLimit: lo.ToPtr(int64(2_000)),
				},
			},
		},
	}

	tracker.AddTokens(channel.ID, 2_000)
	score := strategy.Score(context.Background(), channel)
	assert.Equal(t, float64(rateLimitExhaustedScore), score)
}
