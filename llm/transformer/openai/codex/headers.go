package codex

import (
	"encoding/json"
	"net/http"
	"strings"
)

const (
	SessionHeader         = "Session_id"
	VersionHeader         = "Version"
	TurnStateHeader       = "X-Codex-Turn-State"
	TurnMetadataHeader    = "X-Codex-Turn-Metadata"
	WindowIDHeader        = "X-Codex-Window-Id"
	ClientRequestIDHeader = "X-Client-Request-Id"
	BetaFeaturesHeader    = "X-Codex-Beta-Features"
	// InstallationIDHeader matches codex-rs/core/src/client.rs X_CODEX_INSTALLATION_ID_HEADER.
	// Real codex_cli_rs sends this on every Responses API call AND duplicates it under
	// body.client_metadata.x-codex-installation-id (per codex-rs/core/src/client.rs:872-875).
	InstallationIDHeader = "X-Codex-Installation-Id"
)

type TurnMetadata struct {
	SessionID string `json:"session_id"`
}

var PassthroughHeaders = []string{
	VersionHeader,
	TurnStateHeader,
	TurnMetadataHeader,
	WindowIDHeader,
	ClientRequestIDHeader,
	BetaFeaturesHeader,
	InstallationIDHeader,
}

func HasCodexCallerTraits(headers http.Header) bool {
	if headers == nil {
		return false
	}

	originator := strings.ToLower(strings.TrimSpace(headers.Get("Originator")))
	if originator == strings.ToLower(DefaultOriginator) || strings.Contains(originator, "codex") {
		return true
	}

	userAgent := strings.ToLower(strings.TrimSpace(headers.Get("User-Agent")))
	if strings.Contains(userAgent, strings.ToLower(DefaultOriginator)+"/") {
		return true
	}

	for _, header := range []string{
		TurnStateHeader,
		TurnMetadataHeader,
		WindowIDHeader,
		ClientRequestIDHeader,
		BetaFeaturesHeader,
		InstallationIDHeader,
	} {
		if strings.TrimSpace(headers.Get(header)) != "" {
			return true
		}
	}

	version := strings.TrimSpace(headers.Get(VersionHeader))
	if isCodexCLIVersion(version) && strings.TrimSpace(headers.Get(SessionHeader)) != "" {
		return true
	}

	return false
}

func ExtractSessionIDFromTurnMetadata(raw string) string {
	if raw == "" {
		return ""
	}

	var payload TurnMetadata
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}

	return strings.TrimSpace(payload.SessionID)
}

func GetSessionIDFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}

	sessionID := strings.TrimSpace(headers.Get(SessionHeader))
	if sessionID != "" {
		return sessionID
	}

	return ExtractSessionIDFromTurnMetadata(strings.TrimSpace(headers.Get(TurnMetadataHeader)))
}
