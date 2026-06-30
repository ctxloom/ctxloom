// Package app is the composition root: it wires the frontends into a registry
// and turns an engine.Request into an engine.Response by parsing, then
// evaluating against the rules.
package app

import (
	"context"
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
}

// New builds an App with all frontends registered.
func New(cfg *rules.Config) *App {
	reg := frontend.NewRegistry()
	reg.Register(shell.New()) // sh, bash, zsh, mksh
	reg.Register(pwsh.New())  // pwsh (defers to PowerShell's own parser)
	reg.Register(cmd.New())   // cmd.exe
	return &App{Config: cfg, Registry: reg, DefaultShell: ir.ShellBash}
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
func (a *App) Decide(ctx context.Context, req engine.Request) engine.Response {
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

	// Understand trivial wrappers (bash -c "…", eval "…", …) by re-parsing their
	// inner command, so a denied command can't be smuggled past a rule.
	a.Registry.ExpandWrappers(ctx, script)

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
