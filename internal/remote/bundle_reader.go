package remote

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrBundleNotInLockfile is returned when a caller asks for a bundle that
// has no lockfile entry. Read sites use this to distinguish "not a remote
// bundle, fall back to local fs lookup" from real errors.
var ErrBundleNotInLockfile = errors.New("bundle not in lockfile")

// BundleByteSource is the read surface every bundle-byte producer must
// satisfy. BundleReader is the canonical implementation (git-cache-backed,
// SHA-pinned); CachingBundleReader is the in-memory decorator. Callers that
// need either form (operations.SeededBundlesFrom, show_bundle_verbatim, …)
// program against this interface.
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
// YAML structure. Callers in higher layers parse via internal/bundles.
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
}

// NewBundleReader constructs a reader bound to a specific lockfile snapshot.
func NewBundleReader(registry *Registry, factory FetcherFactory, auth AuthConfig, lock *Lockfile) *BundleReader {
	return &BundleReader{
		registry: registry,
		factory:  factory,
		auth:     auth,
		lock:     lock,
	}
}

// ListBundleNames returns the lockfile bundle keys ("remoteName/path"), sorted.
func (r *BundleReader) ListBundleNames() []string {
	if r == nil || r.lock == nil {
		return nil
	}
	names := make([]string, 0, len(r.lock.Bundles))
	for k := range r.lock.Bundles {
		names = append(names, k)
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
	_, ok := r.lock.Bundles[bundleName]
	return ok
}

// LockEntryFor returns the lockfile entry for bundleName (or zero+false).
// Exposed so callers (e.g. show_bundle_verbatim) can render provenance
// alongside the bytes.
func (r *BundleReader) LockEntryFor(bundleName string) (LockEntry, bool) {
	if r == nil || r.lock == nil {
		return LockEntry{}, false
	}
	e, ok := r.lock.Bundles[bundleName]
	return e, ok
}

// ReadBundleBytes returns the raw bundle YAML for bundleName at its locked SHA.
// bundleName matches lockfile keys ("remoteName/path"). Returns
// ErrBundleNotInLockfile if no entry exists.
func (r *BundleReader) ReadBundleBytes(ctx context.Context, bundleName string) ([]byte, error) {
	if r == nil || r.lock == nil {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, bundleName)
	}
	entry, ok := r.lock.Bundles[bundleName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, bundleName)
	}

	// Lockfile keys are canonical refs ("<url>@bundles/<path>"); parse out the
	// repo URL and item path.
	ref, err := ParseReference(bundleName)
	if err != nil {
		return nil, fmt.Errorf("invalid lockfile bundle key %q: %w", bundleName, err)
	}
	if !ref.IsCanonical() {
		// err is nil here; don't wrap it with %w (that prints "%!w(<nil>)" and
		// leaves Unwrap() nil).
		return nil, fmt.Errorf("invalid lockfile bundle key %q: not a canonical ref", bundleName)
	}

	repoURL := ref.URL
	if repoURL == "" {
		repoURL = entry.URL
	}

	fetcher, ferr := r.factory(repoURL, r.auth)
	if ferr != nil {
		return nil, fmt.Errorf("create fetcher for %s: %w", repoURL, ferr)
	}

	owner, repo, perr := ParseRepoURL(repoURL)
	if perr != nil {
		return nil, fmt.Errorf("parse repo URL %s: %w", repoURL, perr)
	}

	filePath := ref.BuildFilePath(ref.ItemType)

	data, err := fetcher.FetchFile(ctx, owner, repo, filePath, entry.SHA)
	if err != nil {
		return nil, fmt.Errorf("fetch %s@%s: %w", filePath, entry.SHA, err)
	}
	return data, nil
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
