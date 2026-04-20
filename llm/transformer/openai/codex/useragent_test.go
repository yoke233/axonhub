package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserAgent_DefaultFormat(t *testing.T) {
	ua := BuildDefaultCodexUserAgent()
	assert.True(t, strings.HasPrefix(ua, DefaultOriginator+"/"+DefaultCodexCLIVersion+" "),
		"UA should start with %s/%s , got %q", DefaultOriginator, DefaultCodexCLIVersion, ua)
	assert.Contains(t, ua, "(", "UA should contain os info section")
	assert.Contains(t, ua, ")", "UA should contain os info section")
	assert.NotContains(t, strings.ToLower(ua), "axonhub", "UA must not leak axonhub brand")
	assert.NotContains(t, strings.ToLower(ua), "go-http-client", "UA must not leak Go default")
}

func TestUserAgent_PassthroughCallerUA(t *testing.T) {
	ctx := context.Background()
	sim := newCodexSimulator(t)
	req := newCodexChatCompletionRequest(t)
	callerUA := "codex_cli_rs/0.99.0 (Linux 6.1; x86_64) iTerm.app/3.5.0"
	req.Header.Set("User-Agent", callerUA)

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, callerUA, finalReq.Header.Get("User-Agent"),
		"caller-supplied UA must be passed through unchanged")
}

func TestUserAgent_DefaultWhenAbsent(t *testing.T) {
	ctx := context.Background()
	sim := newCodexSimulator(t)
	req := newCodexChatCompletionRequest(t)
	// no UA on inbound

	finalReq, err := sim.Simulate(ctx, req)
	require.NoError(t, err)

	assert.Equal(t, BuildDefaultCodexUserAgent(), finalReq.Header.Get("User-Agent"))
	assert.NotEqual(t, "axonhub/1.0", finalReq.Header.Get("User-Agent"),
		"default UA must no longer be axonhub/1.0")
}
