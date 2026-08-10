package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// evaluateFlags groups `evaluate`'s flag-bound locals so its RunE can be a
// named method rather than a closure over three loose variables.
type evaluateFlags struct {
	cfgPath    string
	shellName  string
	engineName string
}

func newEvaluateCmd() *cobra.Command {
	f := &evaluateFlags{}
	c := &cobra.Command{
		Use:   "evaluate",
		Short: "Evaluate a hook payload from stdin and emit an allow/deny decision",
		Long: `Evaluate reads a hook payload (JSON) on stdin and emits an allow/deny decision.

It handles both kinds of payload the editing agent sends:

  • a shell command (tool_input.command) — parsed and matched against command
    rules, in the shell the tool implies (or --shell to force one).
  • a file edit (tool_input.file_path) — matched against file rules (match.path):
    globs, directory subtrees (a trailing slash, e.g. vendor/), and the
    "@submodules" sentinel, which is resolved against this repo's .gitmodules so
    one rule blocks edits inside every submodule.

A denial is written in the engine's format (for Claude Code, a permissionDecision
on stdout, exit 0); an allow writes nothing. Intended to be run by the hook, not
by hand.`,
		Args: cobra.NoArgs,
		RunE: f.run,
	}
	c.Flags().StringVar(&f.cfgPath, "config", "", "path to rules YAML (default: search cwd)")
	c.Flags().StringVar(&f.shellName, "shell", "", "force a shell dialect, overriding the engine's tool-derived shell")
	c.Flags().StringVar(&f.engineName, "engine", "claude-code", "hook engine adapter")
	return c
}

func (f *evaluateFlags) run(*cobra.Command, []string) error {
	return runEvaluate(f.engineName, f.cfgPath, ir.Shell(f.shellName))
}

// runEvaluate is the process edge around evaluate: it feeds it stdin, writes
// the resulting streams, and exits non-zero when the engine's protocol demands
// it (a zero exit is a normal return, so deferred cleanup runs).
func runEvaluate(engineName, cfgPath string, forceShell ir.Shell) error {
	out, err := evaluate(engineName, cfgPath, forceShell, os.Stdin)
	if err != nil {
		return err
	}
	if err := emitDecision(out, os.Stdout, os.Stderr); err != nil {
		return err
	}
	if out.ExitCode != 0 {
		os.Exit(out.ExitCode)
	}
	return nil
}

// emitDecision writes a decision's two streams. The stdout write is checked
// and the stderr write is not, and that asymmetry is the contract, not an
// oversight:
//
//   - stdout carries the DECISION. If it cannot be delivered the host never
//     sees the deny, so there is nothing left to do but say so loudly.
//   - stderr carries only a DIAGNOSTIC, and the sole channel on which a failed
//     stderr write could be reported is stderr. Promoting it to an error would
//     turn a lost diagnostic into a non-zero exit — which both hook hosts read
//     as an allow, so a broken pipe on the diagnostic channel would disable
//     the guard for that call.
func emitDecision(out engine.Output, stdout, stderr io.Writer) error {
	if len(out.Stdout) > 0 {
		if _, err := stdout.Write(out.Stdout); err != nil {
			return fmt.Errorf("write decision: %w", err)
		}
	}
	if len(out.Stderr) > 0 {
		_, _ = stderr.Write(out.Stderr)
	}
	return nil
}

