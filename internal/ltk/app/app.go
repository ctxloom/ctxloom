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
	// ForceShell, when set (from a --shell flag), overrides every other signal.
	ForceShell ir.Shell
	// HostShell is the user's default login shell (from $SHELL). It is the right
	// dialect for engines that run commands in the user's shell — notably Claude
	// Code's Bash tool. main populates it; engines that force a fixed shell
	// (Codex/Antigravity) emit a strong hint that bypasses it.
	HostShell ir.Shell
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

// New builds an App with all frontends registered.
func New(cfg *rules.Config) *App {
	reg := frontend.NewRegistry()
	reg.Register(shell.New()) // sh, bash, zsh, mksh
	reg.Register(pwsh.New())  // pwsh (defers to PowerShell's own parser)
	reg.Register(cmd.New())   // cmd.exe
	return &App{Config: cfg, Registry: reg, DefaultShell: ir.ShellBash, Warn: os.Stderr}
}

// resolveShell picks the shell to parse with, in precedence order:
//  1. ForceShell — operator override (--shell)
//  2. hint — the adapter's per-call, tool-derived shell (authoritative; e.g.
//     Claude's PowerShell tool → pwsh, or a future Codex adapter → bash)
//  3. defaults.shell — explicit operator config
//  4. HostShell — the user's $SHELL (Claude's Bash tool runs in the login shell)
//  5. DefaultShell — final fallback (bash)
//
// A content-sniff step belongs between (4) and (5) but is deliberately omitted:
// the tool the LLM chose plus the host shell already name the dialect, so
// sniffing command text (where $(...) and ${...} are ambiguous between bash and
// pwsh) is not trusted.
func (a *App) resolveShell(hint ir.Shell) ir.Shell {
	switch {
	case a.ForceShell != "":
		return a.ForceShell
	case hint != "":
		return hint
	case a.Config.Defaults.Shell != "":
		return a.Config.Defaults.Shell
	case a.HostShell != "":
		return a.HostShell
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
	if deny {
		return engine.Response{Allow: false, Reason: fmt.Sprintf(
			"could not analyze command (internal error: %v); defaults.on_parse_error is deny", val)}
	}
	return engine.Response{Allow: true}
}

func (a *App) denyOnUnanalyzable() bool {
	return a.Config != nil && a.Config.Defaults.OnParseError == rules.ActionDeny
}

func (a *App) decide(ctx context.Context, req engine.Request) engine.Response {
	if req.FilePath != "" {
		d := rules.EvaluatePath(a.Config, req.FilePath)
		return engine.Response{
			Allow:                d.Allowed,
			Reason:               d.Reason,
			Suggest:              d.Suggest,
			Confirmable:          d.Confirmable,
			ConfirmWindowSeconds: d.ConfirmWindowSeconds,
			ConfirmDelaySeconds:  d.ConfirmDelaySeconds,
		}
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return engine.Response{Allow: true}
	}
	shell := a.resolveShell(req.Shell)

	script, err := a.Registry.Parse(ctx, shell, command)
	if err != nil {
		// Unsupported shell or parse error. Apply the on_parse_error policy —
		// pass-through by default.
		if a.Config.Defaults.OnParseError == rules.ActionDeny {
			return engine.Response{Allow: false, Reason: "could not analyze command (" + err.Error() + ")"}
		}
		return engine.Response{Allow: true}
	}

	// Understand trivial wrappers (bash -c "…", eval "…", env …, timeout N …,
	// …) by unwrapping their inner command, so a denied command can't be
	// smuggled past a rule. A truncated expansion means the nesting ran deeper
	// than ltk verified — fail CLOSED (deny) rather than evaluate rules
	// against a possibly-incomplete view of what the command actually runs.
	if truncated := a.Registry.ExpandWrappers(ctx, script); truncated {
		return engine.Response{Allow: false, Reason: "nested command-wrapper depth exceeded (possible evasion)"}
	}

	d := rules.Evaluate(a.Config, script)
	return engine.Response{
		Allow:                d.Allowed,
		Reason:               d.Reason,
		Suggest:              d.Suggest,
		Confirmable:          d.Confirmable,
		ConfirmWindowSeconds: d.ConfirmWindowSeconds,
		ConfirmDelaySeconds:  d.ConfirmDelaySeconds,
	}
}
