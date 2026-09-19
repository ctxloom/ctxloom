package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Clear used to discard os.Remove's error and always return nil while
// unconditionally zeroing contextHash. That made a failed cleanup both invisible
// (the ContextProvider interface declares an error return that was never used)
// and UNRECOVERABLE: the hash naming the file still on disk was gone, so nothing
// could retry or even report which file leaked. Clear now reports the failure
// and keeps the hash so a retry is possible.
func TestBaseContextProvider_ClearReportsRemovalFailureAndKeepsTheHash(t *testing.T) {
	work := t.TempDir()
	p := NewBaseContextProvider()
	require.NoError(t, p.Provide(work, []*Fragment{{Content: "project rules"}}))

	hash := p.GetContextHash()
	require.NotEmpty(t, hash)
	path := filepath.Join(work, SCMContextSubdir, hash+".md")
	require.FileExists(t, path)

	// Make the cache path un-removable in a way that does not depend on file
	// permissions (this suite may run as root): replace it with a NON-EMPTY
	// directory, which os.Remove refuses with ENOTEMPTY.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "occupant"), []byte("x"), 0o644))

	// The fixture must be hostile from Clear's point of view BEFORE anything
	// about Clear is asserted — otherwise a green run proves nothing.
	require.Error(t, os.Remove(path), "the fixture must genuinely refuse removal")

	err := p.Clear(work)
	require.Error(t, err, "a cleanup that did not happen must not be reported as success")
	assert.Contains(t, err.Error(), hash, "the error must name the file that leaked")
	assert.Equal(t, hash, p.GetContextHash(),
		"the hash is the only handle on the leaked file — clearing it makes the failure unrecoverable")

	// Once the obstruction is gone the retry the preserved hash enables succeeds.
	require.NoError(t, os.Remove(filepath.Join(path, "occupant")))
	require.NoError(t, p.Clear(work))
	assert.Empty(t, p.GetContextHash(), "a successful Clear releases the hash")
}

// An already-absent context file is a SUCCESSFUL clear, not a failure: Clear's
// job is to leave nothing behind, and there is nothing behind. The hash is
// released.
func TestBaseContextProvider_ClearIsIdempotentWhenTheFileIsAlreadyGone(t *testing.T) {
	work := t.TempDir()
	p := NewBaseContextProvider()
	require.NoError(t, p.Provide(work, []*Fragment{{Content: "project rules"}}))

	hash := p.GetContextHash()
	require.NoError(t, os.Remove(filepath.Join(work, SCMContextSubdir, hash+".md")))

	require.NoError(t, p.Clear(work), "an already-absent file is not a cleanup failure")
	assert.Empty(t, p.GetContextHash())

	// And with no hash at all Clear is a no-op that still succeeds.
	require.NoError(t, p.Clear(work))
}
