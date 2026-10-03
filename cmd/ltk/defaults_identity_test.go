package main

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/rules"
)

// The shipped identity guard blocks WRITES to user.name/user.email and lets
// reads through. It used to block the bare read `git config user.name` as
// well, which differs from the write only by its trailing value.
func TestDefaultRules_GitIdentityBlocksWritesNotReads(t *testing.T) {
	cfg, err := rules.Parse([]byte(defaultRules))
	if err != nil {
		t.Fatalf("default rules do not parse: %v", err)
	}
	for _, tc := range []struct {
		line    string
		allowed bool
	}{
		{"git config user.name", true},
		{"git config --get user.email", true},
		{"git config user.name Bob", false},
		{"git config --local user.email b@x", false},
		{"git config --unset user.name", false},
		{"git config --unset-all user.email", false},
	} {
		argv := strings.Fields(tc.line)
		d := rules.Evaluate(cfg, &ir.Script{
			Shell:     ir.ShellBash,
			Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: argv}}}},
		})
		if d.Allowed != tc.allowed {
			t.Errorf("%q: allowed = %v, want %v (rule %q)", tc.line, d.Allowed, tc.allowed, d.RuleID)
		}
	}
}
