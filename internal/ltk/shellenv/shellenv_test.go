package shellenv

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

func TestShellFromPath(t *testing.T) {
	cases := map[string]ir.Shell{
		"/bin/bash":               ir.ShellBash,
		"/usr/bin/zsh":            ir.ShellZsh,
		"/bin/sh":                 ir.ShellSh,
		"/usr/bin/dash":           ir.ShellSh,
		"/usr/bin/mksh":           ir.ShellMksh,
		// AT&T ksh93, not mksh: the MirBSDKorn variant rejects ksh93's process
		// substitution and C-style for, and a rejected command is one no rule
		// sees (app.TestKsh93ConstructsAreStillAnalyzed measures it).
		"/usr/bin/ksh":            ir.ShellBash,
		"/usr/bin/ksh93":          ir.ShellBash,
		"/usr/bin/oksh":           ir.ShellMksh,
		"/usr/bin/pwsh":           ir.ShellPwsh,
		"cmd.exe":                 ir.ShellCmd, // .exe stripped
		"/usr/local/bin/pwsh.exe": ir.ShellPwsh,
		"/usr/bin/fish":           "", // unsupported → defer
		"":                        "", // an unset $SHELL defers, same as an unsupported one
		"/opt/homebrew/bin/zsh":   ir.ShellZsh,
	}
	for in, want := range cases {
		if got := ShellFromPath(in); got != want {
			t.Errorf("ShellFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// A shell path written with backslashes must resolve on every host. ltk parses
// cmd.exe and PowerShell, so `C:\Windows\System32\cmd.exe` is ordinary input
// here — it arrives from $SHELL/%COMSPEC% and from wrapper argv the frontend
// hands over — and filepath.Base is OS-dependent: on a POSIX build it does not
// split on `\`, so the whole path became the "name", matched nothing, and the
// caller was told "not a shell I parse". That is the fail-open direction: the
// dialect is silently guessed instead, and a wrapper's inner command is never
// re-parsed in the shell it is actually written in.
func TestShellFromPathSplitsWindowsSeparators(t *testing.T) {
	// The fixture must be hostile from the code-under-test's vantage point: on
	// a POSIX build filepath.Base leaves a backslash path completely intact,
	// which is the whole defect. On Windows there is nothing to demonstrate.
	const win = `C:\Windows\System32\cmd.exe`
	if filepath.Base(win) != win {
		t.Skip("host filepath already splits on backslash; the defect is unreachable here")
	}

	cases := map[string]ir.Shell{
		win: ir.ShellCmd,
		`C:\Program Files\PowerShell\7\pwsh.exe`: ir.ShellPwsh,
		`D:\tools\git\usr\bin\bash.exe`:          ir.ShellBash,
		`C:\Users\a\scoop\shims\zsh`:             ir.ShellZsh,
		`C:\tools\fish.exe`:                      "", // still unrecognized, just for the right reason
	}
	for in, want := range cases {
		if got := ShellFromPath(in); got != want {
			t.Errorf("ShellFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
