package operations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/pelletier/go-toml/v2"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// doctorMCPInvocationSurfaces are the engine-native MCP registries a ctxloom
// install can leave under a project, as the probes a registered engine
// declares (agent.EngineCLI.ProbesFor): every cwd-scoped or
// cwd-ancestors-scoped ProbeKindMCP probe, read from the declaration the
// engine's own writer and the mock's impersonation of it both read, so this
// check cannot name a path an engine has stopped reading.
//
// A user-global surface (ScopeHome — ~/.claude.json) is deliberately absent:
// this check reports what THIS project materialized, and a fix it names
// ('ctxloom init' in this project) would not reach a home-scoped entry anyway.
func doctorMCPInvocationSurfaces(reg engine.Registry) []agent.CLIProbe {
	type key struct {
		scope agent.ProbeScope
		rel   string
	}
	seen := map[key]bool{}
	var out []agent.CLIProbe
	for _, name := range EngineNames(reg) {
		clis, ok := agent.EngineCLIsIn(reg, name)
		if !ok {
			continue
		}
		for _, c := range clis {
			for _, p := range c.ProbesFor(agent.ProbeKindMCP) {
				k := key{p.Scope, p.Rel}
				if (p.Scope != agent.ScopeCwd && p.Scope != agent.ScopeCwdAncestors) || seen[k] {
					continue
				}
				seen[k] = true
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// probePaths is the single place a probe's scope becomes concrete paths for
// this check: absolute, in the order the engine consults them.
func probePaths(p agent.CLIProbe, cwd, home string) []string {
	return p.Paths(cwd, home)
}

// doctorCheckMCPInvocation reports any materialized ctxloom MCP entry that
// LAUNCHES ctxloom. ctxloom ships no stdio MCP server: its tools are served
// by the running session's endpoint, which the session injects into its own
// registry (URL + bearer) at start — so a project-side entry under ctxloom's
// name with a command is stale in every spelling.
//
// WHY THIS CHECK EXISTS AT ALL. The break itself is fine — `ctxloom
// materialize --release --surface mcp` takes the entry back out of the
// project's registry — but its UNTREATED shape is
// not. An engine launching a stale entry starts normally, the command
// answers something that is not the protocol, and the client waits forever
// on a JSON-RPC frame that never comes: exit 0, a live session, no ctxloom
// tools, no diagnostic. Nothing else in the system can see that, because
// from every other vantage point the install is perfectly wired — an entry
// is PRESENT, which it is. Reading what the entry launches is the only thing
// that can tell a rendered endpoint from a hanging launch.
//
// Pure file inspection: it reads what the engines read and launches nothing.
func doctorCheckMCPInvocation(reg engine.Registry, projectDir string) DoctorCheck {
	const marker = "DOCTOR-CHECK-MCP-INVOCATION-g7"
	if projectDir == "" {
		return DoctorCheck{Marker: marker, Status: DoctorInfo,
			Detail: "no project directory to check"}
	}

	stale, unreadable := scanMCPInvocations(reg, projectDir)
	sort.Strings(stale)
	sort.Strings(unreadable)
	switch {
	case len(stale) > 0:
		detail := fmt.Sprintf(
			"%d MCP entr(y/ies) launch ctxloom as a stdio server, which ctxloom no longer ships — the engine will start and its ctxloom tools will never appear: %s. ctxloom's tools are served by the running session's endpoint; the fix takes the entry out of the registry (a release removes an entry this machine's ctxloom recorded writing; delete any other by hand)",
			len(stale), strings.Join(stale, ", "))
		var outside []string
		for _, path := range stale {
			if rel, err := filepath.Rel(projectDir, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				outside = append(outside, path)
			}
		}
		remedy := doctorMCPReleaseRemedy
		if len(outside) > 0 {
			// The engine reads these from a parent folder, and the project-side
			// release only takes out entries it recorded writing inside the project.
			detail += "; outside this project, delete the ctxloom entry from each by hand: " + strings.Join(outside, ", ")
			if len(outside) == len(stale) {
				remedy = ""
			}
		}
		if len(unreadable) > 0 {
			detail += "; could not read: " + strings.Join(unreadable, ", ")
		}
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: detail, Remedy: remedy}
	case len(unreadable) > 0:
		// "I could not read it" is not "it is fine". A surface that failed to
		// parse may hold the very entry this check is looking for, and
		// reporting ok beside it would claim an inspection that did not happen.
		return DoctorCheck{Marker: marker, Status: DoctorWarn, Detail: fmt.Sprintf(
			"%d MCP registr(y/ies) could not be read, so their ctxloom invocation is unverified: %s",
			len(unreadable), strings.Join(unreadable, ", "))}
	default:
		return DoctorCheck{Marker: marker, Status: DoctorOK, Detail: "no materialized MCP entry launches ctxloom as a stdio server (its tools are served by the running session's endpoint)"}
	}
}

// scanMCPInvocations reads every file each project MCP surface resolves to
// from projectDir (probePaths), returning the absolute paths that still launch
// ctxloom as a stdio server and the ones that could not be read or parsed.
func scanMCPInvocations(reg engine.Registry, projectDir string) (stale, unreadable []string) {
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	seen := map[string]bool{}
	for _, p := range doctorMCPInvocationSurfaces(reg) {
		for _, path := range probePaths(p, projectDir, "") {
			if seen[path] {
				continue
			}
			seen[path] = true
			data, err := os.ReadFile(path)
			if err != nil {
				// An absent surface is the common case (nobody configures five
				// engines), not a finding.
				if !os.IsNotExist(err) {
					unreadable = append(unreadable, fmt.Sprintf("%s (%v)", path, err))
				}
				continue
			}
			launches, err := mcpSurfaceLaunchesCtxloom(path, data)
			if err != nil {
				unreadable = append(unreadable, fmt.Sprintf("%s (%v)", path, err))
				continue
			}
			if launches {
				stale = append(stale, path)
			}
		}
	}
	return stale, unreadable
}

// BareMCPEntryFiles names each MCP registry file, from dir up through its
// parents, holding a ctxloom entry that launches bare `ctxloom mcp` — the
// files whose entry makes an engine start a server that refuses to speak.
func BareMCPEntryFiles(reg engine.Registry, dir string) []string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	var out []string
	for _, p := range doctorMCPInvocationSurfaces(reg) {
		if p.Scope != agent.ScopeCwdAncestors {
			continue
		}
		for _, path := range probePaths(p, dir, "") {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if root, err := decodeSurface(path, data); err == nil && walkForBareCtxloomMCP(root) {
				out = append(out, path)
			}
		}
	}
	return out
}

// walkForBareCtxloomMCP is walkForCtxloomMCPLaunch narrowed to an entry whose
// whole invocation is the bare `mcp` noun.
func walkForBareCtxloomMCP(node map[string]any) bool {
	if entry, ok := node[agent.MCPServerName].(map[string]any); ok {
		if tokens, isServer := mcpEntryInvocation(entry); isServer && len(tokens) == 1 && tokens[0] == "mcp" {
			return true
		}
	}
	for _, v := range node {
		if child, ok := v.(map[string]any); ok && walkForBareCtxloomMCP(child) {
			return true
		}
	}
	return false
}

// mcpSurfaceLaunchesCtxloom reports whether rel's bytes carry a ctxloom MCP
// entry that launches a command.
//
// The surfaces put their server table under different keys ("mcpServers",
// "mcp", "mcp_servers") at different depths, so the search is a walk for an
// entry NAMED ctxloom that looks like a stdio server, rather than
// hand-written path lookups that would each have to be revisited the day an
// engine moves its table. Only the file's ENCODING is dispatched on, which
// is the one thing the shapes genuinely disagree about.
func mcpSurfaceLaunchesCtxloom(rel string, data []byte) (bool, error) {
	root, err := decodeSurface(rel, data)
	if err != nil {
		return false, err
	}
	return walkForCtxloomMCPLaunch(root), nil
}

// decodeSurface decodes an engine-native config file by the one thing its
// shapes genuinely disagree about, the encoding its name declares.
func decodeSurface(rel string, data []byte) (map[string]any, error) {
	var root map[string]any
	decode := json.Unmarshal
	if strings.EqualFold(filepath.Ext(rel), ".toml") {
		decode = toml.Unmarshal
	}
	if err := decode(data, &root); err != nil {
		return nil, err
	}
	return root, nil
}

// walkForCtxloomMCPLaunch descends any decoded registry looking for a server
// entry keyed by ctxloom's own well-known name that launches a command — a
// URL entry is a session's rendering of the endpoint and launches nothing.
func walkForCtxloomMCPLaunch(node map[string]any) bool {
	if entry, ok := node[agent.MCPServerName].(map[string]any); ok {
		if _, isServer := mcpEntryInvocation(entry); isServer {
			return true
		}
	}
	for _, v := range node {
		if child, ok := v.(map[string]any); ok && walkForCtxloomMCPLaunch(child) {
			return true
		}
	}
	return false
}

// mcpEntryInvocation returns the subcommand tokens a decoded server entry
// launches, and whether the entry is a stdio server at all.
//
// Two spellings are in play and both are the engine's own, not a ctxloom
// choice: most registries carry `command` plus an `args` array, while some
// fold the binary and its arguments into ONE `command` array.
// A remote (url/serverUrl) entry launches nothing, so it reports false
// rather than an empty token list — "not a stdio server" and "a stdio server
// invoking nothing" are different findings.
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
