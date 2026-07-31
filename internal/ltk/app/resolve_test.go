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
	return New(cfg, Shells{})
}

func TestResolveShellPrecedence(t *testing.T) {
	t.Run("force beats everything", func(t *testing.T) {
		a := newAppWithDefault(t, ir.ShellZsh)
		a.forceShell = ir.ShellPwsh
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
		a.hostShell = ir.ShellMksh
		if got := a.resolveShell(""); got != ir.ShellZsh {
			t.Errorf("got %q, want zsh", got)
		}
	})
	t.Run("host shell when no hint or config", func(t *testing.T) {
		a := newAppWithDefault(t, "")
		a.hostShell = ir.ShellZsh
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

// The dialect signals are CONSTRUCTION inputs, not fields a caller has to
// remember to assign before the first Decide. Omitting the host shell is
// silent — the command is still analyzed, just as the wrong dialect, so a rule
// written against a construct that dialect cannot parse simply stops firing —
// and a silent wrong answer is the one thing a compile error is good at
// preventing. If Shells ever stops being a required argument to New, this test
// is the one that stops compiling.
func TestNew_ShellsAreSuppliedAtConstruction(t *testing.T) {
	cfg, err := rules.Parse([]byte("version: 1\nrules: []\n"))
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	if got := New(cfg, Shells{Host: ir.ShellZsh}).resolveShell(""); got != ir.ShellZsh {
		t.Errorf("host shell from New = %q, want %q", got, ir.ShellZsh)
	}
	if got := New(cfg, Shells{Force: ir.ShellPwsh, Host: ir.ShellZsh}).resolveShell(ir.ShellMksh); got != ir.ShellPwsh {
		t.Errorf("force shell from New = %q, want %q — it must beat both the hint and the host shell", got, ir.ShellPwsh)
	}
	// An empty Shells says "no override, host unknown" explicitly, and falls
	// through to the default rather than guessing.
	if got := New(cfg, Shells{}).resolveShell(""); got != ir.ShellBash {
		t.Errorf("empty Shells = %q, want the bash default", got)
	}
}
