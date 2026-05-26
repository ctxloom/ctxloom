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

// Inner returns the wrapped source, primarily for tests that want to
// reach past the cache and assert on the underlying call count.
func (c *CachingBundleReader) Inner() BundleByteSource { return c.inner }

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
func (c *CachingBundleReader) ReadBundleBytes(ctx context.Context, name string) ([]byte, error) {
	if c == nil || c.inner == nil {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, name)
	}

	entry, ok := c.inner.LockEntryFor(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBundleNotInLockfile, name)
	}
	key := bundleCacheKey{name: name, sha: entry.SHA}

	c.mu.RLock()
	if cached, ok := c.cache[key]; ok {
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	data, err := c.inner.ReadBundleBytes(ctx, name)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[key] = data
	c.mu.Unlock()
	return data, nil
}

// Ensure the decorator still satisfies BundleByteSource — that's the
// whole point of decorating.
var _ BundleByteSource = (*CachingBundleReader)(nil)
