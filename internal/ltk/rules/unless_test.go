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
// RULES.md for why the matcher stays program-agnostic. The documented
// mitigation is authoring guidance, not an engine change: prefer `mode:
// confirm` over `unless` for destructive rules (see RULES.md's "`unless` is
// matched POSITION-BLIND" section), since confirm requires a deliberate
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
			"(great — update this test and RULES.md to describe the fix) or " +
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
