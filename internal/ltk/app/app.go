// Package app is the composition root: it wires the frontends into a registry
// and turns an engine.Request into an engine.Response by parsing, then
// evaluating against the rules.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/frontend"
	"github.com/ctxloom/ctxloom/internal/ltk/frontend/cmd"
	"github.com/ctxloom/ctxloom/internal/ltk/frontend/pwsh"
	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// App holds the configured rules and the frontend registry.
type App struct {
	Config   *rules.Config
	Registry *frontend.Registry
	// forceShell and hostShell come from Shells at construction; see that type
	// for what each means and why neither is assignable afterwards.
	forceShell ir.Shell
	hostShell  ir.Shell
	// DefaultShell is the final fallback when nothing else resolves a shell.
	DefaultShell ir.Shell
	// Warn receives one-line diagnostics about ltk's own failures — today, a
	// recovered panic on the analysis path. New sets it to os.Stderr, which is
	// where evaluate already writes its other operator-facing notes; a nil Warn
	// discards them without changing any decision.
	//
	// This writes to the process's stderr stream, NOT into engine.Output.Stderr
	// (the field an adapter's Encode could plumb into the host's own protocol).
	// Verified against Claude Code's hook contract (code.claude.com/docs/en/hooks,
	// 2026-07-24): on exit 0 — which is every outcome cmd/ltk/evaluate.go produces,
	// deny included, since a deny rides stdout JSON, not a nonzero exit — a
	// PreToolUse hook's stderr is written only to Claude Code's debug log; it
	// reaches neither the model nor the user's terminal without --debug. So this
	// warning is effectively invisible in normal operation today. Making it
	// visible would mean adding a Warnings-shaped field to engine.Response and
	// teaching every Adapter.Encode to copy it into Output.Stderr (or, for
	// Claude Code specifically, riding a different channel — see
	// SessionStartOutput.SystemMessage in hooks_wire.go for a precedent of a
	// user-visible-but-not-model-visible channel on another hook event) — a
	// public-interface change, and an escalation, not something to do unasked.
	Warn io.Writer
}

// Shells carries the dialect signals only the caller can know, and is a
// REQUIRED argument to New rather than a pair of fields assigned afterwards.
// Both feed resolveShell's precedence chain, and omitting Host is silent: the
// command is still analyzed, just as the wrong dialect, so a rule written
// against a construct the substituted dialect cannot parse simply stops
// firing. A caller that forgets must therefore fail to compile, not to guard.
type Shells struct {
	// Force overrides every other signal (the --shell flag). Empty — meaning
	// "no override" — is the ordinary case.
	Force ir.Shell
	// Host is the user's login shell, from $SHELL. It is the right dialect for
	// engines that run commands in that shell (notably Claude Code's Bash
	// tool); an engine that forces a fixed shell instead emits a
	// strong hint that bypasses it. Empty means "unknown", which falls through
	// to the default rather than guessing.
	Host ir.Shell
}

// New builds an App with all frontends registered.
func New(cfg *rules.Config, shells Shells) *App {
	reg := frontend.NewRegistry()
	reg.Register(shell.New()) // sh, bash, zsh, mksh
	reg.Register(pwsh.New())  // pwsh (defers to PowerShell's own parser)
	reg.Register(cmd.New())   // cmd.exe
	return &App{
		Config:       cfg,
		Registry:     reg,
		forceShell:   shells.Force,
		hostShell:    shells.Host,
		DefaultShell: ir.ShellBash,
		Warn:         os.Stderr,
	}
}

// resolveShell picks the shell to parse with, in precedence order:
//  1. Shells.Force — operator override (--shell)
//  2. hint — the adapter's per-call, tool-derived shell (authoritative; e.g.
//     Claude's PowerShell tool → pwsh, or a future Codex adapter → bash)
//  3. defaults.shell — explicit operator config
//  4. Shells.Host — the user's $SHELL (Claude's Bash tool runs in the login shell)
//  5. DefaultShell — final fallback (bash)
//
// A content-sniff step belongs between (4) and (5) but is deliberately omitted:
// the tool the LLM chose plus the host shell already name the dialect, so
// sniffing command text (where $(...) and ${...} are ambiguous between bash and
// pwsh) is not trusted.
func (a *App) resolveShell(hint ir.Shell) ir.Shell {
	switch {
	case a.forceShell != "":
		return a.forceShell
	case hint != "":
		return hint
	case a.config().Defaults.Shell != "":
		return a.config().Defaults.Shell
	case a.hostShell != "":
		return a.hostShell
	default:
		return a.DefaultShell
	}
}

