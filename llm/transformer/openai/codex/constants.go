package codex

// DefaultModels returns a static list of Codex-capable model IDs.
//
// The ChatGPT Codex backend does not provide a stable public /models endpoint.
// CLIProxyAPI keeps a local registry; we mirror that approach to power AxonHub "Fetch Models".
func DefaultModels() []string {
	return []string{
		"gpt-5",
		"gpt-5-codex",
		"gpt-5-codex-mini",
		"gpt-5.1",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"gpt-5.1-codex-max",
		"gpt-5.2",
		"gpt-5.2-codex",
		"gpt-5.3-codex",
		"gpt-5.3-codex-spark",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.5",
		"codex-auto-review",
	}
}

const (
	// DefaultOriginator matches the real OpenAI Codex CLI value (codex-rs/login).
	// Using "axonhub" here is an obvious upstream-side fingerprint; the CLI sends "codex_cli_rs".
	DefaultOriginator = "codex_cli_rs"
	// AxonHubOriginator is kept as a deprecated alias for any external import.
	// Deprecated: use DefaultOriginator.
	AxonHubOriginator = DefaultOriginator
	AuthorizeURL      = "https://auth.openai.com/oauth/authorize"
	//nolint:gosec // false alert.
	TokenURL    = "https://auth.openai.com/oauth/token"
	ClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectURI = "http://localhost:1455/auth/callback"
	Scopes      = "openid profile email offline_access"

	ResponsesWebsocketBetaHeaderValue = "responses_websockets=2026-02-06"
)
