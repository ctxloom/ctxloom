package rules

import (
	"errors"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Command fields are anchored regexes and match.path is a glob. These pin the
// two languages side by side, since a pattern that means one thing in one
// field means another in the other.

// A metacharacter is a regex operator in a command field: `*` alone does not
// compile (nothing to repeat), so it fails the load instead of silently
// matching or never firing.
func TestBareStarInACommandFieldIsRefused(t *testing.T) {
	re := parseErr(t, denyRule(`{ command: [rm, '*'] }`))
	if !errors.Is(re, ErrInvalidPattern) {
		t.Errorf("want ErrInvalidPattern, got %v", re)
	}
}

// Escaped, the metacharacter is literal: argv reaches the matcher un-globbed,
// so `rm *` carries a literal `*` element.
func TestEscapedMetacharacterMatchesLiterally(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [rm, '\*'] }`))
	if !denied(t, cfg, ir.ShellBash, "rm", "*") {
		t.Error(`'\*' must match a literal * argument`)
	}
	if denied(t, cfg, ir.ShellBash, "rm", "notes.txt") {
		t.Error(`'\*' must not match an arbitrary argument`)
	}
}

// `push*` is the regex "pus followed by any number of h", not a glob: it
// matches push and pus, and not push-x.
func TestCommandPatternIsRegexNotGlob(t *testing.T) {
	cfg := mustParse(t, denyRule(`{ command: [git, 'push*'] }`))
	if !denied(t, cfg, ir.ShellBash, "git", "pus") {
		t.Error("'push*' is a regex: it matches pus")
	}
	if denied(t, cfg, ir.ShellBash, "git", "push-x") {
		t.Error("'push*' is not a glob: it must not match push-x")
	}
}

// match.path is the contrasting field: its patterns ARE globs.
func TestPathPatternDoesGlob(t *testing.T) {
	cfg := mustParse(t, "version: 1\npath_rules:\n  - id: x\n    match: { path: [\"*.lock\"] }\n    message: m\n")
	if d := EvaluatePath(cfg, "/proj/a/b/go.lock"); d.Allowed {
		t.Error("match.path patterns ARE globs; `*.lock` must catch go.lock")
	}
}
