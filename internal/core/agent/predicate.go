package agent

import "strings"

// IsManaged reports whether command was written by the tool whose executable
// basename is bin. Identity is the command's executable token — path-, quote-,
// and verb-agnostic, so a callback's subcommand can drift ("ctxloom meta hud"
// -> "ctxloom hook hud") without orphaning the old form. Each tool (ctxloom /
// ltk / ctxtask) owns its own executable namespace, which is why exec-token
// identity suffices without an in-file marker (Claude Code's strict schema
// forbids one). bin is required — a tool reconciles only its OWN entries, never
// "any ctxloom-family tool's", so it must not touch a sibling's hooks.
func IsManaged(command, bin string) bool {
	return bin != "" && execToken(command) == bin
}

// execToken returns the basename of a command's leading executable token,
// honoring a single layer of surrounding quotes (so a quoted path with spaces
// stays intact — `"/Apps/My Tools/ctxloom" mcp` → `ctxloom`), then stripping a
// directory prefix, a `.exe` suffix, and any stray surrounding quotes. It is
// intentionally minimal — just enough to identify the executable, not a full
// shell-word parser (production may delegate to ltk's parser for env-prefix /
// `sh -c` wrappers).
func execToken(command string) string {
	exe := firstToken(command)
	if i := strings.LastIndexAny(exe, `/\`); i >= 0 {
		exe = exe[i+1:]
	}
	exe = strings.TrimSuffix(exe, ".exe")
	return strings.Trim(exe, `"'`)
}

func firstToken(command string) string {
	command = strings.TrimLeft(command, " \t")
	if command == "" {
		return ""
	}
	if q := command[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(command[1:], q); end >= 0 {
			return command[1 : 1+end]
		}
		return command[1:] // unterminated quote: take the remainder
	}
	if i := strings.IndexAny(command, " \t"); i >= 0 {
		return command[:i]
	}
	return command
}
