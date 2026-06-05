// Package harness prototypes the shared agent-config writer that ctxloom, ltk,
// and harp (tasks/sessions) would all build on. The linchpin is the owner
// predicate: how several independent tools write one .claude/settings.json and
// each reconcile ONLY its own entries.
//
// Claude Code validates settings.json with a strict (.strict()) schema that
// rejects unknown fields, so a tool cannot persist an "_owner" marker on its
// entries. Identity is therefore inferred from the command's executable token.
package harness

import "strings"

// Owner identifies the entries one tool writes, by the tool's executable
// basename. An entry is owned if its command's executable token resolves to
// Bin — path-, quote-, and verb-agnostic, so a callback's subcommand can drift
// ("ctxloom meta hud" -> "ctxloom hook hud") without orphaning the old form.
//
// Exec-token identity suffices because in the decoupled design each tool owns
// its own executable namespace (ctxloom / ltk / ctxtask) and never writes a
// foreign command: MCP servers live in .mcp.json, which is not strict-schema'd
// and carries a real marker, so they never need this predicate.
type Owner struct {
	Bin string
}

// Owns reports whether command is one this tool writes.
func (o Owner) Owns(command string) bool {
	return o.Bin != "" && execToken(command) == o.Bin
}

// execToken is the basename of a command's executable token, respecting a
// single layer of surrounding quotes (so a quoted path with spaces stays
// intact) and stripping a directory prefix and .exe suffix. Production should
// delegate to ltk's command parser, which also strips leading env assignments
// and unwraps sh -c / trivial wrappers.
func execToken(command string) string {
	s := strings.TrimSpace(command)
	if s == "" {
		return ""
	}
	var tok string
	switch s[0] {
	case '"', '\'':
		if i := strings.IndexByte(s[1:], s[0]); i >= 0 {
			tok = s[1 : 1+i]
		} else {
			tok = s[1:]
		}
	default:
		if i := strings.IndexAny(s, " \t"); i >= 0 {
			tok = s[:i]
		} else {
			tok = s
		}
	}
	if i := strings.LastIndexAny(tok, `/\`); i >= 0 {
		tok = tok[i+1:]
	}
	tok = strings.Trim(tok, `"'`)
	return strings.TrimSuffix(tok, ".exe")
}
