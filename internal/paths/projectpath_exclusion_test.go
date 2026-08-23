//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

// The mapping is only worth anything if the file it names is the file that
// actually excludes, ACROSS SPELLINGS — the equality assertion in
// projectpath_test.go proves two strings match, this proves the flock does.
//
// The probe is a non-blocking flock(2) rather than a second blocking Lock:
// a real acquisition on held is deliberately blocking, so probing with it
// would park instead of answering.
func TestProjectPathFor_LockTakenViaOneSpellingExcludesTheOther(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, AppDirName)
	protected := filepath.Join(appDir, "config.yaml")
	otherSpelling := filepath.Join(appDir, "content", "..", "config.yaml")
	unrelated := filepath.Join(appDir, "remotes.yaml")

	held, err := ProjectPathFor(protected)
	require.NoError(t, err)
	viaOther, err := ProjectPathFor(otherSpelling)
	require.NoError(t, err)
	viaUnrelated, err := ProjectPathFor(unrelated)
	require.NoError(t, err)

	// flock.New does not create the lock's parent directory the way the old
	// filelock.Lock's internal ensureDir used to — every real call site
	// creates it itself before acquiring (see e.g. config.Manager.Update),
	// so the probe does the same here.
	require.NoError(t, os.MkdirAll(filepath.Dir(held), 0o755))
	heldLock := flock.New(held)
	require.NoError(t, heldLock.Lock())

	require.False(t, lockFree(t, viaOther),
		"a second writer spelling the same file differently did not exclude the first")
	require.True(t, lockFree(t, viaUnrelated),
		"locking one file blocked an unrelated one")

	require.NoError(t, heldLock.Unlock())
	require.True(t, lockFree(t, held), "the lock was not released")
}
