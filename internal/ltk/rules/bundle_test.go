package rules

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// A rule written with separate short flags must match a bundled invocation
// (getopt clustering) regardless of order, under POSIX shells.
func TestBundledShortFlagsMatchSeparate(t *testing.T) {
	m := Match{Command: CommandPattern{"rm", "-r", "-f"}}
	cases := map[string]bool{
		"rm -rf x":   true,
		"rm -fr x":   true,
		"rm -r -f x": true,
		"rm -r x":    false, // only -r, missing -f
		"rm x":       false,
	}
	for cmd, want := range cases {
		sc := ir.SimpleCommand{Argv: strings.Fields(cmd)}
		if got := m.matches(ir.ShellBash, sc); got != want {
			t.Errorf("%q: matches=%v want %v", cmd, got, want)
		}
	}
}

// Bundling is a POSIX/getopt convention. PowerShell uses -LongName, so a
// cluster-looking token like -Recurse must NOT be split there; under bash it is
// (the documented tradeoff: the shell can't know a program's option grammar).
func TestBundlingIsPosixOnly(t *testing.T) {
	m := Match{Command: CommandPattern{"x"}, ArgsAny: []string{"-R"}}
	sc := ir.SimpleCommand{Argv: []string{"x", "-Recurse"}}
	if m.matches(ir.ShellPwsh, sc) {
		t.Error("pwsh: -Recurse must not expand into -R")
	}
	if !m.matches(ir.ShellBash, sc) {
		t.Error("bash: -Recurse is treated as a short cluster containing -R")
	}
}
