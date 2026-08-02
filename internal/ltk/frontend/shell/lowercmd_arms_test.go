package shell_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// TestLowerCmdArms covers every branch of lowerCmd's type switch — the bare
// redirect (a nil Cmd), a call, each binary operator, a brace block, a
// subshell, and the compound-command default — asserting the programs each
// lowers to.
//
// lowerCmd was once measured at CCN 12, over the project gate. On current
// release/0.7 it measures 10 under `lizard -C 10`, which fails on EXCEEDING
// 10, so it no longer trips. The drop came from an unrelated change that
// deleted Pipeline.Background and Pipeline.Negated, and with them the two
// `st != nil && st.X` conjunctions in the CallExpr arm. This table is what a
// future reduction would need, since a pure complexity change cannot be shown
// by a red test.
func TestLowerCmdArms(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
	}{
		{"bare redirect (nil Cmd)", "> out.txt", []string{""}},
		{"call", "git push", []string{"git"}},
		{"pipe", "git log | grep x", []string{"git", "grep"}},
		{"and", "a && b", []string{"a", "b"}},
		{"or", "a || b", []string{"a", "b"}},
		{"sequence", "a ; b", []string{"a", "b"}},
		{"brace block", "{ a; b; }", []string{"a", "b"}},
		{"subshell", "( a; b )", []string{"a", "b"}},
		{"compound if", "if a; then b; else c; fi", []string{"a", "b", "c"}},
		{"compound for", "for i in 1 2; do b; done", []string{"b"}},
		{"compound while", "while a; do b; done", []string{"a", "b"}},
		{"compound case", "case $x in y) a;; esac", []string{"a"}},
		{"function body", "f() { a; }", []string{"a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := shell.New().Parse(context.Background(), ir.ShellBash, tc.src)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.src, err)
			}
			var got []string
			for _, c := range s.Commands() {
				got = append(got, c.Program())
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) programs = %v, want %v", tc.src, got, tc.want)
			}
		})
	}
}

// TestBareRedirectKeepsItsTarget is the one arm whose value is not a program
// name: a statement with no command word still has to reach the IR carrying
// its redirect, rather than being dropped.
func TestBareRedirectKeepsItsTarget(t *testing.T) {
	s, err := shell.New().Parse(context.Background(), ir.ShellBash, "> out.txt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmds := s.Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands = %d, want 1", len(cmds))
	}
	if got, want := cmds[0].Redirects, []ir.Redirect{{Op: ">", Target: "out.txt"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("redirects = %+v, want %+v", got, want)
	}
}
