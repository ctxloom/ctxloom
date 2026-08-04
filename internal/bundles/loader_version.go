package bundles

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// Multi-version coexistence (trust rework, TR5)
//
// The default loader materializes ONE version of a bundle — the lockfile-pinned
// SHA (via the seeded-bundle map) or the on-disk copy. These methods add the
// ability to present MULTIPLE commit-versions of the same ref in one assembly,
// each materialized by its own commit and gated INDEPENDENTLY by its own
// effective-content hash through the existing trust gate.
//
// Addressing follows the plan: a logical ref "<bundle>#fragments/<name>" plus an
// opaque "@<commit>" revision. The version-less ref is the trust identity — a
// grant keyed {repo, ref, content_hash} matches whichever commit produced that
// content — so two commits with identical content dedup to one item, while a
// trusted version and a blacklisted version of the same ref coexist with only
// the trusted one surviving.
//
// Load / ReadFragment / ReadCommand are deliberately UNTOUCHED: the
// lockfile-pinned default path is unchanged, and per-version resolution is
// opt-in via these methods (so an "@<version>" on an ordinary profile ref keeps
// resolving to the lockfile default, never silently refetching a
// tag/constraint as a commit).

// bundleAtVersion materializes the bundle named by bundleRef at commit. An empty
// commit (or a bundleRef that itself carries no "@<commit>") resolves the
// lockfile-pinned default via Load — identical to the non-versioned path. A
// non-empty commit materializes that exact historical version through the wired
// version resolver, caching the parsed result per (canonical-ref, commit) for the
// loader's lifetime. The returned bundle's Name is the VERSION-LESS canonical ref
// so the content gate keys on the same identity regardless of which commit served
// it (multiple grants per ref, one per content_hash).
func (l *Loader) bundleAtVersion(bundleRef, commit string) (*Bundle, error) {
	// A bundleRef may itself carry the version ("<bundle>@<commit>"); an explicit
	// commit argument wins, else fall back to the ref's own pinned version.
	canonical, refVersion := splitBundleVersion(bundleRef)
	if commit == "" {
		commit = refVersion
	}
	if commit == "" {
		// No version pinned anywhere → the ordinary lockfile default.
		return l.Load(bundleRef)
	}
	if l.versionResolver == nil {
		return nil, fmt.Errorf("%w: cannot resolve %s@%s", errs.ErrNoVersionResolver, canonical, commit)
	}

	cacheKey := canonical + "@" + commit
	l.versionMu.Lock()
	defer l.versionMu.Unlock()
	if l.versionCache != nil {
		if b, ok := l.versionCache[cacheKey]; ok {
			return b, nil
		}
	}

	b, err := l.versionResolver(canonical, commit)
	if err != nil {
		return nil, fmt.Errorf("resolve %s@%s: %w", canonical, commit, err)
	}
	if b == nil {
		return nil, fmt.Errorf("resolve %s@%s: resolver returned nil bundle", canonical, commit)
	}
	// The version-less canonical ref is the trust/identity key: a historical
	// version is gated by its own content_hash under the SAME ref, so grants
	// keyed {repo, ref, content_hash} match regardless of the serving commit.
	b.Name = canonical
	b.Path = fmt.Sprintf("<remote-version>:%s@%s", canonical, commit)

	if l.versionCache == nil {
		l.versionCache = make(map[string]*Bundle)
	}
	l.versionCache[cacheKey] = b
	return b, nil
}

// splitBundleVersion separates a bundle reference's version-less canonical form
// from any trailing "@<commit>" content version. A ref that does not parse as a
// reference (a plain local bundle name) is returned canonicalized with an empty
// version. The version-less canonical form is the loader/trust identity key.
func splitBundleVersion(bundleRef string) (canonical, version string) {
	if parsed, err := remote.ParseReference(bundleRef); err == nil {
		version = parsed.ContentVersion
	}
	if ck, ok := remote.CanonicalKey(bundleRef); ok {
		return ck, version
	}
	return remote.CanonicalBundleRef(bundleRef), version
}

// ReadFragmentAtVersion reports a fragment from a specific commit-version of
// its bundle. ref is a qualified fragment ref ("<bundle>#fragments/<name>",
// optionally carrying its own "@<commit>" on the bundle part); commit is the
// opaque revision to materialize (empty = the lockfile-pinned default,
// identical to ReadFragment).
//
// Each version carries its OWN bytes under the VERSION-LESS TrustRef, so the
// process stage decides on each independently (a grant keyed
// {repo, ref, content_hash} matches whichever commit produced that content). A
// fetch/parse failure returns a resolve error, which costs only that version —
// the reader never falls back to a different one. A bare-name ref (no "#") has
// no bundle to pin a version against and resolves via the ordinary search
// (commit ignored).
func (l *Loader) ReadFragmentAtVersion(ref, commit string) ([]*ItemRead, error) {
	bundleRef, fragName, isRef, err := splitItemRef(ref, "fragments")
	if err != nil {
		return nil, err
	}
	if !isRef {
		return l.searchFragment(ref)
	}
	bundle, err := l.bundleAtVersion(bundleRef, commit)
	if err != nil {
		return nil, err
	}
	frag, ok := bundle.Fragments[fragName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrFragmentNotFound, fragName, bundle.Name)
	}
	return []*ItemRead{l.fragmentRead(bundle, fragName, frag)}, nil
}

// ReadCommandAtVersion is the command counterpart to ReadFragmentAtVersion: a
// command from a specific commit-version of its bundle, carrying that version's
// own bytes under the version-less TrustRef.
func (l *Loader) ReadCommandAtVersion(ref, commit string) ([]*ItemRead, error) {
	bundleRef, promptName, isRef, err := splitItemRef(ref, "commands")
	if err != nil {
		return nil, err
	}
	if !isRef {
		return l.searchCommand(ref)
	}
	bundle, err := l.bundleAtVersion(bundleRef, commit)
	if err != nil {
		return nil, err
	}
	prompt, ok := bundle.Commands[promptName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrCommandNotFound, promptName, bundle.Name)
	}
	return []*ItemRead{l.commandRead(bundle, promptName, prompt)}, nil
}

// ReadFragmentVersions reports the fragment named by ref at each requested
// commit (use "" for the lockfile-pinned default), in request order. It is the
// read half of the multi-version coexistence primitive: several
// commit-versions of one ref, each carrying its own bytes, so the process stage
// can decide on each independently (see Pipeline.ResolveFragmentVersions).
//
// A version that fails to fetch or parse is omitted — the reader does not HAVE
// it, which is a read fact, not a filtering decision — and its omission never
// falls back to a different version.
func (l *Loader) ReadFragmentVersions(ref string, commits []string) []*ItemRead {
	if len(commits) == 0 {
		commits = []string{""}
	}
	var out []*ItemRead
	for _, commit := range commits {
		reads, err := l.ReadFragmentAtVersion(ref, commit)
		if err != nil {
			continue
		}
		out = append(out, reads...)
	}
	return out
}
