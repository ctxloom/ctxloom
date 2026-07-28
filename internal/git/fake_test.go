package git

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFake_LogSince(t *testing.T) {
	entries := []LogEntry{
		{SHA: "sha2", Date: time.Now(), Subject: "second", Files: []string{"b.txt"}},
		{SHA: "sha1", Date: time.Now(), Subject: "first", Files: []string{"a.txt"}},
	}

	t.Run("returns configured entries for the exact dir", func(t *testing.T) {
		f := &Fake{LogEntries: map[string][]LogEntry{"/repo": entries}}
		got, err := f.LogSince(context.Background(), "/repo", time.Time{}, 0)
		require.NoError(t, err)
		assert.Equal(t, entries, got)
	})

	t.Run("falls back to the default entry for an unconfigured dir", func(t *testing.T) {
		f := &Fake{LogEntries: map[string][]LogEntry{"": entries}}
		got, err := f.LogSince(context.Background(), "/anywhere", time.Time{}, 0)
		require.NoError(t, err)
		assert.Equal(t, entries, got)
	})

	t.Run("maxEntries truncates", func(t *testing.T) {
		f := &Fake{LogEntries: map[string][]LogEntry{"/repo": entries}}
		got, err := f.LogSince(context.Background(), "/repo", time.Time{}, 1)
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Equal(t, "sha2", got[0].SHA)
	})

	t.Run("LogErr is returned instead of entries", func(t *testing.T) {
		f := &Fake{LogErr: assert.AnError}
		_, err := f.LogSince(context.Background(), "/repo", time.Time{}, 0)
		assert.ErrorIs(t, err, assert.AnError)
	})

	t.Run("unconfigured dir with no default returns empty, not nil-panicking", func(t *testing.T) {
		f := &Fake{}
		got, err := f.LogSince(context.Background(), "/nowhere", time.Time{}, 0)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}


// TestFake_IsDirty_DirtyErr pins U053-F04: without an error injector,
// Fake.IsDirty can never return an error, so the fail-closed unknown-state
// guard in isolation's teardown (`if derr != nil || dirty`) has no possible
// unit test — a regression deleting that guard's error half would be caught
// by nothing.
func TestFake_IsDirty_DirtyErr(t *testing.T) {
	f := &Fake{DirtyErr: assert.AnError}
	dirty, err := f.IsDirty(context.Background(), "/repo")
	assert.ErrorIs(t, err, assert.AnError)
	assert.False(t, dirty)
}

// TestFake_ApplyPatch_ReportsApplied pins U053-F03's Fake side: the fake must
// reproduce the same applied/not-applied distinction the real implementation
// now makes, so a test double never lets an empty-patch no-op look identical
// to a real apply.
func TestFake_ApplyPatch_ReportsApplied(t *testing.T) {
	f := &Fake{}
	applied, err := f.ApplyPatch(context.Background(), "/repo", "")
	require.NoError(t, err)
	assert.False(t, applied)

	applied, err = f.ApplyPatch(context.Background(), "/repo", "diff --git a/x b/x")
	require.NoError(t, err)
	assert.True(t, applied)
}

// TestFake_HasIgnoredContent pins the Fake side of U053-F01/U054-F01: a test
// exercising teardown's ignored-content refusal needs a way to configure it
// without a real git binary.
func TestFake_HasIgnoredContent(t *testing.T) {
	f := &Fake{IgnoredContent: map[string]bool{"/repo": true}}
	has, err := f.HasIgnoredContent(context.Background(), "/repo")
	require.NoError(t, err)
	assert.True(t, has)

	has, err = f.HasIgnoredContent(context.Background(), "/elsewhere")
	require.NoError(t, err)
	assert.False(t, has)
}