// Decide evaluates a request against the rules: a file edit (FilePath set) is
// matched against path rules; otherwise the command is parsed and matched
// against command rules.
//
// Decide is the recover boundary for ltk's whole analysis path — parsing,
// wrapper expansion, and rule matching all happen below it. It is the right
// frame for the boundary because it is the innermost one that OWNS a decision:
// recovering any deeper would return a meaningless zero value upward (an empty
// engine.Response is Allow=false, i.e. a silent deny), and recovering any
// shallower — in cmd/ltk's evaluate — would sit on top of the deliberate
// fail-CLOSED paths there (unknown --engine, unreadable config, undecodable
// payload, ungated tool) and risk flipping their intent.
//
// The boundary cannot swallow a legitimate denial: every deny in ltk is a
// RETURNED engine.Response, never a panic, and the deferred handler leaves the
// named result untouched unless recover actually caught something. A panic is
// only ever ltk failing to analyze the command at all — never ltk deciding.
func (a *App) Decide(ctx context.Context, req engine.Request) (resp engine.Response) {
	defer func() {
		if r := recover(); r != nil {
			resp = a.onAnalysisPanic(req, r, debug.Stack())
		}
	}()
	return a.decide(ctx, req)
}

// onAnalysisPanic converts a recovered panic into a decision and tells the
// human. ltk is a cooperative redirect, not a sandbox, so an ltk bug must not
// block a command the operator never wrote a rule against: it takes the same
// route a parse error takes, which under the default (and shipped)
// on_parse_error: allow means FAIL OPEN. An operator who explicitly set
// on_parse_error: deny asked for "block whatever ltk cannot analyze", and a
// crash is the most complete form of that; honouring it keeps one policy knob
// instead of two.
//
// Either way the warning is unconditional. A silent recover would hide the bug
// for as long as the guard keeps limping, which is precisely how this class of
// defect stays invisible while the suite reports green.
func (a *App) onAnalysisPanic(req engine.Request, val any, stack []byte) engine.Response {
	subject := req.Command
	if req.FilePath != "" {
		subject = "file " + req.FilePath
	}
	deny := a.denyOnUnanalyzable()
	if a.Warn != nil {
		verb := "ALLOWED"
		if deny {
			verb = "DENIED"
		}
		fmt.Fprintf(a.Warn,
			"ltk: internal error while analyzing %q — recovered and %s this action. "+
				"This is an ltk bug, not a problem with the command; please report it.\npanic: %v\n%s\n",
			subject, verb, val, stack)
	}
	parseErr := fmt.Sprintf("internal error: %v", val)
	if deny {
		return engine.Response{Allow: false, Unanalyzed: true, ParseError: parseErr, Reason: fmt.Sprintf(
			"could not analyze command (internal error: %v); defaults.on_parse_error is deny", val)}
	}
	return engine.Response{Allow: true, Unanalyzed: true, ParseError: parseErr}
}

func (a *App) denyOnUnanalyzable() bool {
	return a.config().Defaults.OnParseError == rules.ActionDeny
}

// config returns the rules to decide against, and is never nil. A nil Config
// means allow-all — the position rules.Evaluate and rules.EvaluatePath already
// take explicitly, and the only one that keeps a missing config from becoming
// a denial nobody wrote. Every reader in this type goes through here: reading
// the field directly is what made the discipline inconsistent, so that a nil
// Config surfaced as a nil dereference in one reader, a guarded false in
// another, and — through the recover boundary — an "this is an ltk bug"
// warning to the operator.
func (a *App) config() *rules.Config {
	if a.Config == nil {
		return emptyRules
	}
	return a.Config
}

