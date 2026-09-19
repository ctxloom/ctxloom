package remote

import (
	"context"
	"errors"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/errs"
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
// under .ctxloom/content/. Resolver is blind to that difference — that is the
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

	// ListItems returns a canonical (or local) Reference for every item of kind
	// this scheme can serve across its configured sources, at each source's
	// current state. It is the listing counterpart to FetchItem: where FetchItem
	// reads ONE item named by a ref, ListItems enumerates the refs themselves.
	// Sources that cannot be listed (e.g. an un-downloaded remote) are skipped;
	// the returned error joins any such per-source failures so the caller can warn
	// while still using the items that did list (fault-tolerant per CLAUDE.md).
	ListItems(ctx context.Context, kind ItemType) ([]*Reference, error)
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
			return f.FetchItem(ctx, ref, ref.ContentVersion)
		}
	}
	return nil, fmt.Errorf("resolve: no fetcher for reference %q", ref.String())
}

// List enumerates every item of kind across every scheme, fanning out to each
// registered fetcher's ListItems and concatenating the references. It is
// fault-tolerant: a fetcher (or one of its sources) that fails to list is
// skipped, its error joined into the returned error so the caller can warn while
// still using what did list. The result is the listing counterpart to Resolve.
func (r *Resolver) List(ctx context.Context, kind ItemType) ([]*Reference, error) {
	var refs []*Reference
	var errs []error
	for _, f := range r.fetchers {
		got, err := f.ListItems(ctx, kind)
		if err != nil {
			errs = append(errs, err)
		}
		refs = append(refs, got...)
	}
	return refs, errors.Join(errs...)
}

// DeletedItemLister is the OPTIONAL listing capability for surfacing items
// removed upstream — present at a past revision, gone now. A RefFetcher whose
// backend can walk history (the remote git clone) implements it; schemes without
// history (local working-copy content) do not. Resolver.ListDeleted probes for
// it by type assertion, exactly like Versioned extends VCS.
type DeletedItemLister interface {
	ListDeletedItems(ctx context.Context, kind ItemType) ([]*Reference, error)
}

// ListDeleted enumerates items removed upstream across every scheme that can
// report them (those whose fetcher implements DeletedItemLister). Schemes
// without history are skipped. Like List, it is fault-tolerant and joins
// per-source errors. It is the deleted-item counterpart to List.
func (r *Resolver) ListDeleted(ctx context.Context, kind ItemType) ([]*Reference, error) {
	var refs []*Reference
	var failures []error
	for _, f := range r.fetchers {
		lister, ok := f.(DeletedItemLister)
		if !ok {
			continue
		}
		got, err := lister.ListDeletedItems(ctx, kind)
		if err != nil {
			failures = append(failures, err)
		}
		refs = append(refs, got...)
	}
	return refs, errors.Join(failures...)
}

// RemoteRefFetcher is the RefFetcher for canonical URL-sourced references
// (https://, git@, file://). It opens a VCS bound to the referenced repository
// and reads the item at the pinned revision. It holds no git knowledge itself —
// that lives in the VCSFactory it is given (GitForgeVCSFactory today).
type RemoteRefFetcher struct {
	openVCS VCSFactory
	sources func() []string
}

// RemoteRefFetcherOption configures an optional capability on the remote fetcher.
type RemoteRefFetcherOption func(*RemoteRefFetcher)

// WithRemoteSources supplies the repo URLs ListItems enumerates. Reads
// (FetchItem) get their source from the reference itself and need no source set;
// listing has no such ref, so the caller declares which repos to walk — the
// lockfile's installed URLs for `bundle list`. Without this option ListItems
// returns nothing (the fetcher still serves FetchItem).
func WithRemoteSources(urls []string) RemoteRefFetcherOption {
	return func(f *RemoteRefFetcher) {
		f.sources = func() []string { return urls }
	}
}

