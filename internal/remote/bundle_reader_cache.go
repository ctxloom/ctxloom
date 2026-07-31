package remote

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// CachingBundleReader is the read-through cache decorator for any
// BundleByteSource. It memoizes ReadBundleBytes by (name, sha) so the
// underlying source — typically a *BundleReader walking the git clone
// cache — is hit at most once per pair within a session.
//
// Metadata methods (LockEntryFor, ListBundleNames, HasBundle) pass through
// to the inner source unmodified; only ReadBundleBytes is decorated. The
// cache key includes SHA, so a lockfile update that changes a bundle's SHA
// naturally invalidates the entry without explicit eviction — the next
// read sees a new key and falls through to the inner source.
//
// CachingBundleReader is safe for concurrent use. Multiple goroutines may
// race the same first-fetch; subsequent reads of either are served from
// the populated cache.
type CachingBundleReader struct {
	inner BundleByteSource
	mu    sync.RWMutex
	cache map[bundleCacheKey][]byte
}

type bundleCacheKey struct {
	name string
	sha  string
	// sig distinguishes a bundle's detached signature from its bytes, so the
	// two can never be served for one another out of the same cache slot.
	sig bool
}

// NewCachingBundleReader wraps src so subsequent reads of the same (name,
// sha) pair are served from memory. Passing a nil inner is a programming
// error (the decorator has nothing to wrap) but won't crash — every
// method falls through to a zero return.
func NewCachingBundleReader(src BundleByteSource) *CachingBundleReader {
	return &CachingBundleReader{
		inner: src,
		cache: map[bundleCacheKey][]byte{},
	}
}

func (c *CachingBundleReader) ListBundleNames() []string {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.ListBundleNames()
}

func (c *CachingBundleReader) HasBundle(name string) bool {
	if c == nil || c.inner == nil {
		return false
	}
	return c.inner.HasBundle(name)
}

func (c *CachingBundleReader) LockEntryFor(name string) (LockEntry, bool) {
	if c == nil || c.inner == nil {
		return LockEntry{}, false
	}
	return c.inner.LockEntryFor(name)
}

// ReadBundleBytes checks the cache first; on miss, fetches from the inner
// source and stores the result keyed by (name, sha). The cache key is
// derived from the inner source's view of the lockfile, so SHA changes
// invalidate previous cache entries automatically.
//
// Membership is HasBundle's question, not LockEntryFor's: BundleByteSource
// documents LockEntryFor as returning zero+false, so a source with no lockfile
// behind it is legitimate and must stay readable through the decorator. The
// lock entry supplies only the cache KEY — with no locked SHA there is nothing
// safe to key on, so such a read passes through uncached rather than risking a
// stale slot.
func (c *CachingBundleReader) ReadBundleBytes(ctx context.Context, name string) ([]byte, error) {
	if c == nil || c.inner == nil {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, name)
	}
	if !c.inner.HasBundle(name) {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, name)
	}
	return c.readThrough(ctx, name, false, c.inner.ReadBundleBytes)
}

// readThrough serves (name, sha, sig) from the cache, falling through to read
// and memoizing the result. An entry with no locked SHA is read but never
// stored.
func (c *CachingBundleReader) readThrough(
	ctx context.Context, name string, sig bool,
	read func(context.Context, string) ([]byte, error),
) ([]byte, error) {
	entry, pinned := c.inner.LockEntryFor(name)
	key := bundleCacheKey{name: name, sha: entry.SHA, sig: sig}
	pinned = pinned && entry.SHA != ""

	if pinned {
		c.mu.RLock()
		cached, hit := c.cache[key]
		c.mu.RUnlock()
		if hit {
			return cached, nil
		}
	}

	data, err := read(ctx, name)
	if err != nil {
		return nil, err
	}

	if pinned {
		c.mu.Lock()
		c.cache[key] = data
		c.mu.Unlock()
	}
	return data, nil
}

// ErrNoSignatureSurface reports that the wrapped source cannot serve detached
// signatures at all, as distinct from a source that can and found none.
//
// The two are the same OUTCOME — unsigned content — and errors carrying this
// also wrap errs.ErrRemoteContentNotFound so every caller keeps behaving
// identically. They are not the same FACT. An absent .sig is ordinary and
// legal (spec §4.1, §10.1); a source with no signature surface is a wiring
// mistake, since every production source implements BundleSignatureSource, and
// it silently means nothing under this reader can ever be signed. Without a
// name for it, that mistake presented as a repository full of unsigned
// bundles.
var ErrNoSignatureSurface = errors.New("this source cannot serve detached signatures")

// ReadBundleSignature forwards to the inner source when it can serve detached
// signatures, memoizing successes by (name, sha, sig) exactly like the bytes.
//
// An inner source that does NOT implement BundleSignatureSource reports
// not-found rather than erroring: a source with no signature surface serves
// unsigned content, which is legal and ordinary (spec §10.1). It additionally
// wraps ErrNoSignatureSurface so the capability gap remains distinguishable
// from ordinary unsigned content. Failures — the not-found included — are
// never cached, so they cost one tree lookup each and can never pin a bundle
// to "unsigned" for the life of the process.
func (c *CachingBundleReader) ReadBundleSignature(ctx context.Context, name string) ([]byte, error) {
	if c == nil || c.inner == nil {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, name)
	}
	src, ok := c.inner.(BundleSignatureSource)
	if !ok {
		return nil, fmt.Errorf("%w for %s: the wrapped source (%T) serves bytes only, so nothing here can be signed: %w",
			ErrNoSignatureSurface, name, c.inner, errs.ErrRemoteContentNotFound)
	}
	if !c.inner.HasBundle(name) {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, name)
	}
	return c.readThrough(ctx, name, true, src.ReadBundleSignature)
}

// Ensure the decorator still satisfies BundleByteSource — that's the
// whole point of decorating — and passes the signature surface through.
var (
	_ BundleByteSource      = (*CachingBundleReader)(nil)
	_ BundleSignatureSource = (*CachingBundleReader)(nil)
	_ BundleSignatureSource = (*BundleReader)(nil)
)
