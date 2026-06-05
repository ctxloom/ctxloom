package agent

import "strings"

// Owner identifies the settings entries written by one tool, by the tool's
// executable basename. An entry is owned if its command's executable token
// resolves to Bin — path-, quote-, and verb-agnostic, so a callback's
// subcommand can drift ("ctxloom meta hud" -> "ctxloom hook hud") without
// orphaning the old form. Each tool (ctxloom / ltk / ctxtask) owns its own
// executable namespace, which is why exec-token identity suffices without an
// in-file marker (Claude Code's strict schema forbids one).
type Owner struct {
	Bin string
}

// Owns reports whether command is one this tool writes.
func (o Owner) Owns(command string) bool {
	return o.Bin != "" && execToken(command) == o.Bin
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
