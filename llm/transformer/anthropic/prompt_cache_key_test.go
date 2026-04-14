package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildPromptCacheKey_ExplicitCacheControlStableAndScoped(t *testing.T) {
	reqA := mustPromptCacheReq(t, `{
		"model":"claude-sonnet-4-6",
		"metadata":{"user_id":"user-a"},
		"system":[{"type":"text","text":"stable system","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"stable context","cache_control":{"type":"ephemeral"}}]}],
		"max_tokens":128
	}`)
	reqB := mustPromptCacheReq(t, `{
		"model":"claude-sonnet-4-6",
		"metadata":{"user_id":"user-a"},
		"system":[{"type":"text","text":"stable system","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"stable context","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"tail question"}]}
		],
		"max_tokens":128
	}`)
	reqC := mustPromptCacheReq(t, `{
		"model":"claude-sonnet-4-6",
		"metadata":{"user_id":"user-b"},
		"system":[{"type":"text","text":"stable system","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"stable context","cache_control":{"type":"ephemeral"}}]}],
		"max_tokens":128
	}`)
	reqD := mustPromptCacheReq(t, `{
		"model":"claude-sonnet-4-6",
		"metadata":{"user_id":"user-a"},
		"system":[{"type":"text","text":"changed system","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"stable context","cache_control":{"type":"ephemeral"}}]}],
		"max_tokens":128
	}`)

	keyA := BuildPromptCacheKey(reqA)
	keyB := BuildPromptCacheKey(reqB)
	keyC := BuildPromptCacheKey(reqC)
	keyD := BuildPromptCacheKey(reqD)

	require.NotEmpty(t, keyA)
	require.Equal(t, keyA, keyB)
	require.NotEqual(t, keyA, keyC)
	require.NotEqual(t, keyA, keyD)
}

func TestBuildPromptCacheKey_DerivesWhenCacheControlMissing(t *testing.T) {
	reqA := mustPromptCacheReq(t, `{
		"model":"gpt-5.4",
		"metadata":{"user_id":"user-a"},
		"system":"long stable system",
		"messages":[{"role":"user","content":"stable context"}],
		"max_tokens":128
	}`)
	reqB := mustPromptCacheReq(t, `{
		"model":"gpt-5.4",
		"metadata":{"user_id":"user-a"},
		"system":"long stable system",
		"messages":[
			{"role":"user","content":"stable context"},
			{"role":"assistant","content":"assistant reply"},
			{"role":"user","content":"new tail"}
		],
		"max_tokens":128
	}`)

	keyA := BuildPromptCacheKey(reqA)
	keyB := BuildPromptCacheKey(reqB)

	require.NotEmpty(t, keyA)
	require.Equal(t, keyA, keyB)
	require.Equal(t, "derived", PromptCacheKeyModeForRequest(reqA))
}

func TestBuildPromptCacheKey_IncludesToolsForDerivedMode(t *testing.T) {
	reqA := mustPromptCacheReq(t, `{
		"model":"gpt-5.4",
		"metadata":{"user_id":"user-a"},
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"name":"tool_a","description":"A","input_schema":{"type":"object","properties":{}}}],
		"max_tokens":128
	}`)
	reqB := mustPromptCacheReq(t, `{
		"model":"gpt-5.4",
		"metadata":{"user_id":"user-a"},
		"messages":[{"role":"user","content":"hello"}],
		"tools":[{"name":"tool_b","description":"B","input_schema":{"type":"object","properties":{}}}],
		"max_tokens":128
	}`)

	require.NotEqual(t, BuildPromptCacheKey(reqA), BuildPromptCacheKey(reqB))
}

func mustPromptCacheReq(t *testing.T, body string) *MessageRequest {
	t.Helper()
	var req MessageRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}
