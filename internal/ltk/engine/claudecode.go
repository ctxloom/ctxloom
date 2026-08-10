package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	claudecli "github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/agent"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// ClaudeCode adapts the Claude Code PreToolUse hook protocol. The wire types
// — stdin payload, decision JSON — live in the shared github.com/ctxloom/ctxloom/internal/claude
// module (the org's single source of truth for the contract); this adapter
// only maps them onto ltk's Request/Response.
//
// A denial is a hookSpecificOutput object with permissionDecision "deny" on
// stdout. An allow writes nothing and exits 0, letting the normal permission
// flow proceed.
type ClaudeCode struct{}

// Name returns the engine identifier.
func (ClaudeCode) Name() string { return "claude-code" }

// Decode extracts the command from a PreToolUse payload and resolves the shell
// from the tool name. The tool the LLM chose is the authoritative shell signal:
// Claude Code's Bash tool runs in Git Bash (bash) on every platform, and its
// opt-in PowerShell tool spawns pwsh directly.
func (ClaudeCode) Decode(input []byte) (Request, error) {
	p, err := claudecli.DecodeHookPayload(input)
	if err != nil {
		return Request{}, err
	}
	// NotebookEdit spells its target notebook_path, not file_path. Fall back to
	// it so notebook edits face the same path rules as the other editing tools —
	// an empty FilePath here would let them sail past every rule.
	filePath := p.ToolInput.FilePath
	if filePath == "" {
		filePath = p.ToolInput.NotebookPath
	}
	return Request{
		ToolName:    p.ToolName,
		Command:     p.ToolInput.Command,
		Shell:       ccShellForTool(p.ToolName),
		FilePath:    filePath,
		ToolUngated: !claudeGatesTool(p.ToolName),
	}, nil
}

// ccShellForTool maps a Claude Code tool name to a shell hint. It only returns
// a value when the tool authoritatively names the dialect:
//
//   - PowerShell tool → pwsh (unambiguous).
//   - Bash tool → "" (deferred). The Bash tool runs in the user's system
//     default shell, which may be bash OR zsh; the adapter can't tell from the
//     payload. Returning "" lets the resolver apply defaults.shell (e.g. zsh)
//     before falling back to bash, instead of wrongly asserting bash here.
//   - any other / unknown tool → "".
func ccShellForTool(tool string) ir.Shell {
	switch {
	case strings.Contains(strings.ToLower(tool), "powershell"),
		strings.Contains(strings.ToLower(tool), "pwsh"):
		return ir.ShellPwsh
	default:
		return ""
	}
}

// Encode renders a denial as a PreToolUse permission decision on stdout with
// exit 0. An allow produces a zero Output: no bytes, exit 0, which lets Claude
// Code's normal permission flow proceed (it does NOT auto-approve). The
// allow/unanalyzed/deny arms are the shared contract; only the deny bytes are
// Claude Code's own.
func (ClaudeCode) Encode(resp Response) (Output, error) {
	return encodeDecision(resp, claudecli.EncodeDeny)
}

// --- management surface (Claude Code specific) ---

// claudeGatedTools is the set of Claude Code tools ltk knows how to read: the
// shell-bound tools (command rules) plus the file-editing tools (path rules).
//
// It is the SINGLE source of truth for both directions — the installed
// PreToolUse matcher below is derived from it, and Decode uses it to recognise
// an incoming payload. Two hand-maintained lists over a VENDOR-OWNED, mutating
// tool set is how one goes stale and silently stops gating (stark-boxer): the
// matcher can be self-healed on install, but nothing can heal a runtime that
// does not know it is looking at a tool it cannot read.
var claudeGatedTools = []string{"Bash", "PowerShell", "Edit", "Write", "MultiEdit", "NotebookEdit"}

// claudeMatcher is the PreToolUse matcher, derived from claudeGatedTools.
//
// Verified (code.claude.com/docs/en/hooks, "Matcher patterns", 2026-07): Claude
// Code evaluates a matcher containing only letters/digits/_/-/spaces/,/| as an
// EXACT string or list of exact strings, not a regex — which is what this
// value always is, since claudeGatedTools are plain PascalCase identifiers.
// So today, an entirely unknown tool name simply never fires this hook at all
// (the hook isn't invoked, and ToolUngated never comes up). Only a matcher
// containing an actual regex metacharacter — e.g. a hand-edited settings.json,
// or a future gated name that isn't a plain identifier — puts Claude Code on
// its unanchored-regex path, where an unrelated tool whose name contains a
// gated one as a substring (`Edit.*` also matches `NotebookEdit`) can fire it.
var claudeMatcher = strings.Join(claudeGatedTools, "|")

