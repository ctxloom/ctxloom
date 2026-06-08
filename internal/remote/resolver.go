package remote

import (
	"context"
	"fmt"
)

// RefFetcher performs the source-specific raw retrieval for one scheme of the
// reference grammar (SOURCE@kind/name#section/item@version). It is the small,
// scheme-dependent surface of content resolution: given a parsed Reference and
// an already-resolved content version, it returns the raw item YAML bytes.
//
// The content version is a VCS revision — a commit/tag/changeset/rev — and
// applies to BOTH remote and local sources: local content (ctxloom:local) is
// version-controlled too, just backed by a working copy rather than a clone of
// a remote. The interface is deliberately VCS-neutral (git today, but nothing
// here precludes hg/svn): "version" is an opaque revision string the fetcher
// knows how to resolve against its own backend.
//
// Everything scheme-INDEPENDENT — string parsing, item-type/path selection,
// #section/item cherry-pick, and version pinning — belongs in Resolver or its
// callers, NOT here. A RefFetcher only knows how to turn (source, item-path,
// version) into bytes.
//
// This sits one layer ABOVE the VCS backend (revision-addressable reads). The
// remote RefFetcher opens a VCS bound to a cloned remote repo; the future local
// RefFetcher (ctxloom:local) opens a VCS bound to the project working copy
// under .ctxloom/local/. Resolver is blind to that difference — that is the
// whole point of the seam.
type RefFetcher interface {
	// Handles reports whether this fetcher serves ref's source (scheme).
	Handles(ref *Reference) bool

	// FetchItem returns the raw YAML bytes for ref's item — a whole bundle or
	// profile file — at version. version is an OPAQUE, backend-defined revision
	// string: a git SHA/tag today, an hg changeset or svn rev tomorrow. It is
	// never parsed or validated here — it is passed through to the backend
	// verbatim. Empty version means the source's current/default state.
	// Cherry-pick of an individual #section/item is the caller's concern and
	// operates on the returned bytes: FetchItem always returns the complete item.
	FetchItem(ctx context.Context, ref *Reference, version string) ([]byte, error)
}

// Resolver is the scheme-dispatched content resolver: the shared base of the
// reference grammar. It owns the uniform tail (scheme dispatch and version
// selection) and delegates only the source-specific byte retrieval to a
// per-scheme RefFetcher. One Resolver is configured with every scheme's
// fetcher; Resolve picks the first whose Handles matches the reference.
//
// This is the additive Phase 2 seam: Resolver exists for the loader/seed to
// route through in later phases. It does not yet replace any existing read
// path — adding it changes no behavior on its own.
type Resolver struct {
	fetchers []RefFetcher
}

// NewResolver builds a Resolver over the given per-scheme fetchers, tried in
// registration order. Callers pass the remote fetcher today; the local
// (ctxloom:local) fetcher joins in a later phase.
func NewResolver(fetchers ...RefFetcher) *Resolver {
	return &Resolver{fetchers: fetchers}
}

// Resolve returns the raw item bytes for ref, dispatching to the first
// registered fetcher whose scheme matches. The content version is taken from
// the reference itself (already pinned by the caller / lockfile); a missing
// version resolves to the source default.
func (r *Resolver) Resolve(ctx context.Context, ref *Reference) ([]byte, error) {
	if ref == nil {
		return nil, fmt.Errorf("resolve: nil reference")
	}
	for _, f := range r.fetchers {
		if f.Handles(ref) {
			return f.FetchItem(ctx, ref, ref.EffectiveContentVersion())
		}
	}
	return nil, fmt.Errorf("resolve: no fetcher for reference %q", ref.String())
}

// RemoteRefFetcher is the RefFetcher for canonical URL-sourced references
// (https://, git@, file://). It opens a VCS bound to the referenced repository
// and reads the item at the pinned revision. It holds no git knowledge itself —
// that lives in the VCSFactory it is given (GitForgeVCSFactory today).
type RemoteRefFetcher struct {
	openVCS VCSFactory
}

