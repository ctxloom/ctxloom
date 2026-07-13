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
