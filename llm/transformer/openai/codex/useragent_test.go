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

func TestUserAgent_UsesTERMProgramToken(t *testing.T) {
	clearKnownTerminalEnv(t)
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	t.Setenv("TERM_PROGRAM_VERSION", "1.2.3")

	ua := BuildDefaultCodexUserAgent()
	assert.Contains(t, ua, " WarpTerminal/1.2.3")
}

func TestUserAgent_FallsBackToTERM(t *testing.T) {
	clearKnownTerminalEnv(t)
	t.Setenv("TERM", "xterm-256color")

	ua := BuildDefaultCodexUserAgent()
	assert.Contains(t, ua, " xterm-256color")
}

func TestUserAgent_SanitizesTerminalToken(t *testing.T) {
	clearKnownTerminalEnv(t)
	t.Setenv("TERM_PROGRAM", "bad\rterm")
	t.Setenv("TERM_PROGRAM_VERSION", "1.0\nbeta")

	ua := BuildDefaultCodexUserAgent()
	assert.NotContains(t, ua, "\r")
	assert.NotContains(t, ua, "\n")
	assert.Contains(t, ua, " bad_term/1.0_beta")
}

func clearKnownTerminalEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"TERM",
		"TERM_PROGRAM",
		"TERM_PROGRAM_VERSION",
		"WEZTERM_VERSION",
		"ITERM_SESSION_ID",
		"ITERM_PROFILE",
		"ITERM_PROFILE_NAME",
		"TERM_SESSION_ID",
		"KITTY_WINDOW_ID",
		"ALACRITTY_SOCKET",
		"KONSOLE_VERSION",
		"GNOME_TERMINAL_SCREEN",
		"VTE_VERSION",
		"WT_SESSION",
	} {
		t.Setenv(key, "")
	}
}
