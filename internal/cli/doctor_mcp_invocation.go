package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// doctorMCPInvocationSurfaces are the engine-native MCP registries a ctxloom
// install materializes under a project, relative to its root. Each mirrors the
// path its engine's own writer produces:
//
//	claude       ClaudeCodeHookWriter.MCPConfigPath  (.mcp.json)
//	kiro         KiroWriter.mcpPath                  (.kiro/settings/mcp.json)
//	codex        CodexHookWriter.SettingsPath        (config.toml under the
//	             project-scoped CODEX_HOME, which folds [mcp_servers] in beside
//	             [hooks] — codex has no separate MCP file, which is why this
//	             list is paths and not writers)
//	opencode     OpencodeWriter.SettingsPath         (opencode.json, likewise
//	             folded, under its own "mcp" key)
//
// codex's entry is COMPUTED from its own writer rather than spelled out, and
// that is the point: codex is the one engine whose home ctxloom relocates
// (internal/codex.ProjectHome), so a literal here would be a second opinion about
// where the file is — exactly the run-path/static-path split the shared helper
// exists to close. Passing an empty project root yields the project-relative
// path this list wants.
//
// A user-global surface (~/.claude.json) is deliberately absent: this check
// reports what THIS project materialized, and a fix it names ('ctxloom init'
// in this project) would not reach a home-scoped entry anyway.
var doctorMCPInvocationSurfaces = []string{
	claude.MCPFileName,
	filepath.Join(".kiro", "settings", "mcp.json"),
	(&codex.CodexHookWriter{}).SettingsPath(""),
	"opencode.json",
}

// doctorCheckMCPInvocation reports any materialized ctxloom MCP entry whose
// invocation is the bare `mcp` noun rather than the `mcp serve` leaf.
//
// WHY THIS CHECK EXISTS AT ALL. The break itself is fine — re-init is this
// project's documented upgrade path — but its UNTREATED shape is not. An
// engine launching a stale entry starts normally, ctxloom answers the bare
// noun, and the client waits forever on a JSON-RPC frame that never comes:
// exit 0, a live session, no ctxloom tools, no diagnostic. Nothing else in the
// system can see that, because from every other vantage point the install is
// perfectly wired — HarnessStatus reports the entry PRESENT, which it is.
// Reading the argv is the only thing that can tell a working entry from a
// hanging one.
//
// Pure file inspection: it reads what the engines read and launches nothing.
func doctorCheckMCPInvocation(projectDir string) doctorCheck {
	const marker = "DOCTOR-CHECK-MCP-INVOCATION-g7"
	if projectDir == "" {
		return doctorCheck{Marker: marker, Status: doctorInfo,
			Detail: "no project directory to check"}
	}

	var stale, unreadable []string
	for _, rel := range doctorMCPInvocationSurfaces {
		data, err := os.ReadFile(filepath.Join(projectDir, rel))
		if err != nil {
			// An absent surface is the common case (nobody configures five
			// engines), not a finding.
			if !os.IsNotExist(err) {
				unreadable = append(unreadable, fmt.Sprintf("%s (%v)", rel, err))
			}
			continue
		}
		bare, err := mcpSurfaceNamesBareNoun(rel, data)
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s (%v)", rel, err))
			continue
		}
		if bare {
			stale = append(stale, rel)
		}
	}

	sort.Strings(stale)
	sort.Strings(unreadable)
	switch {
	case len(stale) > 0:
		detail := fmt.Sprintf(
			"%d materialized MCP entr(y/ies) invoke ctxloom as the bare `mcp` noun, which lists configured servers instead of speaking the protocol — the engine will start and its ctxloom tools will never appear: %s. Re-run `ctxloom init` to rewrite every engine's settings with `%s`",
			len(stale), strings.Join(stale, ", "), strings.Join(agent.CtxloomMCPArgs, " "))
		if len(unreadable) > 0 {
			detail += "; could not read: " + strings.Join(unreadable, ", ")
		}
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: detail}
	case len(unreadable) > 0:
		// "I could not read it" is not "it is fine". A surface that failed to
		// parse may hold the very entry this check is looking for, and
		// reporting ok beside it would claim an inspection that did not happen.
		return doctorCheck{Marker: marker, Status: doctorWarn, Detail: fmt.Sprintf(
			"%d MCP registr(y/ies) could not be read, so their ctxloom invocation is unverified: %s",
			len(unreadable), strings.Join(unreadable, ", "))}
	default:
		return doctorCheck{Marker: marker, Status: doctorOK, Detail: fmt.Sprintf(
			"every materialized ctxloom MCP entry invokes `%s`", strings.Join(agent.CtxloomMCPArgs, " "))}
	}
}

// mcpSurfaceNamesBareNoun reports whether rel's bytes carry a ctxloom MCP
// entry invoking the bare noun.
//
// The four surfaces put their server table under three different keys
// ("mcpServers", "mcp", "mcp_servers") at two different depths, so the search
// is a walk for an entry NAMED ctxloom that looks like a server, rather than
// five hand-written path lookups that would each have to be revisited the day
// an engine moves its table. Only the file's ENCODING is dispatched on, which
// is the one thing the shapes genuinely disagree about.
func mcpSurfaceNamesBareNoun(rel string, data []byte) (bool, error) {
	var root map[string]any
	decode := json.Unmarshal
	if strings.EqualFold(filepath.Ext(rel), ".toml") {
		decode = toml.Unmarshal
	}
	if err := decode(data, &root); err != nil {
		return false, err
	}
	return walkForBareCtxloomMCP(root), nil
}

// walkForBareCtxloomMCP descends any decoded registry looking for a server
// entry keyed by ctxloom's own well-known name whose invocation is the bare
// noun.
func walkForBareCtxloomMCP(node map[string]any) bool {
	if entry, ok := node[agent.MCPServerName].(map[string]any); ok {
		if tokens, isServer := mcpEntryInvocation(entry); isServer {
			return len(tokens) == 1 && tokens[0] == mcpNounToken
		}
	}
	for _, v := range node {
		if child, ok := v.(map[string]any); ok && walkForBareCtxloomMCP(child) {
			return true
		}
	}
	return false
}

// mcpNounToken is the noun on its own — the argv a stale entry carries.
const mcpNounToken = "mcp"

// mcpEntryInvocation returns the ctxloom subcommand tokens a decoded server
// entry launches, and whether the entry is a stdio server at all.
//
// Two spellings are in play and both are the engine's own, not a ctxloom
// choice: most registries carry `command` plus an `args` array, while
// opencode.json folds the binary and its arguments into ONE `command` array.
// A remote (url/serverUrl) entry is user-authored and names no subcommand, so
// it reports false rather than an empty token list — "not a stdio server" and
// "a stdio server invoking nothing" are different findings, and only the
// second would ever be ctxloom's to report.
func mcpEntryInvocation(entry map[string]any) (tokens []string, isServer bool) {
	if raw, ok := entry["args"]; ok {
		if args, ok := stringSlice(raw); ok {
			return args, true
		}
	}
	if raw, ok := entry["command"]; ok {
		// The one-array form: element 0 is the binary, the rest is the
		// invocation. A plain string command with no args names no subcommand.
		if argv, ok := stringSlice(raw); ok && len(argv) > 0 {
			return argv[1:], true
		}
		if _, isString := raw.(string); isString {
			return nil, true
		}
	}
	return nil, false
}

// stringSlice narrows a decoded JSON/TOML array to its string elements,
// reporting false for anything that is not an all-string array.
func stringSlice(raw any) ([]string, bool) {
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
