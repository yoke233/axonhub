package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/simulator"
	"github.com/looplj/axonhub/llm/transformer/openai"
)

func TestInstallationID_PassthroughWinsOverChannelFallback(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithParams(t, accessToken, Params{
		InstallationID: "channel-fallback-uuid",
	})
	req := newCodexChatCompletionRequest(t)
	req.Header.Set(InstallationIDHeader, "caller-supplied-uuid")

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, "caller-supplied-uuid", finalReq.Header.Get(InstallationIDHeader),
		"caller-supplied installation id must be passed through")

	body := readHTTPBody(t, finalReq)
	assertClientMetadataInstallationID(t, body, "caller-supplied-uuid")
}

func TestInstallationID_FallbackToChannelWhenNoCallerHeader(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	const channelID = "11111111-2222-3333-4444-555555555555"

	sim := newCodexSimulatorWithParams(t, accessToken, Params{
		InstallationID: channelID,
	})
	req := newCodexChatCompletionRequest(t)
	// no InstallationIDHeader on inbound

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, channelID, finalReq.Header.Get(InstallationIDHeader),
		"channel-scoped fallback must be used when caller does not supply one")

	body := readHTTPBody(t, finalReq)
	assertClientMetadataInstallationID(t, body, channelID)
}

func TestInstallationID_OmittedWhenNoSourceProvided(t *testing.T) {
	ctx := context.Background()
	accessToken := testAccessTokenWithAccountID(t)
	sim := newCodexSimulatorWithParams(t, accessToken, Params{
		// InstallationID intentionally empty
	})
	req := newCodexChatCompletionRequest(t)

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	assert.Empty(t, finalReq.Header.Get(InstallationIDHeader),
		"with no caller header and no channel fallback, header must be absent")

	body := readHTTPBody(t, finalReq)

	// body must still be valid JSON; client_metadata key must not be set with empty value
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	if cm, ok := payload["client_metadata"].(map[string]any); ok {
		_, present := cm[clientMetadataInstallationKey]
		assert.False(t, present, "client_metadata key must be absent when no installation id is available")
	}
}

func TestInjectInstallationIDIntoBody_PreservesOtherClientMetadata(t *testing.T) {
	body := []byte(`{"model":"gpt-5","client_metadata":{"foo":"bar"}}`)
	out := injectInstallationIDIntoBody(body, "uuid-123")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(out, &payload))

	cm := payload["client_metadata"].(map[string]any)
	assert.Equal(t, "uuid-123", cm[clientMetadataInstallationKey])
	assert.Equal(t, "bar", cm["foo"], "existing client_metadata fields must be preserved")
}

func TestInjectInstallationIDIntoBody_SkipsEmptyOrInvalid(t *testing.T) {
	assert.Nil(t, injectInstallationIDIntoBody(nil, "uuid"))
	body := []byte(`{"model":"gpt-5"}`)
	assert.Equal(t, body, injectInstallationIDIntoBody(body, ""), "empty id is no-op")
	bad := []byte(`not json`)
	assert.Equal(t, bad, injectInstallationIDIntoBody(bad, "uuid"), "non-JSON body is preserved")
}

func TestResolveInstallationID_PassthroughOrder(t *testing.T) {
	assert.Equal(t, "raw", resolveInstallationID("raw", "fallback"))
	assert.Equal(t, "fallback", resolveInstallationID("", "fallback"))
	assert.Equal(t, "fallback", resolveInstallationID("  ", "fallback"))
	assert.Equal(t, "", resolveInstallationID("", ""))
}

// --- helpers ---

func newCodexSimulatorWithParams(t *testing.T, accessToken string, params Params) *simulator.Simulator {
	t.Helper()
	if params.TokenProvider == nil {
		params.TokenProvider = staticTokenGetter{
			creds: &oauth.OAuthCredentials{
				AccessToken: accessToken,
				ExpiresAt:   time.Now().Add(time.Hour),
			},
		}
	}
	inbound := openai.NewInboundTransformer()
	outbound, err := NewOutboundTransformer(params)
	require.NoError(t, err)
	return simulator.NewSimulator(inbound, outbound)
}

func readHTTPBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	if req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())
	return body
}

func assertClientMetadataInstallationID(t *testing.T, body []byte, expectedID string) {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload), "outgoing body should be valid JSON")
	cm, ok := payload["client_metadata"].(map[string]any)
	require.True(t, ok, "body should contain client_metadata")
	assert.Equal(t, expectedID, cm[clientMetadataInstallationKey])
}
