package codex

import (
	"fmt"
	"os"
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
	arch := defaultArchToken()
	terminal := defaultTerminalToken()
	return sanitizeUserAgent(fmt.Sprintf("%s/%s (%s %s; %s) %s",
		DefaultOriginator,
		DefaultCodexCLIVersion,
		osType,
		osVersion,
		arch,
		terminal,
	))
}

func defaultOSInfo() (string, string) {
	switch runtime.GOOS {
	case "linux":
		return "Linux", "6.0"
	case "darwin":
		return "Mac OS", "14.5.0"
	case "windows":
		return "Windows", "10.0.0"
	default:
		caser := strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
		return caser, "1.0"
	}
}

func defaultArchToken() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

// defaultTerminalToken mirrors codex-rs/terminal-detection at a coarse level:
// TERM_PROGRAM first, then well-known terminal env vars, then TERM, finally "unknown".
func defaultTerminalToken() string {
	if termProgram := strings.TrimSpace(os.Getenv("TERM_PROGRAM")); termProgram != "" {
		return sanitizeHeaderToken(formatTerminalVersion(termProgram, strings.TrimSpace(os.Getenv("TERM_PROGRAM_VERSION"))))
	}

	if version := strings.TrimSpace(os.Getenv("WEZTERM_VERSION")); version != "" {
		return sanitizeHeaderToken(formatTerminalVersion("WezTerm", version))
	}

	switch {
	case hasEnv("ITERM_SESSION_ID") || hasEnv("ITERM_PROFILE") || hasEnv("ITERM_PROFILE_NAME"):
		return "iTerm.app"
	case hasEnv("TERM_SESSION_ID"):
		return "Apple_Terminal"
	case hasEnv("KITTY_WINDOW_ID"):
		return "kitty"
	case hasEnv("ALACRITTY_SOCKET"):
		return "Alacritty"
	case hasEnv("KONSOLE_VERSION"):
		return sanitizeHeaderToken(formatTerminalVersion("Konsole", strings.TrimSpace(os.Getenv("KONSOLE_VERSION"))))
	case hasEnv("GNOME_TERMINAL_SCREEN"):
		return "gnome-terminal"
	case hasEnv("VTE_VERSION"):
		return sanitizeHeaderToken(formatTerminalVersion("VTE", strings.TrimSpace(os.Getenv("VTE_VERSION"))))
	case hasEnv("WT_SESSION"):
		return "WindowsTerminal"
	}

	if term := strings.TrimSpace(os.Getenv("TERM")); term != "" {
		return sanitizeHeaderToken(term)
	}

	return "unknown"
}

func formatTerminalVersion(name string, version string) string {
	if version == "" {
		return name
	}

	return name + "/" + version
}

func hasEnv(key string) bool {
	return strings.TrimSpace(os.Getenv(key)) != ""
}

func sanitizeUserAgent(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 0x20 && r <= 0x7e {
			return r
		}
		return '_'
	}, value)

	if value == "" {
		return DefaultOriginator
	}

	return value
}

func sanitizeHeaderToken(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.' || r == '/':
			return r
		default:
			return '_'
		}
	}, value)

	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}

	return value
}
