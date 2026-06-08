package remote

import (
	"context"
	"fmt"
	"regexp"

	semver "github.com/Masterminds/semver/v3"
)

// ConstraintKind classifies how a reference's version expression resolves
// against a repository's version space. The version expression is the optional
// trailing "@<expr>" of a reference (Reference.ContentVersion): a profile pins a
// bundle or parent to a constraint, and the lockfile records the commit that
// satisfies it. There are three resolution shapes:
//
//   - ConstraintDefault: no expression — track the repository's default branch.
//   - ConstraintSemver:  a semver version or range ("v1.2.3", "^1.2", ">=1.2,<2",
//     "1.2.x") — resolved against the repo's semver tags; the highest match wins.
//   - ConstraintDirect:  anything else (a branch name, a commit SHA, or a
//     non-semver tag) — resolved verbatim to a commit.
type ConstraintKind int

const (
	// ConstraintDefault is an empty expression: track the default branch.
	ConstraintDefault ConstraintKind = iota
	// ConstraintSemver is a semver version or range, matched against tags.
	ConstraintSemver
	// ConstraintDirect is a branch, SHA, or non-semver tag, resolved verbatim.
	ConstraintDirect
)

// shaLike matches a bare git object name: 7–40 lowercase hex digits and nothing
// else. Such a string is classified as a direct ref (a commit) BEFORE the semver
// parser sees it, so an abbreviated SHA like "1234567" is never mistaken for the
// semver version 1234567.0.0. Real version expressions carry a dot or a 'v'
// prefix (e.g. "v1.2.3", "1.2.x") and so fail this test, falling through to the
// semver check.
var shaLike = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// LooksLikeCommit reports whether s is a bare git object name (7–40 hex digits).
// Such an expression is already a concrete commit, so a caller can record it
// verbatim without contacting the repository — the resolution it would produce
// is the string itself. Branch names, tags, and semver ranges are not commits
// and must be resolved against the repo.
func LooksLikeCommit(s string) bool {
	return shaLike.MatchString(s)
}

// ClassifyConstraint reports how expr should be resolved. expr is a reference's
// version expression (Reference.ContentVersion): empty, a semver constraint, or
// a direct ref. It performs no I/O — it only inspects the string's shape.
func ClassifyConstraint(expr string) ConstraintKind {
	if expr == "" {
		return ConstraintDefault
	}
	// A bare object name is a commit, not the semver version of the same digits.
	if shaLike.MatchString(expr) {
		return ConstraintDirect
	}
	if _, err := semver.NewConstraint(expr); err == nil {
		return ConstraintSemver
	}
	return ConstraintDirect
}

// RepoVersions exposes a single repository's version space for constraint
// resolution. It is the minimal seam the resolver needs — the tag list (for
// semver matching), ref→commit resolution, and the default branch — so the
// resolution algorithm is unit-testable without a real clone. The production
// adapter (Phase-2 wiring) binds a forge Fetcher to one owner/repo and satisfies
// this interface.
type RepoVersions interface {
	// ListTags returns the repository's tag names (e.g. "v1.2.3", "v2.0.0"), in
	// any order. Non-semver tags may be present; the resolver ignores those that
	// do not parse as semver when matching a range.
	ListTags(ctx context.Context) ([]string, error)
	// ResolveRef resolves a tag, branch, or commit SHA to a full commit SHA.
	ResolveRef(ctx context.Context, ref string) (string, error)
	// DefaultBranch returns the repository's default branch name.
	DefaultBranch(ctx context.Context) (string, error)
}

// tagLister is the optional Fetcher capability to enumerate a repository's tags.
// The local-clone fetcher provides it and the cache fetcher forwards it; a
// backend without it yields no tags, so a semver range simply finds nothing to
// match. Probed by type assertion, mirroring itemHistorySource / Versioned.
type tagLister interface {
	ListTags(ctx context.Context, owner, repo string) ([]string, error)
}

// fetcherRepoVersions adapts a forge Fetcher bound to one repository to the
// RepoVersions seam the constraint resolver consumes.
type fetcherRepoVersions struct {
	fetcher     Fetcher
	owner, repo string
}

