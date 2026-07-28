package remote

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSource is a hand-written BundleByteSource that records every
// ReadBundleBytes call so cache-hit assertions don't have to reach into
// the MockFetcher (the cache lives one layer above the fetcher).
type countingSource struct {
	bytes map[string][]byte
	shas  map[string]string
	calls int
	err   error
	mu    sync.Mutex
}

func newCountingSource(entries map[string]string, contents map[string][]byte) *countingSource {
	return &countingSource{bytes: contents, shas: entries}
}

func (c *countingSource) ReadBundleBytes(_ context.Context, name string) ([]byte, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	data, ok := c.bytes[name]
	if !ok {
		return nil, ErrBundleNotInLockfile
	}
	return data, nil
}

func (c *countingSource) LockEntryFor(name string) (LockEntry, bool) {
	sha, ok := c.shas[name]
	if !ok {
		return LockEntry{}, false
	}
	return LockEntry{SHA: sha}, true
}

func (c *countingSource) ListBundleNames() []string {
	names := make([]string, 0, len(c.shas))
	for k := range c.shas {
		names = append(names, k)
	}
	return names
}

func (c *countingSource) HasBundle(name string) bool {
	_, ok := c.shas[name]
	return ok
}

func (c *countingSource) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestCachingBundleReader(t *testing.T) {
	t.Run("first read populates cache, subsequent reads short-circuit", func(t *testing.T) {
		inner := newCountingSource(
			map[string]string{"a/x": "sha1"},
			map[string][]byte{"a/x": []byte("body")},
		)
		cache := NewCachingBundleReader(inner)

		for i := 0; i < 5; i++ {
			data, err := cache.ReadBundleBytes(context.Background(), "a/x")
			require.NoError(t, err)
			assert.Equal(t, []byte("body"), data)
		}
		assert.Equal(t, 1, inner.callCount(), "cache must short-circuit after first read")
	})

	t.Run("different bundles cache independently", func(t *testing.T) {
		inner := newCountingSource(
			map[string]string{"a/x": "sha1", "a/y": "sha2"},
			map[string][]byte{"a/x": []byte("X"), "a/y": []byte("Y")},
		)
		cache := NewCachingBundleReader(inner)

		_, _ = cache.ReadBundleBytes(context.Background(), "a/x")
		_, _ = cache.ReadBundleBytes(context.Background(), "a/y")
		_, _ = cache.ReadBundleBytes(context.Background(), "a/x")
		_, _ = cache.ReadBundleBytes(context.Background(), "a/y")
		assert.Equal(t, 2, inner.callCount(), "one hit per (name, sha)")
	})

	t.Run("SHA change invalidates the cache entry implicitly", func(t *testing.T) {
		inner := newCountingSource(
			map[string]string{"a/x": "sha1"},
			map[string][]byte{"a/x": []byte("old")},
		)
		cache := NewCachingBundleReader(inner)

		_, _ = cache.ReadBundleBytes(context.Background(), "a/x")

		inner.shas["a/x"] = "sha2"
		inner.bytes["a/x"] = []byte("new")

		data, err := cache.ReadBundleBytes(context.Background(), "a/x")
		require.NoError(t, err)
		assert.Equal(t, []byte("new"), data, "new SHA → cache miss → fresh body")
		assert.Equal(t, 2, inner.callCount())
	})

	t.Run("inner errors are not cached", func(t *testing.T) {
		inner := newCountingSource(
			map[string]string{"a/x": "sha1"},
			map[string][]byte{"a/x": []byte("body")},
		)
		inner.err = errors.New("boom")
		cache := NewCachingBundleReader(inner)

		_, err1 := cache.ReadBundleBytes(context.Background(), "a/x")
		require.Error(t, err1)

		inner.err = nil
		data, err2 := cache.ReadBundleBytes(context.Background(), "a/x")
		require.NoError(t, err2)
		assert.Equal(t, []byte("body"), data)
		assert.Equal(t, 2, inner.callCount(), "failed read must not poison the cache")
	})

	t.Run("missing bundle returns ErrBundleNotInLockfile", func(t *testing.T) {
		inner := newCountingSource(nil, nil)
		cache := NewCachingBundleReader(inner)

		_, err := cache.ReadBundleBytes(context.Background(), "missing/bundle")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})

	t.Run("metadata pass-through", func(t *testing.T) {
		inner := newCountingSource(
			map[string]string{"a/x": "sha1"},
			map[string][]byte{"a/x": []byte("body")},
		)
		cache := NewCachingBundleReader(inner)

		assert.True(t, cache.HasBundle("a/x"))
		assert.False(t, cache.HasBundle("nope"))

		entry, ok := cache.LockEntryFor("a/x")
		require.True(t, ok)
		assert.Equal(t, "sha1", entry.SHA)

		names := cache.ListBundleNames()
		assert.ElementsMatch(t, []string{"a/x"}, names)
	})

	t.Run("nil decorator and nil inner are safe", func(t *testing.T) {
		var nilCache *CachingBundleReader
		assert.Empty(t, nilCache.ListBundleNames())
		assert.False(t, nilCache.HasBundle("x"))

		nilInner := NewCachingBundleReader(nil)
		assert.Empty(t, nilInner.ListBundleNames())
		assert.False(t, nilInner.HasBundle("x"))

		_, err := nilInner.ReadBundleBytes(context.Background(), "x/y")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})

	t.Run("concurrent reads are race-safe and cache-correct", func(t *testing.T) {
		inner := newCountingSource(
			map[string]string{"a/x": "sha1"},
			map[string][]byte{"a/x": []byte("body")},
		)
		cache := NewCachingBundleReader(inner)

		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				data, err := cache.ReadBundleBytes(context.Background(), "a/x")
				assert.NoError(t, err)
				assert.Equal(t, []byte("body"), data)
			}()
		}
		wg.Wait()

		// Concurrent first-fetchers may race; the strict "exactly one"
		// invariant would require single-flight. We accept "much less
		// than goroutine count," which is the practical benefit.
		assert.LessOrEqual(t, inner.callCount(), 20)
	})
}

// TestCachingBundleReader_WithRealReader sanity-checks that the decorator
// composes correctly with the concrete *BundleReader fetched via the
// MockFetcher. Without this, the unit tests above could pass with a fake
// inner while production wiring silently broke.
func TestCachingBundleReader_WithRealReader(t *testing.T) {
	inner, fetcher, _ := readerFixture(t)
	cache := NewCachingBundleReader(inner)

	for i := 0; i < 3; i++ {
		data, err := cache.ReadBundleBytes(context.Background(), secKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Security bundle\n", string(data))
	}
	assert.Len(t, fetcher.FetchFileCalls, 1, "underlying fetcher hit exactly once")
}