// evaluate runs the full decision path — decode the hook payload, parse the
// command, match the rules, encode the engine's wire output — with no process
// concerns, so it is testable end to end.
func evaluate(engineName, cfgPath string, forceShell ir.Shell, stdin io.Reader) (engine.Output, error) {
	adapter, err := engine.Get(engineName)
	if err != nil {
		return denyUnknownEngine(engineName, err, stdin)
	}
	// A typo'd --shell in the installed hook command would otherwise surface as
	// ErrUnsupportedShell on every parse, which the default on_parse_error: allow
	// turns into a permanent, silent allow-all. Fail closed instead.
	if forceShell != "" && !forceShell.Valid() {
		return failClosed(adapter, fmt.Sprintf(
			"%s is misconfigured: unknown --shell %q (known: %s); denying everything it guards until the hook command is fixed",
			progName, forceShell, knownShells()))
	}
	cfg, resolved, err := loadConfig(cfgPath)
	if err != nil {
		// A rules file was named (--config) or found by the search, but could not
		// be read or parsed. Erroring out here would fail OPEN — exit 1 disables
		// the guard on both hosts — so a broken config must deny instead. The
		// parse error rides in the reason so the agent relays it to the user.
		return failClosed(adapter, fmt.Sprintf(
			"%s could not load its rules config and is denying everything it guards until the config is fixed: %v",
			progName, err))
	}
	// Same discipline as the config-load branch above — deny until it is fixed
	// rather than guard nothing quietly.
	if err := expandSubmodules(cfg, os.Getwd); err != nil {
		return failClosed(adapter, fmt.Sprintf(
			"%s could not resolve the @submodules rule sentinel and is denying everything it guards until it is fixed: %v",
			progName, err))
	}
	// Reading or decoding the hook payload could otherwise return a plain error
	// → exit 1 → fail OPEN on both hosts (the same silent allow-all the --shell
	// and config branches above guard against). An adapter is in hand here, so a
	// malformed/version-skewed payload denies rather than sails through ungated.
	input, err := io.ReadAll(stdin)
	if err != nil {
		return failClosed(adapter, fmt.Sprintf(
			"%s could not read the hook payload and is denying this action: %v", progName, err))
	}
	req, err := adapter.Decode(input)
	if err != nil {
		return failClosed(adapter, fmt.Sprintf(
			"%s could not parse the hook payload and is denying this action: %v", progName, err))
	}
	// The installed PreToolUse matcher fired (this call is running at all) but
	// the tool name it fired on is not one Decode recognises — an unrecognized
	// tool with an unrecognized payload schema, the narrow exception to ltk's
	// fail-open posture. See ungatedToolDenyReason.
	if req.ToolUngated {
		return failClosed(adapter, ungatedToolDenyReason(adapter, req))
	}

	resp := applyConfirmOverride(newDecider(cfg, forceShell).Decide(context.Background(), req), req, resolved)

	out, err := adapter.Encode(resp)
	if err != nil {
		return engine.Output{}, fmt.Errorf("encode decision: %w", err)
	}
	return noteWhenNothingIsGated(out, resolved), nil
}

// denyUnknownEngine fails closed for an --engine name no adapter answers to.
//
// An unknown --engine in the installed hook (a manual edit or a cross-version
// rename) would otherwise exit 1, which the host treats as a silent allow, and
// there is no adapter for the NAMED engine to deny with. Guessing claude-code
// unconditionally was itself a bug: on a SECOND host (antigravity, before it
// was removed in 0.7.0, was the case that surfaced it) the deny
// then rode claude-code's wire format, which that host did not recognize, so
// the "fail closed" deny was invisible and the action proceeded anyway — the
// exact failure mode this branch exists to prevent. Try every registered
// engine's own Decode against the payload actually received; the first one
// that decodes something meaningful (a tool name, command, or file path) is
// presumably the real host, so fail closed in ITS wire format instead of
// guessing. Only one engine is registered today, but the loop stays general
// for whichever second host arrives next.
//
// lookupErr is returned unchanged in the one case with nothing to deny with.
func denyUnknownEngine(engineName string, lookupErr error, stdin io.Reader) (engine.Output, error) {
	input, _ := io.ReadAll(stdin) // best-effort: an unreadable body just skips detection below
	fallback := detectEngineFromPayload(input)
	if fallback == nil {
		var ferr error
		fallback, ferr = engine.Get("claude-code")
		if ferr != nil {
			return engine.Output{}, lookupErr // unreachable in practice; nothing to deny with
		}
	}
	return failClosed(fallback, fmt.Sprintf(
		"%s is misconfigured: unknown --engine %q; denying everything it guards until the hook command is fixed",
		progName, engineName))
}

// noteWhenNothingIsGated adds the "no rules config found" diagnostic when the
// search came up empty (resolvedConfig == "") and nothing else has written to
// stderr.
//
// loadConfig then fell back to the built-in zero-rule config, which allows
// everything with NO error. That is a silent, total fail-open by design — a
// project that never configured ltk is not gated — but "by design" and
// "invisible" should not be the same thing: an operator who believes the hook
// is active would otherwise have no way to notice it is gating nothing.
// Diagnostic only; the decision is unchanged.
func noteWhenNothingIsGated(out engine.Output, resolvedConfig string) engine.Output {
	if resolvedConfig != "" || len(out.Stderr) > 0 {
		return out
	}
	out.Stderr = []byte(fmt.Sprintf(
		"%s: no rules config found (searched cwd and ancestors for %s); nothing is being gated\n",
		progName, strings.Join(configSearch, ", ")))
	return out
}

