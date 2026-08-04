package remote

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// captureWarnings redirects clidiag's sink to a buffer for the duration of
// the test and returns it, restoring the default sink on cleanup.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// legacyMarkerName is a plausible <sha256>.confirmed marker file name, the
// exact shape the old store wrote.
const legacyMarkerName = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855.confirmed"

// TestSweepLegacyPublishRemotesDir_RemovesAWellFormedLegacyDirectory pins the
// happy path: a directory holding nothing but legacy markers is removed, and
// the removal is announced exactly once, naming both the old path and the new
// store.
func TestSweepLegacyPublishRemotesDir_RemovesAWellFormedLegacyDirectory(t *testing.T) {
	home := testsupport.Isolate(t)
	buf := captureWarnings(t)

	const secondMarkerName = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.confirmed"
	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, legacyMarkerName), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, secondMarkerName), nil, 0o600))

	sweepLegacyPublishRemotesDir()

	_, statErr := os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(statErr), "a well-formed legacy directory must be removed")

	msg := buf.String()
	assert.Contains(t, msg, legacyDir, "the message must name the removed path")
	assert.Contains(t, msg, "publish_remotes.yaml", "the message must name the new store")
	assert.Contains(t, msg, "asked about again", "the message must say the user will be re-asked")

	// A second sweep of the (now-absent) directory is the ordinary case:
	// silent, no further warning.
	buf.Reset()
	sweepLegacyPublishRemotesDir()
	assert.Empty(t, buf.String(), "sweeping an already-absent legacy directory must be silent")
}

// TestSweepLegacyPublishRemotesDir_AbsentDirectoryIsSilent pins constraint 5:
// a legacy directory that was never there is the ordinary case, not an event.
func TestSweepLegacyPublishRemotesDir_AbsentDirectoryIsSilent(t *testing.T) {
	testsupport.Isolate(t)
	buf := captureWarnings(t)

	sweepLegacyPublishRemotesDir()

	assert.Empty(t, buf.String(), "a missing legacy directory must produce no warning at all")
}

// TestSweepLegacyPublishRemotesDir_UnresolvableHomeDoesNothing pins
// constraint 1, the sharpest one: an unresolvable $HOME must not make the
// sweep act relative to the process's working directory. It plants a legacy-
// shaped directory at that RELATIVE path (i.e. exactly where
// filepath.Join("", x) would resolve it) and asserts the sweep leaves it
// completely untouched.
func TestSweepLegacyPublishRemotesDir_UnresolvableHomeDoesNothing(t *testing.T) {
	testsupport.Isolate(t)
	cwd := t.TempDir()
	testsupport.ChangeDir(t, cwd)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	_, err := os.UserHomeDir()
	require.Error(t, err, "the fixture did not make os.UserHomeDir fail; nothing below is exercised")

	// Plant a legacy-shaped directory at the RELATIVE path an unguarded
	// filepath.Join("", x) would resolve the legacy dir to: relative to cwd,
	// exactly like AppDirName/LegacyPublishRemotesDirName under the working
	// directory the test just changed into.
	relDir := filepath.Join(cwd, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(relDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(relDir, legacyMarkerName), nil, 0o600))

	buf := captureWarnings(t)
	sweepLegacyPublishRemotesDir()

	_, statErr := os.Stat(relDir)
	require.NoError(t, statErr, "an unresolvable $HOME must NOT make the sweep delete a directory "+
		"relative to the process's working directory")
	assert.Empty(t, buf.String(), "an unresolvable $HOME produces no warning of its own here — the "+
		"identical fault already surfaces loudly at the store itself")
}

// TestSweepLegacyPublishRemotesDir_UnexpectedEntryIsLeftAlone pins constraint
// 2: any entry that is not a plain legacy marker file aborts the whole sweep,
// and warns naming the path and the unexpected entry.
func TestSweepLegacyPublishRemotesDir_UnexpectedEntryIsLeftAlone(t *testing.T) {
	home := testsupport.Isolate(t)
	buf := captureWarnings(t)

	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, legacyMarkerName), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, "notes.txt"), []byte("hi"), 0o600))

	sweepLegacyPublishRemotesDir()

	_, statErr := os.Stat(legacyDir)
	require.NoError(t, statErr, "a directory holding an unexpected entry must be left alone entirely")
	_, statErr = os.Stat(filepath.Join(legacyDir, legacyMarkerName))
	assert.NoError(t, statErr, "the legitimate marker must survive too — nothing is partially cleaned")

	msg := buf.String()
	assert.Contains(t, msg, legacyDir, "the warning must name the directory")
	assert.Contains(t, msg, "notes.txt", "the warning must name the unexpected entry")
}

