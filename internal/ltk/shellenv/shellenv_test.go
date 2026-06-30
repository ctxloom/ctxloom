package shellenv

import (
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
		"/usr/bin/ksh":            ir.ShellMksh,
		"/usr/bin/pwsh":           ir.ShellPwsh,
		"cmd.exe":                 ir.ShellCmd, // .exe stripped
		"/usr/local/bin/pwsh.exe": ir.ShellPwsh,
		"/usr/bin/fish":           "", // unsupported → defer
		"":                        "",
		"/opt/homebrew/bin/zsh":   ir.ShellZsh,
	}
	for in, want := range cases {
		if got := ShellFromPath(in); got != want {
			t.Errorf("ShellFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromEnvEmpty(t *testing.T) {
	if got := FromEnv(""); got != "" {
		t.Errorf("FromEnv(\"\") = %q, want \"\"", got)
	}
}
