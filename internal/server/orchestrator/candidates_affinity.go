package orchestrator

import (
	"context"
	"slices"

	"github.com/looplj/axonhub/internal/log"
	"github.com/looplj/axonhub/llm"
)

// AffinitySelector is a CandidateSelector decorator that reorders candidates
// to prefer the channel recorded in the affinity cache. If no affinity exists
// or the cached channel is not among the candidates, the original order is preserved.
type AffinitySelector struct {
	wrapped CandidateSelector
	store   *ChannelAffinityStore
}

// WithAffinitySelector wraps a selector with channel affinity awareness.
func WithAffinitySelector(wrapped CandidateSelector, store *ChannelAffinityStore) *AffinitySelector {
	return &AffinitySelector{wrapped: wrapped, store: store}
}

func (s *AffinitySelector) Select(ctx context.Context, req *llm.Request) ([]*ChannelModelsCandidate, error) {
	candidates, err := s.wrapped.Select(ctx, req)
	if err != nil || len(candidates) <= 1 {
		return candidates, err
	}

	preferredID, found := s.store.Lookup(ctx, req.Model, req)
	if !found {
		return candidates, nil
	}

	// Find the index of the preferred channel.
	idx := slices.IndexFunc(candidates, func(c *ChannelModelsCandidate) bool {
		return c.Channel.ID == preferredID
	})

	if idx <= 0 {
		// Not found among candidates or already first — nothing to do.
		return candidates, nil
	}

	if log.DebugEnabled(ctx) {
		log.Debug(ctx, "channel affinity: promoting cached channel",
			log.Int("channel_id", preferredID),
			log.String("channel_name", candidates[idx].Channel.Name),
			log.Int("from_index", idx),
		)
	}

	// Move the preferred candidate to the front without allocating a new slice.
	preferred := candidates[idx]
	copy(candidates[1:idx+1], candidates[:idx])
	candidates[0] = preferred

	return candidates, nil
}
