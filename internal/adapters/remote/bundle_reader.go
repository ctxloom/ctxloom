package remote

import (
	"context"
	"errors"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"sort"
)

// ErrBundleNotInLockfile is returned when a caller asks for a bundle that
// has no lockfile entry. Read sites use this to distinguish "not a remote
// bundle, fall back to local fs lookup" from real errors.
var ErrBundleNotInLockfile = errors.New("bundle not in lockfile")

// ErrTreeBundleUnreadable reports a bundle that pulled successfully in
// DIRECTORY form and whose tree is at the pinned SHA, but which THIS reader
// cannot serve because no tree reader was wired into it (see
// WithReaderTreeFetcher).
//
// It is a distinct sentinel rather than another not-found because the two call
// for opposite actions. A not-found means "pull it"; this means "the pull
// worked, and the missing piece is in ctxloom". Callers that collapse them
// print a fix that cannot fix anything (taskloom: engaged-chivalry).
//
// A reader WITH a tree fetcher never raises it: the tree form is then a form
// this reader reads, not a form it declines.
var ErrTreeBundleUnreadable = errors.New("directory-form bundle: this reader has no tree read surface wired")

// BundleByteSource is the read surface every bundle-byte producer must
// satisfy. BundleReader is the canonical implementation (git-cache-backed,
// SHA-pinned); CachingBundleReader is the in-memory decorator. A caller that
// must accept either form programs against this interface.
type BundleByteSource interface {
	// ReadBundleBytes returns the raw bundle YAML for name at its locked SHA.
	ReadBundleBytes(ctx context.Context, name string) ([]byte, error)
	// LockEntryFor returns the lockfile entry for name (or zero+false).
	LockEntryFor(name string) (LockEntry, bool)
	// ListBundleNames returns every known bundle name, sorted.
	ListBundleNames() []string
	// HasBundle reports whether the source knows about name.
	HasBundle(name string) bool
}

// BundleReader serves bundle YAML for remote bundles, version-pinned to the
// SHA recorded in the lockfile, by reading from the local git clone cache.
//
// It never writes to disk and never hits a forge API: the supplied
// FetcherFactory MUST be a cached factory (see NewCachedFetcherFactory).
//
// BundleReader is intentionally bytes-only — it has no knowledge of bundle
// YAML structure. Callers in higher layers parse via internal/core/bundles.
// This keeps the remote package free of an upward dependency on bundles
// and lets the same fetcher work for the review-flow tool that returns
// raw YAML verbatim to the model.
//
// BundleReader is uncached. Wrap with NewCachingBundleReader to memoize
// (name, sha) lookups across a session.
//
// Local-only bundles (no lockfile entry) are out of scope here.
type BundleReader struct {
	registry *Registry
	factory  FetcherFactory
	auth     AuthConfig
	lock     *Lockfile
	// treeFetch is the pinned-remote tree walker a bundle needs, wired in from
	// above exactly as Puller.treeFetch is and for the same layering reason
	// (see TreeFetchFunc). Nil means this reader refuses every bundle with
	// ErrTreeBundleUnreadable.
	treeFetch TreeFetchFunc
}

// BundleReaderOption configures a BundleReader at construction.
type BundleReaderOption func(*BundleReader)

// WithReaderTreeFetcher supplies the pinned-remote tree walker that lets this
// reader serve a bundle's manifest bytes, read out of the tree at the pinned
// SHA.
//
// It is deliberately the same TreeFetchFunc the Puller takes, wired at the same
// kind of composition point, because the implementation lives in
// internal/adapters/content/remotetree — above this package — and cannot be imported
// from here.
//
// WHEN NOT TO WIRE IT: a caller that resolves tree bundles some OTHER way must
// leave it nil, and then keeps the refusal (ErrTreeBundleUnreadable) that tells
// it to. internal/core/config does exactly that — it assembles a tree bundle from
// the tree `deps pull` INSTALLED, because a skill package needs a real
// directory on disk (bundles.Bundle.FSDir) that a fetched-into-memory tree
// cannot provide, and because verifying the installed bytes is strictly
// stronger than trusting the pin. Wiring a tree fetcher does not change that:
// every v2 pin is a tree, so config resolves every entry that way and clears
// this refusal rather than dispatching on it.
func WithReaderTreeFetcher(tf TreeFetchFunc) BundleReaderOption {
	return func(r *BundleReader) { r.treeFetch = tf }
}

