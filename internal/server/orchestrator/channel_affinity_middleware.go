package orchestrator

import (
	"context"

	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/pipeline"
	"github.com/looplj/axonhub/llm/streams"
)

// recordChannelAffinity returns a pipeline middleware that records which channel
// was used for a successful request, so subsequent requests with the same
// session/cache key can be routed to the same channel.
func recordChannelAffinity(
	outbound *PersistentOutboundTransformer,
	store *ChannelAffinityStore,
) pipeline.Middleware {
	return &affinityRecorderMiddleware{
		outbound: outbound,
		store:    store,
	}
}

type affinityRecorderMiddleware struct {
	pipeline.DummyMiddleware
	outbound *PersistentOutboundTransformer
	store    *ChannelAffinityStore
}

func (m *affinityRecorderMiddleware) Name() string {
	return "channel-affinity-recorder"
}

// OnOutboundLlmResponse fires after a successful non-streaming response.
func (m *affinityRecorderMiddleware) OnOutboundLlmResponse(ctx context.Context, response *llm.Response) (*llm.Response, error) {
	m.record(ctx)
	return response, nil
}

// OnOutboundRawStream fires after a successful streaming response is established.
func (m *affinityRecorderMiddleware) OnOutboundRawStream(ctx context.Context, stream streams.Stream[*httpclient.StreamEvent]) (streams.Stream[*httpclient.StreamEvent], error) {
	m.record(ctx)
	return stream, nil
}

func (m *affinityRecorderMiddleware) record(ctx context.Context) {
	channel := m.outbound.GetCurrentChannel()
	if channel == nil {
		return
	}

	model := m.outbound.GetRequestedModel()
	if model == "" {
		return
	}

	req := m.outbound.state.LlmRequest
	m.store.Record(ctx, model, req, channel.ID)
}
