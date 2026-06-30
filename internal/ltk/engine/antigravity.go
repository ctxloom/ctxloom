package engine

import (
	"errors"
	"os"
	"path/filepath"

	antigravitycli "github.com/ctxloom/ctxloom/internal/antigravity"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Antigravity adapts the Antigravity CLI (agy) PreToolUse hook protocol. The
// wire types — stdin payload, decision JSON — live in the shared
// github.com/ctxloom/ctxloom/internal/antigravity module (the single source of truth for agy's
// verified contract); this adapter only maps them onto ltk's Request/Response.
//
// Deny is a {"decision":"deny","reason":…} object on stdout with exit 0; the
// model receives the reason verbatim. An allow writes nothing and exits 0.
// agy fails OPEN on a crashing hook (non-zero exit ⇒ the tool call proceeds),
// so a denial must always be this well-formed decision, never an exit code.
type Antigravity struct{}

// Name returns the engine identifier.
func (Antigravity) Name() string { return "antigravity" }

// Decode extracts the command or file target from a PreToolUse payload.
func (Antigravity) Decode(input []byte) (Request, error) {
	p, err := antigravitycli.DecodeHookPayload(input)
	if err != nil {
		return Request{}, err
	}
	return Request{
		ToolName: p.ToolCall.Name,
		Command:  p.ToolCall.Args.CommandLine,
		Shell:    agShellForTool(p.ToolCall.Name),
		FilePath: p.ToolCall.Args.FilePath(),
	}, nil
}

// agShellForTool maps an agy tool name to a shell hint. agy's command tools
// execute via bash regardless of the user's $SHELL (verified: `echo $0` from
// run_command prints "bash" on a zsh host), so the hint is a strong bash —
// it bypasses defaults.shell/$SHELL resolution. File tools carry no dialect.
func agShellForTool(tool string) ir.Shell {
	switch tool {
	case antigravitycli.ToolRunCommand, antigravitycli.ToolExecuteCommand:
		return ir.ShellBash
	default:
		return ""
	}
}

// Encode renders a denial as agy's decision JSON on stdout with exit 0. An
// allow produces a zero Output: no bytes, exit 0 — agy proceeds normally.
func (Antigravity) Encode(resp Response) (Output, error) {
	if resp.Allow {
		return Output{}, nil
	}
	body, err := antigravitycli.EncodeDeny(resp.Message())
	if err != nil {
		return Output{}, err
	}
	return Output{Stdout: body, ExitCode: 0}, nil
}

// --- management surface (Antigravity specific) ---

// antigravityMatcher is the PreToolUse matcher (a regex over agy tool names):
// the shell tools (command rules) plus the file-mutating tools (path rules).
const antigravityMatcher = antigravitycli.ToolRunCommand + "|" +
	antigravitycli.ToolExecuteCommand + "|" +
	antigravitycli.ToolWriteToFile + "|" +
	antigravitycli.ToolReplaceFileContent

// errAntigravityNoGlobal: agy v1.0.7 has no usable global hooks.json —
// ~/.gemini/antigravity-cli/hooks.json is silently ignored, and a hooks.json
// under ~/.gemini/ or ~/.gemini/config/ hangs headless agy (verified). Erroring
// beats writing a dead (or harmful) file.
var errAntigravityNoGlobal = errors.New("antigravity reads hooks only from the workspace .agents/hooks.json; install per project")

// Detect scores Antigravity's relevance. .agents/ is a weak marker — agy's own
// subagents and other tools write scratch there — so a bare directory scores
// below ClaudeCode's .claude/ (registry order breaks ties in Claude's favor),
// while an actual .agents/hooks.json marks a genuine agy hook host.
func (Antigravity) Detect(dir string) int {
	if fi, err := os.Stat(antigravitycli.WorkspaceHooksPath(dir)); err == nil && !fi.IsDir() {
		return 2
	}
	if fi, err := os.Stat(filepath.Join(dir, antigravitycli.AgentsDir)); err == nil && fi.IsDir() {
		return 1
	}
	return 0
}

// SettingsPath is .agents/hooks.json (project), per the antigravity module's
// path convention. There is no global scope — see errAntigravityNoGlobal.
func (Antigravity) SettingsPath(dir string, global bool) (string, error) {
	if global {
		return "", errAntigravityNoGlobal
	}
	return antigravitycli.WorkspaceHooksPath(dir), nil
}

// HookCommand runs the evaluate subcommand. --engine is explicit because
// evaluate defaults to claude-code. agy executes hooks with the working
// directory set to <workspace>/.agents (verified), so a workspace-relative
// rules path must be rebased one level up to resolve.
func (Antigravity) HookCommand(bin, configPath string) string {
	cmd := bin + " evaluate --engine antigravity"
	if configPath != "" {
		if !filepath.IsAbs(configPath) {
			configPath = "../" + configPath
		}
		cmd += " --config " + quotePathIfNeeded(configPath)
	}
	return cmd
}

// Install merges a PreToolUse hook running command into the hooks.json bytes.
// agy prompts the user to trust a newly seen hook on first interactive run.
func (Antigravity) Install(settings []byte, command string) ([]byte, string, error) {
	return mergePreToolUseHook(settings, antigravityMatcher, command)
}

// Uninstall removes any PreToolUse hook running command from the hooks.json
// bytes. removed reports whether a matching hook was actually dropped.
func (Antigravity) Uninstall(settings []byte, command string) ([]byte, bool, error) {
	return removePreToolUseHook(settings, command)
}