// claudeGatesTool reports whether ltk recognises this tool's exact name. A
// false here sets Request.ToolUngated, which cmd/ltk/evaluate.go denies on —
// the installed matcher fired (this tool reached the hook) but the payload
// schema is not one Decode is known to read correctly.
func claudeGatesTool(tool string) bool {
	return slices.Contains(claudeGatedTools, tool)
}

// Claude Code settings.json keys, used when merging/removing the hook in the
// untyped JSON document. Kept as named constants so the merge and remove paths
// (and the read-back checks) can never disagree on a key spelling.
const (
	keyHooks      = "hooks"
	keyPreToolUse = "PreToolUse"
	keyMatcher    = "matcher"
	keyType       = "type"
	keyCommand    = "command"
	typeCommand   = "command" // value of the "type" field for a command hook
)

// Detect scores Claude Code's relevance by the presence of a .claude directory.
func (ClaudeCode) Detect(dir string) int {
	if fi, err := os.Stat(filepath.Join(dir, ".claude")); err == nil && fi.IsDir() {
		return 2
	}
	return 0
}

// SettingsPath is .claude/settings.json (project) or ~/.claude/settings.json.
// The path conventions come from the claude module (the org's single source
// of truth for Claude Code's on-disk layout).
func (ClaudeCode) SettingsPath(dir string, global bool) (string, error) {
	if global {
		return claudecli.GlobalSettingsPath()
	}
	return claudecli.ProjectSettingsPath(dir), nil
}

// HookCommand runs the evaluate subcommand, optionally with a rules file.
func (ClaudeCode) HookCommand(bin, configPath string) string {
	cmd := bin + " evaluate"
	if configPath != "" {
		cmd += " --config " + quotePathIfNeeded(configPath)
	}
	return cmd
}