// NewBundleReader constructs a reader bound to a specific lockfile snapshot.
func NewBundleReader(registry *Registry, factory FetcherFactory, auth AuthConfig, lock *Lockfile, opts ...BundleReaderOption) *BundleReader {
	r := &BundleReader{
		registry: registry,
		factory:  factory,
		auth:     auth,
		lock:     lock,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ListBundleNames returns the lockfile bundle keys ("remoteName/path"), sorted.
func (r *BundleReader) ListBundleNames() []string {
	if r == nil || r.lock == nil {
		return nil
	}
	names := make([]string, 0, len(r.lock.Bundles))
	for k := range r.lock.Bundles {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return names
}

// HasBundle reports whether bundleName is a known remote bundle in this
// reader's lockfile snapshot.
func (r *BundleReader) HasBundle(bundleName string) bool {
	if r == nil || r.lock == nil {
		return false
	}
	_, ok := r.lock.Bundles[trust.BundleKey(bundleName)]
	return ok
}

// LockEntryFor returns the lockfile entry for bundleName (or zero+false).
// Exposed so a caller can render provenance alongside the bytes.
func (r *BundleReader) LockEntryFor(bundleName string) (LockEntry, bool) {
	if r == nil || r.lock == nil {
		return LockEntry{}, false
	}
	e, ok := r.lock.Bundles[trust.BundleKey(bundleName)]
	return e, ok
}

// ReadBundleBytes returns the raw bundle YAML for bundleName at its locked SHA.
// bundleName matches lockfile keys ("remoteName/path"). Returns
// ErrBundleNotInLockfile if no entry exists.
func (r *BundleReader) ReadBundleBytes(ctx context.Context, bundleName string) ([]byte, error) {
	return r.fetchAtLockedSHA(ctx, bundleName)
}

// readableEntry returns bundleName's lockfile entry once it has established
// that this reader can honestly serve it: the entry exists, it is PINNED, and
// its published FORM is one this reader was built to read.
//
// The three refusals live together because they are one question — "may these
// bytes be read at all" — asked before any transport work happens, and because
// each of them is a case where carrying on would produce a plausible-looking
// wrong answer rather than an error.
func (r *BundleReader) readableEntry(bundleName string) (LockEntry, error) {
	if r == nil || r.lock == nil {
		return LockEntry{}, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, bundleName)
	}
	entry, ok := r.lock.Bundles[trust.BundleKey(bundleName)]
	if !ok {
		return LockEntry{}, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, bundleName)
	}
	// The pin IS the security control — EffectiveTrust's content
	// gate keys on bytes read at THIS exact commit. An empty SHA is not "no
	// preference"; every Fetcher implementation resolves "" to the default
	// branch TIP, so a blank pin (a hand-edited, truncated, or future-written
	// lockfile) would silently convert a pinned read into a latest read with
	// no error anywhere. A pinned reader must refuse to read unpinned.
	if entry.SHA == "" {
		return LockEntry{}, fmt.Errorf("bundle %q has no SHA pinned in the lockfile — refusing to read (a pinned reader must never resolve an empty ref to the latest commit)", bundleName)
	}
	// Say what is actually true: the bytes are there, and this reader was built
	// without the surface that reads them — not "remote content not found",
	// which points the user at a pull that already succeeded.
	if r.treeFetch == nil {
		return LockEntry{}, fmt.Errorf("%w: bundle %q was published in directory form and its tree is installed at the pinned SHA %s, "+
			"but this reader was constructed without a tree fetcher (see WithReaderTreeFetcher), so its content cannot be read here (do NOT re-pull; the pull worked)",
			ErrTreeBundleUnreadable, bundleName, entry.SHA)
	}
	return entry, nil
}

// fetchAtLockedSHA resolves bundleName to its repo/path/SHA and fetches its
// manifest at that SHA.
func (r *BundleReader) fetchAtLockedSHA(ctx context.Context, bundleName string) ([]byte, error) {
	entry, err := r.readableEntry(bundleName)
	if err != nil {
		return nil, err
	}

	// Lockfile keys are bundle identities; parse out the repository's fetch
	// location and the bundle path.
	ref, err := ParseReference(bundleName)
	if err != nil {
		return nil, fmt.Errorf("invalid lockfile bundle key %q: %w", bundleName, err)
	}
	if !ref.IsCanonical() {
		// err is nil here; don't wrap it with %w (that prints "%!w(<nil>)" and
		// leaves Unwrap() nil).
		return nil, fmt.Errorf("invalid lockfile bundle key %q: not a canonical ref", bundleName)
	}

	// ref.URL is guaranteed non-empty here: the IsCanonical() check above is
	// exactly `ref.URL != ""`, so there is no fallback to entry.URL to take
	// (the fallback was unreachable dead code; entry.URL is never
	// cross-checked against ref.URL, which is a separate, still-open concern
	// for whoever owns lockfile provenance validation).
	repoURL := ref.URL

	fetcher, ferr := r.factory(repoURL, r.auth)
	if ferr != nil {
		return nil, fmt.Errorf("create fetcher for %s: %w", repoURL, ferr)
	}

	owner, repo, perr := ParseOwnerRepo(repoURL)
	if perr != nil {
		return nil, fmt.Errorf("parse repo URL %s: %w", repoURL, perr)
	}

	return r.readFromTree(ctx, fetcher, owner, repo, repoURL, bundleName, ref.BuildFilePath(ref.ItemType), entry.SHA)
}

// readFromTree serves a bundle's manifest out of the tree at the pinned SHA.
//
// The candidate roots come from ProbeBundleTreeRoots, the same probe the PULL
// takes, so a reader can never look somewhere the installer did not write.
//
// # It returns the MANIFEST ONLY, and that is deliberate — do not "fix" it
//
// This looks like the truncation that made remote profile inheritance lose
// every item file (see bundles.ReadRemoteRef), and it is the same shape, but it
// is not the same defect: its consumers genuinely want manifest bytes, and
// none of them parses them expecting items.
//
//   - operations.isInstalled discards the bytes entirely and reads only whether
//     the read succeeded.
//   - config's treeBundleReaders path refuses every entry it is handed by
//     design, resolving instead off the INSTALLED tree through readTreeForm ->
//     bundles.ReadTree, which reads item files properly.
//
// A caller that wants the BUNDLE rather than its manifest must not come here at
// all; bundles.ReadRemoteRef is that path.
func (r *BundleReader) readFromTree(ctx context.Context, fetcher Fetcher, owner, repo, repoURL, bundleName, filePath, sha string) ([]byte, error) {
	tree, root, err := ProbeBundleTreeRoots(filePath, func(root string) (map[string]TreeFile, error) {
		return r.treeFetch(ctx, fetcher, owner, repo, root, sha, repoURL)
	})
	if err != nil {
		return nil, fmt.Errorf("fetch tree %s@%s: %w", root, sha, err)
	}
	manifest, ok := TreeManifest(tree)
	if !ok {
		// A tree with no manifest is not a bundle, and saying so here —
		// naming the root — is the difference between a diagnosable
		// publisher mistake and a bundle that resolves to nothing. This is
		// NOT ErrTreeBundleUnreadable: nothing about the reader is missing.
		return nil, fmt.Errorf("bundle %q: the tree at %s@%s has no %s, so it is not a bundle",
			bundleName, root, sha, BundleManifestName)
	}
	return manifest, nil
}

// LoadAllBytes reads every bundle src knows about, returning a name → bytes
// map plus a parallel name → error map for failures. Free function rather
// than a method so the caching decorator picks up the benefit transparently
// — calls to ReadBundleBytes route through whichever concrete src was
// passed in.
//
// Individual per-bundle errors do not abort — fault tolerance applies to
// the catalogue, not to one bad SHA.
func LoadAllBytes(ctx context.Context, src BundleByteSource) (loaded map[string][]byte, failures map[string]error) {
	loaded = make(map[string][]byte)
	failures = map[string]error{}
	if src == nil {
		return loaded, failures
	}
	for _, name := range src.ListBundleNames() {
		if ctx.Err() != nil {
			failures[name] = ctx.Err()
			continue
		}
		data, err := src.ReadBundleBytes(ctx, name)
		if err != nil {
			failures[name] = err
			continue
		}
		loaded[name] = data
	}
	return loaded, failures
}

// Ensure BundleReader satisfies the byte-source interface at compile time.
var _ BundleByteSource = (*BundleReader)(nil)
