package remote

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	semver "github.com/Masterminds/semver/v3"
)

// SelectorKind classifies a reference's version selector (the optional trailing
// "@<expr>" of a reference, Reference.ContentVersion) into one of four kinds that
// drive resolution, lock persistence, and update semantics. The user-facing story
// is three modes — PIN (sha or tag), VERSION (a semver range), and TRACK (a
// branch) — but the lock records the precise kind so "outdated" is declarative:
//
//   - SelectorSHA / SelectorTag: pinned — the lock never moves, never outdated.
//   - SelectorVersion: re-resolves to the highest tag satisfying the range.
//   - SelectorBranch: re-resolves to the branch tip (empty selector ⇒ the
//     repository's default branch).
type SelectorKind string

const (
	// SelectorSHA is a commit object name — pinned, never outdated.
	SelectorSHA SelectorKind = "sha"
	// SelectorTag is an exact (non-semver) tag — pinned, never outdated.
	SelectorTag SelectorKind = "tag"
	// SelectorVersion is a semver version or range, matched against tags.
	SelectorVersion SelectorKind = "version"
	// SelectorBranch tracks a branch tip; an empty selector ⇒ the default branch.
	SelectorBranch SelectorKind = "branch"
)

// IsPin reports whether the kind is a fixed pin — a commit or an exact tag —
// that never goes outdated (re-resolving it yields the same commit). A version
// range re-resolves to the highest satisfying tag and a branch to its tip, so
// those are NOT pins.
func (k SelectorKind) IsPin() bool {
	return k == SelectorSHA || k == SelectorTag
}

// selectorPrefixes maps an explicit "kind:" disambiguator to its kind. An
// explicit prefix overrides shape inference (e.g. "branch:1.0" forces a branch
// named "1.0" that would otherwise infer as a version; "tag:main" pins a tag
// named "main" instead of tracking the branch).
var selectorPrefixes = map[string]SelectorKind{
	"sha:":     SelectorSHA,
	"tag:":     SelectorTag,
	"version:": SelectorVersion,
	"branch:":  SelectorBranch,
}

// parseSelector strips an optional explicit "kind:" prefix from expr, returning
// the forced kind ("" when none) and the bare expression.
func parseSelector(expr string) (forced SelectorKind, bare string) {
	for prefix, kind := range selectorPrefixes {
		if rest, ok := strings.CutPrefix(expr, prefix); ok {
			return kind, rest
		}
	}
	return "", expr
}

// shaLike matches a bare git object name: 7–40 lowercase hex digits and nothing
// else. Such a string is classified as a commit (SelectorSHA) BEFORE the semver
// parser sees it, so an abbreviated SHA like "1234567" is never mistaken for the
// semver version 1234567.0.0. Real version expressions carry a dot or a 'v'
// prefix (e.g. "v1.2.3", "1.2.x") and so fail this test, falling through to the
// semver check.
var shaLike = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// SelectorKind returns the entry's recorded selector kind, deriving it from
// RequestedVersion (shape only) for entries written before the Kind field
// existed. A recorded Version means a version selector; otherwise sha/empty
// classify exactly and a bare tag/branch name (unclassifiable without a repo)
// derives to SelectorBranch — the safe re-resolving default, since re-resolving
// an immutable tag by name yields the same SHA (never spuriously outdated).
func (e LockEntry) SelectorKind() SelectorKind {
	if e.Kind != "" {
		return e.Kind
	}
	if e.Version != "" {
		return SelectorVersion
	}
	if k := inferSelectorKind(e.RequestedVersion); k != "" {
		return k
	}
	return SelectorBranch
}

// LooksLikeCommit reports whether s is a bare git object name (7–40 hex digits).
// Such an expression is already a concrete commit, so a caller can record it
// verbatim without contacting the repository — the resolution it would produce
// is the string itself. Branch names, tags, and semver ranges are not commits
// and must be resolved against the repo.
func LooksLikeCommit(s string) bool {
	return shaLike.MatchString(s)
}

