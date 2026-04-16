package orchestrator

import (
	"context"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer/shared"
)

func TestChannelAffinityStore_LookupMiss(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()

	req := &llm.Request{Model: "claude-sonnet-4-20250514", PromptCacheKey: lo.ToPtr("key-1")}
	_, found := store.Lookup(ctx, "claude-sonnet-4-20250514", req)
	assert.False(t, found)
}

func TestChannelAffinityStore_RecordAndLookup(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()

	req := &llm.Request{Model: "claude-sonnet-4-20250514", PromptCacheKey: lo.ToPtr("anthropic-cache-v2-abc123")}

	store.Record(ctx, "claude-sonnet-4-20250514", req, 42)

	channelID, found := store.Lookup(ctx, "claude-sonnet-4-20250514", req)
	require.True(t, found)
	assert.Equal(t, 42, channelID)
}

func TestChannelAffinityStore_ScopedByModel(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()

	req := &llm.Request{PromptCacheKey: lo.ToPtr("same-key")}

	store.Record(ctx, "model-a", req, 10)
	store.Record(ctx, "model-b", req, 20)

	idA, found := store.Lookup(ctx, "model-a", req)
	require.True(t, found)
	assert.Equal(t, 10, idA)

	idB, found := store.Lookup(ctx, "model-b", req)
	require.True(t, found)
	assert.Equal(t, 20, idB)
}

func TestChannelAffinityStore_FallsBackToSessionID(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := shared.WithSessionID(context.Background(), "session-xyz")

	req := &llm.Request{Model: "gpt-4o"} // no PromptCacheKey

	store.Record(ctx, "gpt-4o", req, 77)

	channelID, found := store.Lookup(ctx, "gpt-4o", req)
	require.True(t, found)
	assert.Equal(t, 77, channelID)
}

func TestChannelAffinityStore_NoKeyReturnsNotFound(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()

	req := &llm.Request{Model: "gpt-4o"} // no PromptCacheKey, no session

	_, found := store.Lookup(ctx, "gpt-4o", req)
	assert.False(t, found)
}

func TestChannelAffinityStore_SessionIDTakesPriority(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := shared.WithSessionID(context.Background(), "session-1")

	req := &llm.Request{PromptCacheKey: lo.ToPtr("cache-key-1")}

	store.Record(ctx, "m", req, 55)

	// Lookup with same session but different PromptCacheKey should still hit,
	// because SessionID takes priority over PromptCacheKey.
	req2 := &llm.Request{PromptCacheKey: lo.ToPtr("cache-key-changed")}
	channelID, found := store.Lookup(ctx, "m", req2)
	require.True(t, found)
	assert.Equal(t, 55, channelID)
}

func TestChannelAffinityStore_PromptCacheKeyAsFallback(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background() // no session ID

	req := &llm.Request{PromptCacheKey: lo.ToPtr("cache-key-1")}

	store.Record(ctx, "m", req, 33)

	channelID, found := store.Lookup(ctx, "m", req)
	require.True(t, found)
	assert.Equal(t, 33, channelID)
}

// --- AffinitySelector tests ---

type staticSelector struct {
	candidates []*ChannelModelsCandidate
}

func (s *staticSelector) Select(_ context.Context, _ *llm.Request) ([]*ChannelModelsCandidate, error) {
	return s.candidates, nil
}

func makeCandidates(ids ...int) []*ChannelModelsCandidate {
	candidates := make([]*ChannelModelsCandidate, len(ids))
	for i, id := range ids {
		candidates[i] = &ChannelModelsCandidate{
			Channel: &biz.Channel{
				Channel: &ent.Channel{ID: id},
			},
			Priority: i,
		}
	}

	return candidates
}

func TestAffinitySelector_NoAffinityPassthrough(t *testing.T) {
	store := NewChannelAffinityStore()
	inner := &staticSelector{candidates: makeCandidates(1, 2, 3)}
	selector := WithAffinitySelector(inner, store)

	ctx := context.Background()
	req := &llm.Request{Model: "m", PromptCacheKey: lo.ToPtr("key-miss")}

	result, err := selector.Select(ctx, req)
	require.NoError(t, err)
	require.Len(t, result, 3)
	assert.Equal(t, 1, result[0].Channel.ID)
	assert.Equal(t, 2, result[1].Channel.ID)
	assert.Equal(t, 3, result[2].Channel.ID)
}

func TestAffinitySelector_PromoteCachedChannel(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()
	req := &llm.Request{Model: "m", PromptCacheKey: lo.ToPtr("key-1")}

	// Record channel 3 as preferred.
	store.Record(ctx, "m", req, 3)

	inner := &staticSelector{candidates: makeCandidates(1, 2, 3)}
	selector := WithAffinitySelector(inner, store)

	result, err := selector.Select(ctx, req)
	require.NoError(t, err)
	require.Len(t, result, 3)
	// Channel 3 should be promoted to first position.
	assert.Equal(t, 3, result[0].Channel.ID)
	assert.Equal(t, 1, result[1].Channel.ID)
	assert.Equal(t, 2, result[2].Channel.ID)
}

func TestAffinitySelector_CachedChannelAlreadyFirst(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()
	req := &llm.Request{Model: "m", PromptCacheKey: lo.ToPtr("key-1")}

	store.Record(ctx, "m", req, 1)

	inner := &staticSelector{candidates: makeCandidates(1, 2, 3)}
	selector := WithAffinitySelector(inner, store)

	result, err := selector.Select(ctx, req)
	require.NoError(t, err)
	// Order unchanged.
	assert.Equal(t, 1, result[0].Channel.ID)
	assert.Equal(t, 2, result[1].Channel.ID)
	assert.Equal(t, 3, result[2].Channel.ID)
}

func TestAffinitySelector_CachedChannelNotInCandidates(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()
	req := &llm.Request{Model: "m", PromptCacheKey: lo.ToPtr("key-1")}

	// Record channel 99 which doesn't exist among candidates.
	store.Record(ctx, "m", req, 99)

	inner := &staticSelector{candidates: makeCandidates(1, 2, 3)}
	selector := WithAffinitySelector(inner, store)

	result, err := selector.Select(ctx, req)
	require.NoError(t, err)
	// Order unchanged since 99 is not a candidate.
	assert.Equal(t, 1, result[0].Channel.ID)
	assert.Equal(t, 2, result[1].Channel.ID)
	assert.Equal(t, 3, result[2].Channel.ID)
}

func TestAffinitySelector_SingleCandidate(t *testing.T) {
	store := NewChannelAffinityStore()
	ctx := context.Background()
	req := &llm.Request{Model: "m", PromptCacheKey: lo.ToPtr("key-1")}

	store.Record(ctx, "m", req, 1)

	inner := &staticSelector{candidates: makeCandidates(1)}
	selector := WithAffinitySelector(inner, store)

	result, err := selector.Select(ctx, req)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, 1, result[0].Channel.ID)
}