// TestSweepLegacyPublishRemotesDir_SubdirectoryIsLeftAlone is the
// unexpected-entry case for a nested directory rather than a stray file.
func TestSweepLegacyPublishRemotesDir_SubdirectoryIsLeftAlone(t *testing.T) {
	home := testsupport.Isolate(t)
	buf := captureWarnings(t)

	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(filepath.Join(legacyDir, "sub"), 0o700))

	sweepLegacyPublishRemotesDir()

	_, statErr := os.Stat(legacyDir)
	require.NoError(t, statErr, "a directory holding a subdirectory must be left alone")
	assert.Contains(t, buf.String(), legacyDir)
}

// TestSweepLegacyPublishRemotesDir_SymlinkedLegacyPathIsLeftAlone pins
// constraint 3: the legacy path itself being a symlink must stop the sweep
// before it reads or removes anything, whether through the link or the
// target.
func TestSweepLegacyPublishRemotesDir_SymlinkedLegacyPathIsLeftAlone(t *testing.T) {
	home := testsupport.Isolate(t)
	buf := captureWarnings(t)

	target := filepath.Join(home, "elsewhere")
	require.NoError(t, os.MkdirAll(target, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(target, legacyMarkerName), nil, 0o600))

	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyDir), 0o700))
	require.NoError(t, os.Symlink(target, legacyDir))

	sweepLegacyPublishRemotesDir()

	// The symlink itself must survive.
	fi, err := os.Lstat(legacyDir)
	require.NoError(t, err, "the symlink must not be removed")
	assert.True(t, fi.Mode()&os.ModeSymlink != 0, "the legacy path must still be a symlink")
	// And the target it points at must survive too.
	_, err = os.Stat(target)
	require.NoError(t, err, "the symlink's target must not be removed either")
	_, err = os.Stat(filepath.Join(target, legacyMarkerName))
	require.NoError(t, err)

	assert.Contains(t, buf.String(), legacyDir, "a symlinked legacy path must warn, naming the path")
}

// TestSweepLegacyPublishRemotesDir_SymlinkedMarkerEntryIsLeftAlone extends
// constraint 3 to an entry INSIDE an otherwise-plain legacy directory: a
// symlink named like a legacy marker is still not a marker file this sweep
// wrote, and must not be treated as one.
func TestSweepLegacyPublishRemotesDir_SymlinkedMarkerEntryIsLeftAlone(t *testing.T) {
	home := testsupport.Isolate(t)
	buf := captureWarnings(t)

	outside := filepath.Join(home, "outside-marker")
	require.NoError(t, os.WriteFile(outside, nil, 0o600))

	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(legacyDir, legacyMarkerName)))

	sweepLegacyPublishRemotesDir()

	_, statErr := os.Stat(legacyDir)
	require.NoError(t, statErr, "a directory whose only entry is a symlink (even one shaped like a "+
		"marker name) must be left alone")
	assert.Contains(t, buf.String(), legacyDir)
}

// TestSweepLegacyPublishRemotesDir_NeverTouchesTheNewStore builds a fixture
// with BOTH the legacy directory and the new store file present (the exact
// state a real upgraded install is in) and asserts the sweep removes only
// the legacy directory, leaving the new store's bytes untouched.
func TestSweepLegacyPublishRemotesDir_NeverTouchesTheNewStore(t *testing.T) {
	home := testsupport.Isolate(t)
	captureWarnings(t)

	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, legacyMarkerName), nil, 0o600))

	newStorePath, err := paths.HomePublishRemotesPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(newStorePath), 0o700))
	newContent := []byte("version: 1\nrecords: []\n")
	require.NoError(t, os.WriteFile(newStorePath, newContent, 0o600))

	sweepLegacyPublishRemotesDir()

	_, statErr := os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(statErr), "the legacy directory must be gone")

	got, err := os.ReadFile(newStorePath)
	require.NoError(t, err, "the new store must still be there")
	assert.Equal(t, newContent, got, "the new store's bytes must be untouched")
}

// TestSweepLegacyPublishRemotesDir_RunsFromDefaultPublishRemoteStore is the
// hook-point test: DefaultPublishRemoteStore is where this sweep is wired in
// (see its doc comment), and it must actually fire from there, not merely
// exist as dead code.
func TestSweepLegacyPublishRemotesDir_RunsFromDefaultPublishRemoteStore(t *testing.T) {
	home := testsupport.Isolate(t)
	buf := captureWarnings(t)

	legacyDir := filepath.Join(home, paths.AppDirName, paths.LegacyPublishRemotesDirName)
	require.NoError(t, os.MkdirAll(legacyDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(legacyDir, legacyMarkerName), nil, 0o600))

	store := DefaultPublishRemoteStore()
	require.NotNil(t, store)

	_, statErr := os.Stat(legacyDir)
	assert.True(t, os.IsNotExist(statErr), "constructing the production store must trigger the sweep")
	assert.Contains(t, buf.String(), legacyDir)
}
