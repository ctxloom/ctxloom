package remote

import (
	"context"
	"fmt"
	"sync"
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
	return c.readThrough(ctx, name, c.inner.ReadBundleBytes)
}

// readThrough serves (name, sha) from the cache, falling through to read
// and memoizing the result. An entry with no locked SHA is read but never
// stored.
func (c *CachingBundleReader) readThrough(
	ctx context.Context, name string,
	read func(context.Context, string) ([]byte, error),
) ([]byte, error) {
	entry, pinned := c.inner.LockEntryFor(name)
	key := bundleCacheKey{name: name, sha: entry.SHA}
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

// Ensure the decorator still satisfies BundleByteSource — that's the
// whole point of decorating.
var _ BundleByteSource = (*CachingBundleReader)(nil)
