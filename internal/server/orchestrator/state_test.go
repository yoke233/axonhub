package orchestrator

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/ent"
	"github.com/looplj/axonhub/internal/objects"
	"github.com/looplj/axonhub/internal/server/biz"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestPersistenceStateReleaseRequestPayloads(t *testing.T) {
	raw := &httpclient.Request{
		Body:       []byte("large request body"),
		JSONBody:   []byte(`{"body":"large"}`),
		RawRequest: &http.Request{},
	}
	state := &PersistenceState{
		RawRequest: raw,
		LlmRequest: &llm.Request{
			RawRequest: raw,
		},
	}

	state.ReleaseRequestPayloads()

	require.Nil(t, state.RawRequest)
	require.Nil(t, state.LlmRequest)
	require.Nil(t, raw.Body)
	require.Nil(t, raw.JSONBody)
	require.Nil(t, raw.RawRequest)
}

func TestPersistenceStateReleaseRawRequestBody(t *testing.T) {
	raw := &httpclient.Request{
		Body:       []byte("large request body"),
		JSONBody:   []byte(`{"body":"large"}`),
		RawRequest: &http.Request{},
	}
	state := &PersistenceState{
		RawRequest: raw,
		LlmRequest: &llm.Request{
			RawRequest: raw,
		},
	}

	state.ReleaseRawRequestBody()

	require.NotNil(t, state.RawRequest)
	require.NotNil(t, state.LlmRequest)
	require.Nil(t, raw.Body)
	require.Nil(t, raw.JSONBody)
	require.NotNil(t, raw.RawRequest)
}

func TestPersistenceStateNeedsRawRequestBodyForPassThrough(t *testing.T) {
	passThroughBody := true

	noPassThrough := &PersistenceState{
		ChannelModelsCandidates: []*ChannelModelsCandidate{
			{Channel: &biz.Channel{Channel: &ent.Channel{Settings: &objects.ChannelSettings{}}}},
		},
	}
	require.False(t, noPassThrough.NeedsRawRequestBodyForPassThrough())

	withPassThrough := &PersistenceState{
		ChannelModelsCandidates: []*ChannelModelsCandidate{
			{Channel: &biz.Channel{Channel: &ent.Channel{Settings: &objects.ChannelSettings{PassThroughBody: &passThroughBody}}}},
		},
	}
	require.True(t, withPassThrough.NeedsRawRequestBodyForPassThrough())
}
