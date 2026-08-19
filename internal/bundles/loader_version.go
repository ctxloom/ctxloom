package bundles

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
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
func (l *Loader) bundleAtVersion(bundleRef, commit string) (BundleRead, error) {
	// A bundleRef may itself carry the version ("<bundle>@<commit>"); an explicit
	// commit argument wins, else fall back to the ref's own pinned version.
	canonical, refVersion := splitBundleVersion(bundleRef)
	if commit == "" {
		commit = refVersion
	}
	if commit == "" {
		// No version pinned anywhere → the ordinary lockfile default, whose
		// facts a real reader established.
		read, err := l.Read(bundleRef)
		if err != nil {
			return BundleRead{}, err
		}
		return read, nil
	}
	if l.versionResolver == nil {
		return BundleRead{}, fmt.Errorf("%w: cannot resolve %s@%s", errs.ErrNoVersionResolver, canonical, commit)
	}

	cacheKey := canonical + "@" + commit
	l.versionMu.Lock()
	defer l.versionMu.Unlock()
	if l.versionCache != nil {
		if b, ok := l.versionCache[cacheKey]; ok {
			return versionRead(canonical, commit, b), nil
		}
	}

	b, err := l.versionResolver(canonical, commit)
	if err != nil {
		return BundleRead{}, fmt.Errorf("resolve %s@%s: %w", canonical, commit, err)
	}
	if b == nil {
		return BundleRead{}, fmt.Errorf("resolve %s@%s: resolver returned nil bundle", canonical, commit)
	}
	// The version-less canonical ref is the trust/identity key: a historical
	// version is gated by its own content_hash under the SAME ref, so grants
	// keyed {repo, ref, content_hash} match regardless of the serving commit.
	//
	// sourceRef carries it, because sourceRef is what contentSourceRef reads
	// and therefore what the content gate keys on. Stamping it HERE — rather
	// than letting newRead fall back to versionRead's `canonical@commit` ref —
	// is what keeps a historical version under the same key as its unpinned
	// twin. `canonical` is location-derived: splitBundleVersion produced it
	// from the ref the loader was ASKED for, with the version split off, so it
	// is a resolution identity and not anything the fetched document said
	// about itself.
	//
	// Name is stamped too, for the resolution/display identity it names. It is
	// no longer load-bearing for trust, and that matters: Name is declared in
	// a bundle's own YAML, so a version path that keyed trust off it would let
	// a declared name steer which grants apply to a historical version
	// (outdated-recoil).
	//
	// sourceRefTyped is stamped alongside sourceRef, through the SAME
	// canonicalBundleRefTyped bridge repoFSReader.sourceRefTyped uses on a
	// ref of this identical canonical shape — never left for newRead's
	// only-if-empty fallback to reach, because that fallback mints
	// trust.LocalRef unconditionally and would misclassify every non-local
	// historical version. Without this, BundleRead.SourceRef on a
	// version-pinned read reported the zero BundleRef, and a zero source ref
	// is not merely undocumented — the producers that mint an item's ref from
	// it (fragmentRead, commandRead, the skill loader) would silently
	// withhold every item this path serves.
	typed, terr := canonicalBundleRefTyped(canonical)
	if terr != nil {
		warnUnmintableSource(canonical, terr)
	}
	b.sourceRef = typed
	b.sourceRefSet = true
	b.Name = canonical
	b.Path = fmt.Sprintf("<remote-version>:%s@%s", canonical, commit)

	if l.versionCache == nil {
		l.versionCache = make(map[string]*Bundle)
	}
	l.versionCache[cacheKey] = b
	return versionRead(canonical, commit, b), nil
}

// versionRead states the trust facts of a HISTORICAL version, which no Reader
// produced: BundleVersionResolver fetches one document's bytes at one commit
// and parses them, and that is all it does.
//
// The facts it reports are therefore the honest ones for that fetch:
//
//   - TrustCtx comes from the ref. A ctxloom:local pin reads the PROJECT'S OWN
//     git history, so it is local exactly as its unpinned twin is; anything else
//     crossed a forge and is remote.
//   - Signature/Signer are none/none, because no detached signature accompanied
//     the fetch — the resolver asks for the document and nothing else. This is
//     not a claim that the publisher did not sign; it is the claim that nothing
//     here verified one, which is why a historical remote version is never
//     admitted as trusted-signer. It reaches review like any unsigned remote
//     content, which is exactly what it did before the readers existed
//     (Bundle.Signer() was never stamped on this path either).
//
// It is unexported and takes the resolver's own output, so it cannot be used to
// mint a posture for anything else.
func versionRead(canonical, commit string, b *Bundle) BundleRead {
	tctx, prov := TrustCtxRemote, ProvenanceRemote
	if parsed, err := remote.ParseReference(canonical); err == nil && parsed.IsLocal {
		tctx, prov = TrustCtxLocal, ProvenanceProject
	}
	return newRead(canonical+"@"+commit, b, prov, tctx,
		signatureFacts{signature: SignatureNone, signer: SignerNone})
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
	ask, err := ParseItemAsk(ref)
	if err != nil {
		return nil, err
	}
	if !ask.Scoped {
		return l.searchFragment(ref)
	}
	if ask.Kind != trust.KindFragment {
		return nil, fmt.Errorf("%w: %q selects a %s, not a %s", errs.ErrBadItemRef, ref, ask.Kind, trust.KindFragment)
	}
	read, err := l.bundleAtVersion(ask.Bundle, commit)
	if err != nil {
		return nil, err
	}
	frag, ok := read.Bundle.Fragments[ask.Item]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrFragmentNotFound, ask.Item, read.Bundle.Name)
	}
	return []*ItemRead{l.fragmentRead(read, ask.Item, frag)}, nil
}

// ReadCommandAtVersion is the command counterpart to ReadFragmentAtVersion: a
// command from a specific commit-version of its bundle, carrying that version's
// own bytes under the version-less TrustRef.
func (l *Loader) ReadCommandAtVersion(ref, commit string) ([]*ItemRead, error) {
	ask, err := ParseItemAsk(ref)
	if err != nil {
		return nil, err
	}
	if !ask.Scoped {
		return l.searchCommand(ref)
	}
	if ask.Kind != trust.KindPrompt {
		return nil, fmt.Errorf("%w: %q selects a %s, not a %s", errs.ErrBadItemRef, ref, ask.Kind, trust.KindPrompt)
	}
	read, err := l.bundleAtVersion(ask.Bundle, commit)
	if err != nil {
		return nil, err
	}
	prompt, ok := read.Bundle.Commands[ask.Item]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrCommandNotFound, ask.Item, read.Bundle.Name)
	}
	return []*ItemRead{l.commandRead(read, ask.Item, prompt)}, nil
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
