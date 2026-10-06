package remote

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// A lockfile carries pins and holds, nothing about who signed or withdrew the
// content. One written by a build that recorded a version floor and a
// retraction verdict still loads, keeping its pin and hold, and the next save
// writes none of those keys back.
func TestLockfile_TrustKeysAreNotPartOfTheFormat(t *testing.T) {
	body := lockBody(schemaver.Key, LockfileVersion) +
		"    held: true\n" +
		"    signed_version: 2.0.0\n" +
		"    publisher: alice@example.test\n" +
		"    retracted: true\n" +
		"    retracted_reason: leaked token\n" +
		"    retraction_checked_at: 2026-01-01T00:00:00Z\n"
	lm := lockWithBody(t, body)

	lock, err := lm.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(ItemTypeBundle, schemaverLockKey)
	require.True(t, ok)
	assert.Equal(t, "abc123", entry.SHA)
	assert.True(t, entry.Held)

	require.NoError(t, lm.Save(lock))
	onDisk, err := afero.ReadFile(lm.FS(), lm.Path())
	require.NoError(t, err)
	for _, key := range []string{"signed_version", "publisher", "retracted", "retraction_checked_at"} {
		assert.NotContains(t, string(onDisk), key+":")
	}
	assert.Contains(t, string(onDisk), "held: true")
}