// inferSelectorKind classifies a prefix-less selector by SHAPE alone (no I/O). An
// empty expression tracks the default branch; a bare hex object name is a commit;
// a semver-parseable expression (a range, or an exact version like "1.2.3") is a
// version. Anything else — a bare name like "main" or "release" — is ambiguous
// between a tag and a branch and is resolved against the repo at resolve time
// (returns ""); tag wins there (a pin), with explicit tag:/branch: to override.
func inferSelectorKind(bare string) SelectorKind {
	switch {
	case bare == "":
		return SelectorBranch
	case shaLike.MatchString(bare):
		return SelectorSHA
	default:
		if _, err := semver.NewConstraint(bare); err == nil {
			return SelectorVersion
		}
		return ""
	}
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

// TagResolver is the optional RepoVersions capability to resolve a name through
// the tag namespace ONLY (refs/tags/<tag>). The semver resolver uses it when
// present so the tag it selected can never be shadowed by a same-named branch —
// the generic ResolveRef strategy order tries the branch namespace first.
// Probed by type assertion, mirroring tagLister.
type TagResolver interface {
	// ResolveTag resolves a tag name to its commit SHA, considering tags only.
	ResolveTag(ctx context.Context, tag string) (string, error)
}

// tagLister is the optional Fetcher capability to enumerate a repository's tags.
// The local-clone fetcher provides it and the cache fetcher forwards it; a
// backend without it yields no tags, so a semver range simply finds nothing to
// match. Probed by type assertion, mirroring itemHistorySource / Versioned.
type tagLister interface {
	ListTags(ctx context.Context, owner, repo string) ([]string, error)
}

// fetcherTagResolver is the optional Fetcher capability for tag-namespace-only
// resolution (the Fetcher-shaped counterpart of TagResolver). The clone-backed
// fetchers and the GitHub API fetcher provide it; fetcherRepoVersions forwards
// it through the RepoVersions seam.
type fetcherTagResolver interface {
	ResolveTag(ctx context.Context, owner, repo, tag string) (string, error)
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

// ResolveTag resolves a tag through the tag namespace only when the underlying
// fetcher offers that capability, falling back to the generic ResolveRef for
// backends that don't distinguish namespaces (e.g. test mocks).
func (a *fetcherRepoVersions) ResolveTag(ctx context.Context, tag string) (string, error) {
	if tr, ok := a.fetcher.(fetcherTagResolver); ok {
		return tr.ResolveTag(ctx, a.owner, a.repo, tag)
	}
	return a.fetcher.ResolveRef(ctx, a.owner, a.repo, tag)
}

func (a *fetcherRepoVersions) DefaultBranch(ctx context.Context) (string, error) {
	return a.fetcher.GetDefaultBranch(ctx, a.owner, a.repo)
}

var _ RepoVersions = (*fetcherRepoVersions)(nil)

// Resolution is the outcome of resolving a version selector against a repository:
// the commit the lockfile should pin, the classified Kind (persisted so update
// semantics are declarative), and the concrete tag chosen when a semver
// constraint or a tag selector picked one. Version is empty for sha and branch
// resolutions, where there is no single tag label to record.
type Resolution struct {
	// SHA is the commit the lockfile should pin.
	SHA string
	// Version is the tag a version/tag selector resolved (e.g. "v1.3.0"), or empty.
	Version string
	// Kind is the classified selector kind, recorded in the lockfile.
	Kind SelectorKind
}

// ResolveConstraint resolves a reference's version selector against a
// repository's version space, returning the commit to lock, the classified kind,
// and — for a version/tag selector — the tag chosen. It is the single home of the
// selector→commit policy:
//
//   - branch (empty ⇒ default): the branch's current tip.
//   - sha: the commit resolved verbatim (an abbreviated SHA expands to the full).
//   - version (semver): the highest tag satisfying the range; error if none match.
//   - tag: an exact non-semver tag, resolved through the tag namespace.
//   - a bare, prefix-less name of no inferable shape: a tag wins (pin), else a
//     branch (track); tag:/branch: overrides.
//
// The selector is honored on every resolve — the returned SHA always satisfies
// it — so a lock can never contradict the profile that requested it. This is the
// invariant the whole version model rests on.
func ResolveConstraint(ctx context.Context, expr string, rv RepoVersions) (Resolution, error) {
	forced, bare := parseSelector(expr)
	kind := forced
	if kind == "" {
		kind = inferSelectorKind(bare)
	}
	switch kind {
	case SelectorBranch:
		return resolveBranch(ctx, bare, rv)
	case SelectorSHA:
		sha, err := rv.ResolveRef(ctx, bare)
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve commit %q: %w", bare, err)
		}
		return Resolution{SHA: sha, Kind: SelectorSHA}, nil
	case SelectorVersion:
		return resolveSemver(ctx, bare, rv)
	case SelectorTag:
		sha, err := resolveTagSHA(ctx, rv, bare)
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve tag %q: %w", bare, err)
		}
		return Resolution{SHA: sha, Version: bare, Kind: SelectorTag}, nil
	default:
		// Prefix-less bare name of no inferable shape: tag wins (a pin), else branch.
		return resolveNameTagFirst(ctx, bare, rv)
	}
}

