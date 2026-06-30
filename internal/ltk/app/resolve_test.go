package app

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

func newAppWithDefault(t *testing.T, defaultShell ir.Shell) *App {
	t.Helper()
	y := "version: 1\nrules: []\n"
	if defaultShell != "" {
		y = "version: 1\ndefaults: { shell: " + string(defaultShell) + " }\nrules: []\n"
	}
	cfg, err := rules.Parse([]byte(y))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return New(cfg)
}

func TestResolveShellPrecedence(t *testing.T) {
	t.Run("force beats everything", func(t *testing.T) {
		a := newAppWithDefault(t, ir.ShellZsh)
		a.ForceShell = ir.ShellPwsh
		if got := a.resolveShell(ir.ShellBash); got != ir.ShellPwsh {
			t.Errorf("got %q, want pwsh", got)
		}
	})
	t.Run("adapter hint beats config default", func(t *testing.T) {
		a := newAppWithDefault(t, ir.ShellZsh)
		if got := a.resolveShell(ir.ShellPwsh); got != ir.ShellPwsh {
			t.Errorf("got %q, want pwsh", got)
		}
	})
	t.Run("config default beats host shell", func(t *testing.T) {
		a := newAppWithDefault(t, ir.ShellZsh)
		a.HostShell = ir.ShellMksh
		if got := a.resolveShell(""); got != ir.ShellZsh {
			t.Errorf("got %q, want zsh", got)
		}
	})
	t.Run("host shell when no hint or config", func(t *testing.T) {
		a := newAppWithDefault(t, "")
		a.HostShell = ir.ShellZsh
		if got := a.resolveShell(""); got != ir.ShellZsh {
			t.Errorf("got %q, want zsh (from $SHELL)", got)
		}
	})
	t.Run("falls back to bash", func(t *testing.T) {
		a := newAppWithDefault(t, "")
		if got := a.resolveShell(""); got != ir.ShellBash {
			t.Errorf("got %q, want bash", got)
		}
	})
}