// NewRemoteRefFetcher constructs the remote-scheme fetcher over a VCSFactory.
// In production, wire GitForgeVCSFactory(cachedFactory, auth). Pass
// WithRemoteSources to enable ListItems.
func NewRemoteRefFetcher(openVCS VCSFactory, opts ...RemoteRefFetcherOption) *RemoteRefFetcher {
	f := &RemoteRefFetcher{openVCS: openVCS}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Handles serves canonical references that carry a repository URL. URL-less
// refs (local ctxloom:local content) are left to other fetchers.
func (f *RemoteRefFetcher) Handles(ref *Reference) bool {
	return ref != nil && ref.IsCanonical()
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

// ListItems walks each configured source repo's tree and returns a canonical
// reference for every bundle/profile of kind found. A source whose local clone
// is absent (not yet downloaded) is NOT silently dropped: its failure is wrapped
// as a "not materialized" error and joined into the result, so the caller warns
// the user (a non-materialized remote is invisible to listing until synced)
// while still returning the items from the remotes that ARE present.
func (f *RemoteRefFetcher) ListItems(ctx context.Context, kind ItemType) ([]*Reference, error) {
	if f.sources == nil {
		return nil, nil
	}
	var refs []*Reference
	var failures []error
	for _, url := range f.sources() {
		vcs, err := f.openVCS(url)
		if err != nil {
			failures = append(failures, fmt.Errorf("%w: %s — run `ctxloom deps pull` to download it", errs.ErrRemoteNotMaterialized, url))
			continue
		}
		paths, err := vcs.ListItems(ctx, kind)
		if err != nil {
			failures = append(failures, fmt.Errorf("list %s: %w", url, err))
			continue
		}
		for _, p := range paths {
			refs = append(refs, &Reference{URL: url, ItemType: kind, Path: p})
		}
	}
	return refs, errors.Join(failures...)
}

// ListDeletedItems returns references for items removed upstream across the
// configured sources, using each source's Versioned history capability. A source
// whose clone is absent or whose backend has no history is skipped silently —
// ListItems already surfaced the not-materialized case, so this does not warn
// again. Per-source history-walk failures are joined into the error.
func (f *RemoteRefFetcher) ListDeletedItems(ctx context.Context, kind ItemType) ([]*Reference, error) {
	if f.sources == nil {
		return nil, nil
	}
	var refs []*Reference
	var failures []error
	for _, url := range f.sources() {
		vcs, err := f.openVCS(url)
		if err != nil {
			continue
		}
		versioned, ok := vcs.(Versioned)
		if !ok {
			continue
		}
		paths, err := versioned.ListDeletedItems(ctx, kind)
		if err != nil {
			failures = append(failures, fmt.Errorf("list deleted %s: %w", url, err))
			continue
		}
		for _, p := range paths {
			refs = append(refs, &Reference{URL: url, ItemType: kind, Path: p})
		}
	}
	return refs, errors.Join(failures...)
}

// Ensure RemoteRefFetcher satisfies the per-scheme interface at compile time,
// plus the optional deleted-item capability.
var (
	_ RefFetcher        = (*RemoteRefFetcher)(nil)
	_ DeletedItemLister = (*RemoteRefFetcher)(nil)
)

// LocalRefFetcher is the RefFetcher for ctxloom:local references — project
// authored content under the committed .ctxloom/content/ working copy. It opens a
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

// ListItems walks the committed local-content root and returns a ctxloom:local
// reference for every bundle/profile of kind found there. The single source is
// the project working copy, so there is no "not materialized" case.
func (f *LocalRefFetcher) ListItems(ctx context.Context, kind ItemType) ([]*Reference, error) {
	vcs, err := f.openVCS(f.root)
	if err != nil {
		return nil, fmt.Errorf("open local source %s: %w", f.root, err)
	}
	paths, err := vcs.ListItems(ctx, kind)
	if err != nil {
		return nil, err
	}
	refs := make([]*Reference, 0, len(paths))
	for _, p := range paths {
		refs = append(refs, &Reference{IsLocal: true, ItemType: kind, Path: p})
	}
	return refs, nil
}

// Ensure LocalRefFetcher satisfies the per-scheme interface at compile time.
var _ RefFetcher = (*LocalRefFetcher)(nil)