// resolveBranch pins the tip of a branch; an empty name uses the default branch.
func resolveBranch(ctx context.Context, name string, rv RepoVersions) (Resolution, error) {
	if name == "" {
		b, err := rv.DefaultBranch(ctx)
		if err != nil {
			return Resolution{}, fmt.Errorf("determine default branch: %w", err)
		}
		name = b
	}
	sha, err := rv.ResolveRef(ctx, name)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve branch %q: %w", name, err)
	}
	return Resolution{SHA: sha, Kind: SelectorBranch}, nil
}

// resolveNameTagFirst resolves a bare, prefix-less name whose shape can't classify
// it: if the repo has a tag of that name it is a PIN (SelectorTag); otherwise it
// is treated as a branch to TRACK (SelectorBranch). Explicit tag:/branch:
// overrides this. A backend that can't list tags reports none, so the name
// resolves as a branch — the safe fault-tolerant direction.
func resolveNameTagFirst(ctx context.Context, name string, rv RepoVersions) (Resolution, error) {
	isTag, err := isRepoTag(ctx, rv, name)
	if err != nil {
		return Resolution{}, fmt.Errorf("determine whether %q is a tag: %w", name, err)
	}
	if isTag {
		sha, err := resolveTagSHA(ctx, rv, name)
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve tag %q: %w", name, err)
		}
		return Resolution{SHA: sha, Version: name, Kind: SelectorTag}, nil
	}
	return resolveBranch(ctx, name, rv)
}

// isRepoTag reports whether name is exactly one of the repository's tags. A
// failed tag listing is reported to the caller rather than silently read as
// "not a tag" — swallowing it here used to let a transient listing
// failure fall through to resolveBranch and silently convert what may have
// been intended as a tag PIN into branch TRACKING.
func isRepoTag(ctx context.Context, rv RepoVersions, name string) (bool, error) {
	tags, err := rv.ListTags(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range tags {
		if t == name {
			return true, nil
		}
	}
	return false, nil
}

// resolveSemver selects the highest tag satisfying expr and pins its commit.
// Tags that do not parse as semver are not candidates; a constraint that nothing
// satisfies is an error (the request cannot be met from this repo's tags).
func resolveSemver(ctx context.Context, expr string, rv RepoVersions) (Resolution, error) {
	constraint, err := semver.NewConstraint(expr)
	if err != nil {
		// On the INFERRED path inferSelectorKind already proved expr parses, so
		// this is defensive. On the FORCED path ("version:<expr>") parseSelector
		// never parses it, so this is the only validation there and can really
		// fire. Either way: an error, never a panic.
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
	// is the actual ref name to resolve back to a commit. We KNOW this is a tag,
	// so resolve through the tag namespace when the backend can (TagResolver) —
	// the generic ResolveRef tries branches first, and a branch upstream named
	// like the tag would silently make the lock track the branch tip.
	tag := best.Original()
	sha, err := resolveTagSHA(ctx, rv, tag)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve matched tag %q: %w", tag, err)
	}
	return Resolution{SHA: sha, Version: tag, Kind: SelectorVersion}, nil
}

// resolveTagSHA resolves a known-tag name to its commit: through the optional
// TagResolver capability when present (tag namespace only), else the generic
// ResolveRef.
func resolveTagSHA(ctx context.Context, rv RepoVersions, tag string) (string, error) {
	if tr, ok := rv.(TagResolver); ok {
		return tr.ResolveTag(ctx, tag)
	}
	return rv.ResolveRef(ctx, tag)
}
