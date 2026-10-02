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

// TestUnlessIsPositionBlindToOptionArguments PINS a known, accepted limitation
// (not a regression to fix here): `unless` checks "is this token present
// anywhere in argv", with no notion of which option a token belongs to. It
// cannot tell a standalone exception flag from the same text sitting there as
// ANOTHER option's argument VALUE.
//
// `git clean -fdx -e --dry-run` — real git's `-e` takes an exclude PATTERN
// argument, so `-e --dry-run` means "exclude files named --dry-run"; this is
// NOT a dry run, it deletes for real. But `unless: ["-n", "--dry-run"]` sees
// `--dry-run` present in argv and exempts it anyway. This is the exact
// shipped shape of the `no-git-clean` default rule (see DEFAULTS.md) — the
// bug is real against ltk's own defaults, not a contrived example.
//
// Why this is pinned rather than fixed: a correct fix needs per-program
// argument-arity knowledge (which flags consume a following word, and how
// many) that ltk deliberately does not carry — see "Matching commands" in
// https://ctxloom.dev/ltk/rules/ for why the matcher stays program-agnostic. The documented
// mitigation is authoring guidance, not an engine change: prefer `mode:
// confirm` over `unless` for destructive rules (see the rules reference's note that `unless`
// is matched position-blind), since confirm requires a deliberate
// repeat rather than trusting an exception token found anywhere in argv.
func TestUnlessIsPositionBlindToOptionArguments(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID: "no-git-clean",
		Match: Match{
			Command: CommandPattern{"git", "clean"},
			Unless:  []string{"-n", "--dry-run"},
		},
		Message: "git clean deletes untracked files for good",
	}}}

	// KNOWN-WRONG: -e's argument value ("--dry-run") satisfies the unless
	// exception, even though this invocation deletes for real. Documented,
	// accepted limitation — see the comment above.
	got := Evaluate(cfg, cmd(ir.ShellBash, "git", "clean", "-fdx", "-e", "--dry-run"))
	if !got.Allowed {
		t.Fatal("this test pins the KNOWN-WRONG behavior (see comment); " +
			"if this now fails, either the matcher grew arg-arity awareness " +
			"(great — update this test and the rules reference to describe the fix) or " +
			"something else changed unless-matching in a way that needs review")
	}

	// Control: the same destructive invocation with no exception token present
	// is correctly denied — confirms the rule itself, and expandShortClusters,
	// still work as intended; only the smuggled-argument case is blind.
	denied := Evaluate(cfg, cmd(ir.ShellBash, "git", "clean", "-fdx"))
	if denied.Allowed {
		t.Fatal("git clean -fdx with no unless token present must still be denied")
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

// `unless_arg_contains` carves out an exception by SHAPE rather than by token.
//
// The case: `go install <module>@<version>` installs a THIRD-PARTY tool, which
// by Go's own rules is a build of something other than this module, while a
// bare `go install` builds this one. A rule redirecting the latter to the task
// runner must not catch the former — `just install` installs ctxloom, so the
// refusal would name a remedy that installs the wrong program.
//
// Both directions are asserted, and that pairing is the point: an exemption
// tested alone is satisfied just as well by a rule that stopped matching
// anything at all.
func TestUnlessArgContainsExemptsAShape(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:      "install-via-just",
		Match:   Match{Command: CommandPattern{"go", "install"}, UnlessArgContains: []string{"@"}},
		Message: "Install through the task runner",
	}}}

	allow := [][]string{
		{"go", "install", "golang.org/x/tools/gopls@v0.23.0"}, // versioned module
		{"go", "install", "golang.org/x/tools/gopls@latest"},  // and its floating form
	}
	for _, argv := range allow {
		if !Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("%v should be allowed: a versioned module is not this module's build", argv)
		}
	}

	deny := [][]string{
		{"go", "install", "./cmd/ctxloom"}, // this module, by path
		{"go", "install"},                  // and bare
	}
	for _, argv := range deny {
		if Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
			t.Errorf("%v should still be denied; the exemption must not disable the rule", argv)
		}
	}
}

// A glob cannot express the case above, which is why the field matches a
// substring: Go's path.Match stops `*` at `/`, so no pattern spans a module
// path like golang.org/x/tools/gopls@v1. Pinned so nobody "simplifies" the
// field into a glob and silently stops exempting anything.
func TestUnlessArgContainsIsNotAGlob(t *testing.T) {
	cfg := &Config{Rules: []Rule{{
		ID:      "install-via-just",
		Match:   Match{Command: CommandPattern{"go", "install"}, UnlessArgContains: []string{"@"}},
		Message: "Install through the task runner",
	}}}
	argv := []string{"go", "install", "example.com/deep/nested/path/tool@v1.2.3"}
	if !Evaluate(cfg, cmd(ir.ShellBash, argv...)).Allowed {
		t.Errorf("%v should be allowed: the match is a substring, so path separators are irrelevant", argv)
	}
}