// emptyRules is the stand-in for a nil Config: no rules, normalized defaults.
// It is shared because it is never mutated — nothing in this package writes
// through a *rules.Config, and callers that need to expand or amend rules do
// it on their own config before handing it to New.
var emptyRules = rules.Empty()

func (a *App) decide(ctx context.Context, req engine.Request) engine.Response {
	if req.FilePath != "" {
		return responseFromDecision(rules.EvaluatePath(a.config(), req.FilePath))
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return engine.Response{Allow: true}
	}
	shell := a.resolveShell(req.Shell)

	script, err := a.Registry.Parse(ctx, shell, command)
	if err != nil {
		// Unsupported shell or parse error. Apply the on_parse_error policy —
		// pass-through by default. Either way this decision is UNANALYZED: an
		// allow here is not "the rules were checked and nothing matched", it is
		// "nothing could be checked at all" — carried on the
		// Response so a caller (the CLI, an adapter's Encode) can say so instead
		// of looking byte-identical to a clean allow.
		if a.denyOnUnanalyzable() {
			return engine.Response{Allow: false, Unanalyzed: true, ParseError: err.Error(),
				Reason: "could not analyze command (" + err.Error() + ")"}
		}
		return engine.Response{Allow: true, Unanalyzed: true, ParseError: err.Error()}
	}

	// Understand trivial wrappers (bash -c "…", eval "…", env …, timeout N …,
	// …) by unwrapping their inner command, so a denied command can't be
	// smuggled past a rule. A truncated expansion means a WRAPPER was left
	// unexpanded — its inner command string was never parsed, so no rule saw
	// the command that actually runs — and is failed CLOSED (deny). Depth on
	// its own is not that signal: a command substitution the frontend already
	// parsed is visible to rules.Evaluate at any depth, so nesting alone must
	// not produce this denial.
	// A nested command that failed to PARSE (as opposed to depth) is a
	// separate signal: previously this was silently dropped from the IR with
	// no trace at all — a wrapped command ltk could not verify was treated
	// identically to "nothing to see here" instead of going through the
	// SAME on_parse_error policy the top-level parse failure above already
	// respects.
	truncated, unanalyzed := a.Registry.ExpandWrappers(ctx, script)
	if truncated {
		return engine.Response{Allow: false, Unanalyzed: true,
			Reason: "nested command-wrapper depth exceeded (possible evasion)"}
	}
	if unanalyzed {
		const msg = "a nested command (inside a wrapper such as bash -c/eval/cmd /c/pwsh -Command) could not be parsed"
		if a.denyOnUnanalyzable() {
			return engine.Response{Allow: false, Unanalyzed: true, ParseError: msg,
				Reason: "could not analyze command (" + msg + ")"}
		}
		// Fall through to Evaluate: whatever the frontend salvaged from the
		// nested command is still matched below (fail-safe direction), but the
		// Response still reports Unanalyzed so the caller knows the view of the
		// nested command was incomplete even though this rule pass allowed it.
		d := rules.Evaluate(a.config(), script)
		if !d.Allowed {
			return responseFromDecision(d)
		}
		return engine.Response{Allow: true, Unanalyzed: true, ParseError: msg}
	}

	return responseFromDecision(rules.Evaluate(a.config(), script))
}

// responseFromDecision is the ONE Decision -> Response conversion. decide
// reaches it from three separate return paths, and the mapping used to be
// written out field for field at each of them: nothing but review stopped a
// seventh Decision field being wired into one path and silently dropped by the
// other two, and a dropped Suggest or ConfirmWindowSeconds is invisible — the
// verdict is still right, the operator just loses the way out of it.
//
// Unanalyzed and ParseError are deliberately NOT set here. They do not come
// from a Decision at all: they record that the command could not be fully
// PARSED, which is settled before any rule is consulted, so the caller sets
// them on the response this returns.
func responseFromDecision(d rules.Decision) engine.Response {
	return engine.Response{
		Allow:                d.Allowed,
		Reason:               d.Reason,
		Suggest:              d.Suggest,
		Confirmable:          d.Confirmable,
		ConfirmWindowSeconds: d.ConfirmWindowSeconds,
		ConfirmDelaySeconds:  d.ConfirmDelaySeconds,
	}
}