// detectEngineFromPayload tries every registered engine's Decode against
// input and returns the first whose result looks like a genuine decode — a
// recognized tool name or a populated Command/FilePath — never a blind guess.
// nil means no engine's Decode produced anything meaningful for
// this payload, so the caller falls back to claude-code.
func detectEngineFromPayload(input []byte) engine.Engine {
	if len(input) == 0 {
		return nil
	}
	for _, e := range engine.All() {
		req, err := e.Decode(input)
		if err != nil {
			continue
		}
		if req.ToolName != "" || req.Command != "" || req.FilePath != "" {
			return e
		}
	}
	return nil
}

// ungatedToolDenyReason explains a deny for a payload whose tool name the
// adapter does not recognise. ltk's tool matcher is a hand-maintained list
// over a VENDOR-OWNED, mutating tool set (claudeGatedTools today; a future
// second host would add its own): when the vendor ships or renames a
// shell/file tool, the installed PreToolUse matcher can end up firing on it
// while Decode's exact-name list does not recognise it — confirmed live on
// antigravity before it was removed in 0.7.0 (its matcher was a real,
// unanchored regex: a tool like "safe_run_command" matched the
// "run_command" alternative). Claude Code evaluates a matcher
// built purely of plain identifiers and "|" (exactly what claudeMatcher is
// today) as an EXACT list instead — see claudecode.go's claudeMatcher comment
// — so this specific collision needs the matcher to contain a genuine regex
// metacharacter to reach Claude Code's regex path at all (a hand-edited
// settings.json, or a future gated name that isn't a plain identifier).
// Either way, once the installed matcher DOES fire on an unrecognised name,
// Decode reads only the payload field names it knows
// (tool_input.command/file_path/notebook_path); an unrecognized tool using
// different field names yields an empty Request that every rule silently
// misses.
//
// This is a deliberate, NARROW exception to ltk's fail-open posture (see
// docs/ltk/RULES.md and README.md's "Scope" section): everywhere else,
// uncertainty about a command or config chooses to let the agent through
// rather than block on it. Here it can't, because the alternative is not "the
// agent proceeds under a slightly weaker guard" — it is "a tool the operator
// explicitly told ltk to gate (it's in the installed matcher) silently
// evaluates as though no rule existed at all". A tool that never matches the
// installed matcher (Read, Grep, WebSearch, ...) never invokes evaluate in the
// first place, so this exception has no effect on those — see
// TestEvaluateDeniesUnrecognizedToolName's "recognised tool stays silent" case
// and the claude-code Decode tests for the matcher-miss path.
func ungatedToolDenyReason(adapter engine.Adapter, req engine.Request) string {
	return fmt.Sprintf(
		"%s could not read %s tool %q's payload — it matched the installed hook but is not in "+
			"ltk's gated tool set, so no rule could be evaluated against it — and is denying this "+
			"call rather than letting it through unchecked. To fix: add %q to the engine's gated "+
			"tools and re-run '%s manage install'.",
		progName, adapter.Name(), req.ToolName, req.ToolName, progName)
}

// failClosed renders reason as a well-formed deny decision in the engine's wire
// format, exit 0. It exists because a hook host fails OPEN when a hook exits
// non-zero — Claude Code treats exit 1 as non-blocking (antigravity, before
// its removal in 0.7.0, proceeded on any crashing hook the same way) — so a
// broken ltk installation must
// never surface as an error exit on the hook path: that would silently disable
// every rule. Explicit user-facing commands (manage, --print) keep their
// fail-loud exit-1 behavior; only `evaluate` fails closed.
//
// That guarantee is not absolute in the type system: this function, and
// evaluate itself, still have `engine.Output{}, err` returns. They rest on two
// properties, both pinned by
// TestEvaluate_HookPathNeverReturnsAPlainError rather than assumed:
//
//   - every registered adapter's Encode is TOTAL. The deny bytes are
//     json.Marshal over a struct of plain string fields, which has no failure
//     mode (invalid UTF-8 is replaced, not rejected), so the encode-error
//     returns are unreachable rather than merely unlikely.
//   - engine.Get("claude-code") always resolves, so the last-resort fallback
//     for an unknown --engine always has something to deny with.
//
// The moment an adapter grows a fallible encoder, the hook path stops being
// fail-closed and this comment stops being true — which is exactly when that
// test goes red.
func failClosed(adapter engine.Adapter, reason string) (engine.Output, error) {
	out, err := adapter.Encode(engine.Response{Allow: false, Reason: reason})
	if err != nil {
		return engine.Output{}, fmt.Errorf("encode fail-closed decision: %w", err)
	}
	return out, nil
}