// NewFetcherRepoVersions adapts a forge Fetcher (bound to owner/repo) into a
// RepoVersions for constraint resolution. Tag listing requires the fetcher to
// implement the optional tagLister capability (the clone-backed fetchers do);
// without it ListTags returns empty, so only default and direct constraints
// resolve — a semver range over a tag-less backend yields no match.
func NewFetcherRepoVersions(fetcher Fetcher, owner, repo string) RepoVersions {
	return &fetcherRepoVersions{fetcher: fetcher, owner: owner, repo: repo}
}

func (a *fetcherRepoVersions) ListTags(ctx context.Context) ([]string, error) {
	tl, ok := a.fetcher.(tagLister)
	if !ok {
		return nil, nil
	}
	return tl.ListTags(ctx, a.owner, a.repo)
}

func (a *fetcherRepoVersions) ResolveRef(ctx context.Context, ref string) (string, error) {
	return a.fetcher.ResolveRef(ctx, a.owner, a.repo, ref)
}

func (a *fetcherRepoVersions) DefaultBranch(ctx context.Context) (string, error) {
	return a.fetcher.GetDefaultBranch(ctx, a.owner, a.repo)
}

var _ RepoVersions = (*fetcherRepoVersions)(nil)

// Resolution is the outcome of resolving a version constraint against a
// repository: the commit the lockfile should pin, plus the concrete tag chosen
// when a semver constraint selected one. Version is empty for default-branch and
// direct-ref resolutions, where there is no single tag label to record.
type Resolution struct {
	// SHA is the commit the lockfile should pin.
	SHA string
	// Version is the tag a semver constraint matched (e.g. "v1.3.0"), or empty.
	Version string
}

// ResolveConstraint resolves a reference's version expression against a
// repository's version space, returning the commit to lock and — for a semver
// match — the tag chosen. It is the single home of the constraint→commit policy:
//
//   - default (empty expr): the default branch's current tip.
//   - semver: the highest tag satisfying the constraint; an error if none match.
//   - direct: the branch/tag/SHA resolved verbatim.
//
// The constraint is honored on every resolve — the returned SHA always satisfies
// expr — so a lock can never contradict the profile that requested it. This is
// the invariant the whole version model rests on.
func ResolveConstraint(ctx context.Context, expr string, rv RepoVersions) (Resolution, error) {
	switch ClassifyConstraint(expr) {
	case ConstraintDefault:
		return resolveDefaultBranch(ctx, rv)
	case ConstraintSemver:
		return resolveSemver(ctx, expr, rv)
	default:
		sha, err := rv.ResolveRef(ctx, expr)
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve ref %q: %w", expr, err)
		}
		return Resolution{SHA: sha}, nil
	}
}

// resolveDefaultBranch pins the tip of the repository's default branch.
func resolveDefaultBranch(ctx context.Context, rv RepoVersions) (Resolution, error) {
	branch, err := rv.DefaultBranch(ctx)
	if err != nil {
		return Resolution{}, fmt.Errorf("determine default branch: %w", err)
	}
	sha, err := rv.ResolveRef(ctx, branch)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve default branch %q: %w", branch, err)
	}
	return Resolution{SHA: sha}, nil
}

// resolveSemver selects the highest tag satisfying expr and pins its commit.
// Tags that do not parse as semver are not candidates; a constraint that nothing
// satisfies is an error (the request cannot be met from this repo's tags).
func resolveSemver(ctx context.Context, expr string, rv RepoVersions) (Resolution, error) {
	constraint, err := semver.NewConstraint(expr)
	if err != nil {
		// ClassifyConstraint already validated expr parses; treat a late failure
		// defensively rather than panic.
		return Resolution{}, fmt.Errorf("invalid version constraint %q: %w", expr, err)
	}
	tags, err := rv.ListTags(ctx)
	if err != nil {
		return Resolution{}, fmt.Errorf("list tags: %w", err)
	}
	var best *semver.Version
	for _, tag := range tags {
		v, verr := semver.NewVersion(tag)
		if verr != nil {
			continue // non-semver tag — not a candidate for a range
		}
		if !constraint.Check(v) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
		}
	}
	if best == nil {
		return Resolution{}, fmt.Errorf("no tag satisfies version constraint %q", expr)
	}
	// Original() preserves the tag's authored form (e.g. the 'v' prefix), which
	// is the actual ref name to resolve back to a commit.
	tag := best.Original()
	sha, err := rv.ResolveRef(ctx, tag)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve matched tag %q: %w", tag, err)
	}
	return Resolution{SHA: sha, Version: tag}, nil
}
