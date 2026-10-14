package git

import (
	"fmt"
	"strings"
	"unicode"
)

// headsPrefix is the namespace every local branch lives under. Git spells a
// branch both ways depending on the command — `worktree list --porcelain`
// says "refs/heads/x", `rev-parse --abbrev-ref` and `branch --format=
// %(refname:short)` say "x" — so ParseBranch is the one place that knows it.
const headsPrefix = "refs/heads/"

// Branch is a local git branch name, held in ONE canonical spelling (the
// short name) so two Branch values compare with == and can never silently
// mix "x" against "refs/heads/x" — the mismatch that once refused every
// resume of a preserved agent worktree. The field is unexported on purpose:
// ParseBranch (or MustParseBranch for a literal) is the only way in, so a
// raw string cannot reach a comparison unnormalized.
//
// The zero Branch means "no branch": a detached HEAD (Worktree.Branch,
// CurrentBranch) or "the remote's default" (Clone).
type Branch struct {
	name string
}

// ParseBranch accepts a branch in either spelling — short ("ctxloom/x") or
// fully qualified ("refs/heads/ctxloom/x") — and normalizes it. It refuses
// what cannot be a branch: the empty string, git's detached sentinel "HEAD",
// whitespace or control characters, and any other ref namespace
// ("refs/tags/x" names a tag, not a branch).
func ParseBranch(s string) (Branch, error) {
	name := strings.TrimPrefix(s, headsPrefix)
	switch {
	case name == "":
		return Branch{}, fmt.Errorf("git branch %q: empty name", s)
	case name == "HEAD":
		return Branch{}, fmt.Errorf("git branch %q: HEAD is git's detached-HEAD sentinel, not a branch", s)
	case strings.HasPrefix(name, "refs/"):
		return Branch{}, fmt.Errorf("git branch %q: not under %s, so it names a different kind of ref", s, headsPrefix)
	case strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return Branch{}, fmt.Errorf("git branch %q: a branch name carries no whitespace or control characters", s)
	}
	return Branch{name: name}, nil
}

// MustParseBranch is ParseBranch for a name known valid at compile time (a
// literal, a test fixture); it panics on a bad one.
func MustParseBranch(s string) Branch {
	b, err := ParseBranch(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Short is the branch's short name ("ctxloom/x"): what `git branch`,
// `worktree add -b` and `clone --branch` take. Empty for the zero Branch.
func (b Branch) Short() string { return b.name }

// Ref is the fully-qualified ref ("refs/heads/ctxloom/x"), for a refspec that
// must not let git resolve the destination against another namespace.
// Empty for the zero Branch.
func (b Branch) Ref() string {
	if b.name == "" {
		return ""
	}
	return headsPrefix + b.name
}

// String is the short name, the spelling a human reads in a message.
func (b Branch) String() string { return b.name }

// IsZero reports the "no branch" value (detached HEAD / remote default).
func (b Branch) IsZero() bool { return b.name == "" }