// NewRemoteRefFetcher constructs the remote-scheme fetcher over a VCSFactory.
// In production, wire GitForgeVCSFactory(cachedFactory, auth).
func NewRemoteRefFetcher(openVCS VCSFactory) *RemoteRefFetcher {
	return &RemoteRefFetcher{openVCS: openVCS}
}

// Handles serves canonical references that carry a repository URL. Simple
// (short) refs and the future ctxloom:local refs are left to other fetchers.
func (f *RemoteRefFetcher) Handles(ref *Reference) bool {
	return ref != nil && ref.IsCanonical && ref.URL != ""
}

// FetchItem opens the VCS for ref's repository and returns the raw item bytes
// for the bundle/profile at ref's path, as of version. The item type comes
// from the canonical reference itself.
func (f *RemoteRefFetcher) FetchItem(ctx context.Context, ref *Reference, version string) ([]byte, error) {
	if !f.Handles(ref) {
		return nil, fmt.Errorf("remote fetcher: cannot handle reference %q", ref.String())
	}

	vcs, err := f.openVCS(ref.URL)
	if err != nil {
		return nil, fmt.Errorf("open source %s: %w", ref.URL, err)
	}

	filePath := ref.BuildFilePath(ref.ItemType)
	data, err := readItemAt(ctx, vcs, filePath, version)
	if err != nil {
		return nil, fmt.Errorf("read %s@%s: %w", filePath, version, err)
	}
	return data, nil
}

// Ensure RemoteRefFetcher satisfies the per-scheme interface at compile time.
var _ RefFetcher = (*RemoteRefFetcher)(nil)

// LocalRefFetcher is the RefFetcher for ctxloom:local references — project
// authored content under the committed .ctxloom/local/ working copy. It opens a
// VCS bound to that directory and reads the referenced item.
//
// The VCSFactory chooses the backend: a plain filesystem (current-only, via
// FSVCSFactory) by default, or a working-copy VCS that adds history + pinning
// when the project is itself under version control. The fetcher itself is
// backend-agnostic — like RemoteRefFetcher, it only locates the source and the
// item path, then defers to readItemAt for capability-aware reading.
type LocalRefFetcher struct {
	openVCS VCSFactory
	root    string
}

// NewLocalRefFetcher constructs the local-scheme fetcher. root is the committed
// local-content directory (paths.LocalPath(appDir)); openVCS opens a VCS over it.
func NewLocalRefFetcher(openVCS VCSFactory, root string) *LocalRefFetcher {
	return &LocalRefFetcher{openVCS: openVCS, root: root}
}

// Handles serves ctxloom:local references; everything else is left to other
// fetchers.
func (f *LocalRefFetcher) Handles(ref *Reference) bool {
	return ref != nil && ref.IsLocal
}

// FetchItem opens the local source and returns the raw item bytes at version.
// A versionless ref (the common local case) reads the working copy; a pinned
// ref reads history when the backend supports it, else errors via readItemAt.
func (f *LocalRefFetcher) FetchItem(ctx context.Context, ref *Reference, version string) ([]byte, error) {
	if !f.Handles(ref) {
		return nil, fmt.Errorf("local fetcher: cannot handle reference %q", ref.String())
	}

	vcs, err := f.openVCS(f.root)
	if err != nil {
		return nil, fmt.Errorf("open local source %s: %w", f.root, err)
	}

	filePath := ref.BuildFilePath(ref.ItemType)
	data, err := readItemAt(ctx, vcs, filePath, version)
	if err != nil {
		return nil, fmt.Errorf("read %s@%s: %w", filePath, version, err)
	}
	return data, nil
}

// Ensure LocalRefFetcher satisfies the per-scheme interface at compile time.
var _ RefFetcher = (*LocalRefFetcher)(nil)
