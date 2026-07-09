package rules

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// `unless` lets a rule carve out read-only exceptions: the rule matches only
// when NONE of the listed tokens are present. e.g. block `git tag` but not the
// read-only `git tag --list`.
func TestUnlessExceptions(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:      "no-git-tag",
		Match:   Match{Command: CommandPattern{"git", "tag"}, Unless: []string{"--list", "-l", "-n"}},
		Message: "releases go through the pipeline",
	}}}

	deny := [][]string{
		{"git", "tag", "v1.2.3"}, // creating a tag → denied
		{"git", "tag"},           // bare → denied
	}
	for _, argv := range deny {
		if Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("%v should be denied", argv)
		}
	}

	allow := [][]string{
		{"git", "tag", "--list"},       // read-only listing → exception
		{"git", "tag", "-l", "v1.*"},   // short form
		{"git", "tag", "-n", "--list"}, // any excepted token present
	}
	for _, argv := range allow {
		if !Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("%v should be allowed (unless exception)", argv)
		}
	}
}

// `unless` is checked against args with bundled short flags expanded.
func TestUnlessWithBundledFlags(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:      "rm-recursive",
		Match:   Match{Command: CommandPattern{"rm", "-r"}, Unless: []string{"-i"}},
		Message: "no recursive delete",
	}}}
	// `rm -ri` bundles -r and -i; the -i exception must fire even though it's
	// clustered with -r.
	if !Evaluate(cfg, cmd(ir.ShellBash, "rm", "-ri", "dir")).Allowed {
		t.Error("rm -ri should be allowed: -i is an `unless` exception (bundled)")
	}
	if Evaluate(cfg, cmd(ir.ShellBash, "rm", "-rf", "dir")).Allowed {
		t.Error("rm -rf should be denied: no exception token present")
	}
}

func TestUnlessParsesAndCountsAsConstraint(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nrules:\n  - id: x\n    match: { unless: [--help] }\n    message: m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Rules[0].Match.hasConstraint() {
		t.Error("`unless` alone should be a valid match constraint")
	}
}
