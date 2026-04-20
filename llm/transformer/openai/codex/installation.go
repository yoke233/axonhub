package codex

import (
	"encoding/json"
	"strings"
)

// clientMetadataInstallationKey is the body field codex_cli_rs uses to duplicate the
// installation id under the Responses API request body.client_metadata map (matches
// codex-rs/core/src/client.rs:872-875).
const clientMetadataInstallationKey = "x-codex-installation-id"

// resolveInstallationID picks the installation id in passthrough-first order:
//
//  1. raw inbound header from the caller (real codex CLI sends one), so we keep it.
//  2. channel-scoped fallback (persisted on the channel, mimicking ~/.codex/installation_id).
//  3. empty string when neither is available; caller may inject a per-request value.
func resolveInstallationID(rawHeaderValue, channelFallback string) string {
	if v := strings.TrimSpace(rawHeaderValue); v != "" {
		return v
	}
	return strings.TrimSpace(channelFallback)
}

// injectInstallationIDIntoBody mutates a JSON body to ensure
// client_metadata.x-codex-installation-id == installationID.
//
// If the body already has a client_metadata map, the key is set without disturbing
// other entries. If installationID is empty or body is empty/non-JSON, the body is
// returned unchanged so we never break upstream parsing.
func injectInstallationIDIntoBody(body []byte, installationID string) []byte {
	if len(body) == 0 || installationID == "" {
		return body
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}

	cm, ok := payload["client_metadata"].(map[string]any)
	if !ok {
		cm = map[string]any{}
	}
	cm[clientMetadataInstallationKey] = installationID
	payload["client_metadata"] = cm

	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}
