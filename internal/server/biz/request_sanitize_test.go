package biz

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/looplj/axonhub/internal/objects"
)

func TestSanitizeStoredPayload_RedactsInlineBase64Media(t *testing.T) {
	raw := objects.JSONRawMessage(`{
		"input":[
			{
				"type":"message",
				"content":[
					{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ"},
					{"type":"input_text","text":"keep this"}
				]
			}
		],
		"data":[{"b64_json":"AAECAwQFBgcICQoLDA0ODw=="}]
	}`)

	got := sanitizeStoredPayload(raw)

	require.True(t, json.Valid(got))
	require.NotContains(t, string(got), "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ")
	require.NotContains(t, string(got), "AAECAwQFBgcICQoLDA0ODw==")
	require.Contains(t, string(got), "data:image/png;base64,[redacted]")
	require.Contains(t, string(got), `"b64_json":"[redacted]"`)
	require.Contains(t, string(got), "keep this")
}

func TestSanitizeStoredPayload_NoInlineMediaReturnsEquivalentPayload(t *testing.T) {
	raw := objects.JSONRawMessage(`{"input":"hello","image_url":"https://example.com/image.png"}`)

	got := sanitizeStoredPayload(raw)

	require.JSONEq(t, string(raw), string(got))
}

func TestStoredPayloadFromBytes_StoresRawJSONInsteadOfBase64EncodedBytes(t *testing.T) {
	raw := []byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,AAAABBBB"}]}`)

	got, err := storedPayloadFromBytes(raw)

	require.NoError(t, err)
	require.True(t, json.Valid(got))
	require.NotContains(t, string(got), "eyJpbnB1dCI")
	require.Contains(t, string(got), `"image_url":"data:image/png;base64,[redacted]"`)
}

func TestStoredPayloadFromBytes_NonJSONFallsBackToJSONString(t *testing.T) {
	raw := []byte("plain text")

	got, err := storedPayloadFromBytes(raw)

	require.NoError(t, err)
	require.JSONEq(t, `"cGxhaW4gdGV4dA=="`, string(got))
}
