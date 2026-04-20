package codex

import (
	"fmt"
	"runtime"
	"strings"
)

// DefaultCodexCLIVersion is the codex_cli_rs build_version used in the default User-Agent.
// Bump when tracking a newer upstream release. The exact value is cosmetic; what matters
// for fingerprint parity is the prefix "codex_cli_rs/" and the overall format.
const DefaultCodexCLIVersion = "0.50.0"

// BuildDefaultCodexUserAgent returns a User-Agent string mirroring the format produced by
// real codex_cli_rs (see codex-rs/login/src/auth/default_client.rs::get_codex_user_agent).
//
// Format: "{originator}/{version} ({os_type} {os_version}; {arch}) {terminal_user_agent}"
// Example: "codex_cli_rs/0.50.0 (Linux 6.0; x86_64) xterm/0.1"
//
// This is only used as a fallback when the caller did not supply its own User-Agent.
// Real codex CLI requests already have a proper UA and are passed through unchanged.
func BuildDefaultCodexUserAgent() string {
	osType, osVersion := defaultOSInfo()
	arch := runtime.GOARCH
	terminal := defaultTerminalToken()
	return fmt.Sprintf("%s/%s (%s %s; %s) %s",
		DefaultOriginator,
		DefaultCodexCLIVersion,
		osType,
		osVersion,
		arch,
		terminal,
	)
}

func defaultOSInfo() (string, string) {
	switch runtime.GOOS {
	case "linux":
		return "Linux", "6.0"
	case "darwin":
		return "Macos", "14.5.0"
	case "windows":
		return "Windows", "10.0"
	default:
		caser := strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
		return caser, "1.0"
	}
}

// defaultTerminalToken approximates the terminal_user_agent suffix in real codex CLI.
// codex_cli_rs derives this from terminfo/env; we use a generic but realistic value.
func defaultTerminalToken() string {
	return "xterm/0.1"
}
