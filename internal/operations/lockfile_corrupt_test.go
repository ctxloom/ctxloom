package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// U085-F01, the same defect from the READ side. LockDependencies rebuilds the
// whole lockfile from the closure and carries the two things the closure
// cannot know — the user's holds (Pinned) and the publisher's retractions
// (Retracted) — forward by reading the PREVIOUS lockfile. When that read
// failed it degraded the previous lockfile to an empty one and carried nothing
// forward, then saved the result over the corrupt file: unparseable became
// empty, empty got written back, and every hold and retraction was gone. A
// silently un-retracted bundle is content the publisher withdrew being served
// to an agent again.
//
// The fix is the same guard as the empty-write case: Save reads back what is
// on disk and refuses to overwrite what it cannot parse.
func TestLockDependencies_CorruptLockfileIsNotOverwritten(t *testing.T) {
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	lockPath := remote.NewLockfileManager(tmp).Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	// Valid-looking but unparseable: this is what a truncated or
	// half-written lock.yaml looks like.
	corrupt := []byte("version: 1\nbundles:\n  https://github.com/test/repo@bundles/demo:\n    sha: \"abc\n    pinned: true\n")
	require.NoError(t, os.WriteFile(lockPath, corrupt, 0o644))

	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.Error(t, err, "a rebuild that could not read the previous holds/retractions must not persist")
	assert.ErrorIs(t, err, remote.ErrLockfileUnreadable)
	assert.Contains(t, err.Error(), lockPath, "the error names the file to fix")

	after, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	assert.Equal(t, string(corrupt), string(after),
		"the corrupt lockfile is left intact — its holds and retractions are still recoverable by hand")
}

// The recovery path the error message points at: delete the unreadable file
// and the very next lock rebuild succeeds. A refusal that cannot be recovered
// from would be a wedge, not a guard.
func TestLockDependencies_DeletingTheCorruptLockfileRecovers(t *testing.T) {
	tmp := t.TempDir()
	writeLocalProfile(t, tmp, "default",
		"bundles:\n  - https://github.com/test/repo@bundles/demo@abc123def456\n")
	cfg := testConfigWithSCMPath(tmp)

	lockPath := remote.NewLockfileManager(tmp).Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	require.NoError(t, os.WriteFile(lockPath, []byte("\tnot: [yaml\n"), 0o644))

	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.Error(t, err)

	require.NoError(t, os.Remove(lockPath))

	result, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err, "removing the unreadable lockfile restores a working rebuild")
	assert.Equal(t, "generated", result.Status)
	assert.Equal(t, 1, result.ItemCount)
}