// quotePathIfNeeded quotes a path for the hook host's shell. Paths made only
// of conservative shell-neutral characters pass through unquoted, keeping
// existing installed hook commands byte-stable. Anything else — whitespace,
// but also metacharacters like ;, $(…), and backticks — is wrapped in double
// quotes with POSIX double-quote escaping (\, ", `, $), so a hostile path
// can't smuggle extra shell syntax into the hook command line. Escaping $
// deliberately stops env references in the path from expanding: $ is exactly
// how an injection rides in.
func quotePathIfNeeded(p string) string {
	if p != "" && strings.IndexFunc(p, isUnsafePathRune) < 0 {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range p {
		switch r {
		case '\\', '"', '`', '$':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// isUnsafePathRune reports whether r falls outside the shell-neutral set
// [A-Za-z0-9._/~-] that may appear on a command line unquoted.
func isUnsafePathRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case r == '.', r == '_', r == '/', r == '~', r == '-':
		return false
	}
	return true
}

// Install merges a PreToolUse hook running command into the settings JSON.
func (ClaudeCode) Install(settings []byte, command string) ([]byte, string, error) {
	return mergePreToolUseHook(settings, claudeMatcher, command)
}

// Uninstall removes any PreToolUse hook running command from the settings JSON.
func (ClaudeCode) Uninstall(settings []byte, command string) ([]byte, bool, error) {
	return removePreToolUseHook(settings, command)
}

// mergePreToolUseHook adds a PreToolUse command hook to a settings document
// without disturbing other settings. Idempotent; the returned note reports a
// repair (empty when there was nothing to fix). Shared across engines: a
// future engine reusing Claude Code's nested registration shape
// (hooks.PreToolUse[].matcher + hooks[].{type,command}) needs only its own
// matcher vocabulary, not a new merge implementation.
func mergePreToolUseHook(existing []byte, matcher, command string) ([]byte, string, error) {
	settings, err := decodeSettings(existing)
	if err != nil {
		return nil, "", err
	}
	hooks, err := childMap(settings, keyHooks)
	if err != nil {
		return nil, "", err
	}
	pre, err := childSlice(hooks, keyPreToolUse)
	if err != nil {
		return nil, "", err
	}
	note := ""
	switch entry := findCommandEntry(pre, command); {
	case entry == nil:
		pre = append(pre, map[string]any{
			keyMatcher: matcher,
			keyHooks: []any{
				map[string]any{keyType: typeCommand, keyCommand: command},
			},
		})
	case entry[keyMatcher] != matcher:
		// The hook is installed, but under an older, narrower matcher. Leaving it
		// would silently keep the tools added since then ungated, so upgrade the
		// matcher in place and report it.
		old, _ := entry[keyMatcher].(string)
		entry[keyMatcher] = matcher
		note = fmt.Sprintf("upgraded the installed hook's matcher (%q → %q)", old, matcher)
	}
	hooks[keyPreToolUse] = pre
	settings[keyHooks] = hooks
	out, err := agent.CanonicalJSON(settings)
	return out, note, err
}

// removePreToolUseHook removes PreToolUse inner hooks running command, pruning
// entries/keys that become empty. Idempotent. removed reports whether a matching
// hook was actually found and dropped (false ⇒ the document is unchanged, so the
// caller can skip the rewrite).
//
// It reads the document through the same accessors the merge uses, so a
// malformed settings file — a `hooks` that is not an object, a `PreToolUse`
// that is not an array — is REFUSED here exactly as it is on install. Reporting
// it as "no matching hook found" instead would tell the user their hook is not
// installed when the truth is that their settings could not be read.
// Pruning is conditional on having removed something: an empty container the
// user wrote themselves is theirs to keep.
func removePreToolUseHook(existing []byte, command string) ([]byte, bool, error) {
	settings, err := decodeSettings(existing)
	if err != nil {
		return nil, false, err
	}
	hooks, err := childMap(settings, keyHooks)
	if err != nil {
		return nil, false, err
	}
	pre, err := childSlice(hooks, keyPreToolUse)
	if err != nil {
		return nil, false, err
	}
	kept, removed := removeCommandEntries(pre, command)
	if !removed {
		out, err := agent.CanonicalJSON(settings)
		return out, false, err
	}
	if len(kept) == 0 {
		delete(hooks, keyPreToolUse)
	} else {
		hooks[keyPreToolUse] = kept
	}
	if len(hooks) == 0 {
		delete(settings, keyHooks)
	} else {
		settings[keyHooks] = hooks
	}
	out, err := agent.CanonicalJSON(settings)
	return out, removed, err
}

// removeCommandEntries drops inner hooks running command from each PreToolUse
// entry, and drops any entry left with no hooks. removed reports whether any of
// our hooks was actually dropped.
func removeCommandEntries(pre []any, command string) (kept []any, removed bool) {
	for _, e := range pre {
		em, ok := e.(map[string]any)
		if !ok {
			kept = append(kept, e)
			continue
		}
		hs, ok := em[keyHooks].([]any)
		if !ok {
			kept = append(kept, e)
			continue
		}
		keptHooks, dropped := filterOutCommand(hs, command)
		if dropped {
			removed = true
		}
		if len(keptHooks) == 0 {
			continue // entry had only our hook → drop it
		}
		em[keyHooks] = keptHooks
		kept = append(kept, em)
	}
	return kept, removed
}

// filterOutCommand returns the hooks whose command is not command, and whether
// any hook was dropped.
func filterOutCommand(hooks []any, command string) (kept []any, removed bool) {
	for _, h := range hooks {
		if hm, ok := h.(map[string]any); ok {
			if c, _ := hm[keyCommand].(string); c == command {
				removed = true
				continue
			}
		}
		kept = append(kept, h)
	}
	return kept, removed
}

// decodeSettings parses the user's settings document for a read-modify-write.
// UseNumber is load-bearing: every value in here is re-emitted by
// CanonicalJSON, so a decode that lands numbers in float64 rewrites any
// literal float64 cannot hold exactly (1234567890123456789 →
// 1234567890123456800) in a file this process only meant to add a hook to.
func decodeSettings(existing []byte) (map[string]any, error) {
	settings := map[string]any{}
	if trimmed := bytes.TrimSpace(existing); len(trimmed) > 0 {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&settings); err != nil {
			return nil, fmt.Errorf("parse existing settings: %w", err)
		}
	}
	return settings, nil
}

// childMap and childSlice read one sub-document out of the untyped settings
// map, materializing an empty one when the key is absent. Neither STORES what
// it returns: the caller writes the (possibly reallocated, in childSlice's
// case) result back itself, so a single contract covers both and no half-built
// document is left behind when a later step errors out.
func childMap(m map[string]any, key string) (map[string]any, error) {
	switch v := m[key].(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return v, nil
	default:
		return nil, fmt.Errorf("settings field %q is not an object", key)
	}
}

func childSlice(m map[string]any, key string) ([]any, error) {
	switch v := m[key].(type) {
	case nil:
		return []any{}, nil
	case []any:
		return v, nil
	default:
		return nil, fmt.Errorf("settings field %q is not an array", key)
	}
}

// findCommandEntry returns the first PreToolUse entry whose inner hooks run
// command, or nil when none does.
func findCommandEntry(pre []any, command string) map[string]any {
	for _, e := range pre {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hs, ok := em[keyHooks].([]any)
		if !ok {
			continue
		}
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok {
				if c, _ := hm[keyCommand].(string); c == command {
					return em
				}
			}
		}
	}
	return nil
}
