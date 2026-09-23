package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/release"
)

// floorRepo is a repository whose demo bundle was signed at firstVersion, a
// project that trusts the signer and depends on it, and a lockfile pinning that
// first commit with its signed version recorded as the floor — the state a
// verified pull leaves behind.
func floorRepo(t *testing.T, firstVersion string) (baseDir, src, ref string, signer ssh.Signer, first string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src = filepath.Join(tmp, "src")
	signer = testSigner(t)
	first = commitTree(t, src, demoTreeFilesAt(t, signer, "v1\n", firstVersion), true)
	trustPublisher(t, baseDir, signer)
	ref = "file://" + src + "@bundles/demo"
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")

	cfg := testConfigWithSCMPath(baseDir)
	_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	lm := remote.NewLockfileManager(baseDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	e, ok := lock.GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok)
	require.Equal(t, first, e.SHA)
	e.SignedVersion, e.Publisher = firstVersion, "publisher@example.com"
	lock.AddEntry(remote.ItemTypeBundle, ref, e)
	require.NoError(t, lm.Save(lock))
	return baseDir, src, ref, signer, first
}

func floorEntry(t *testing.T, baseDir, ref string) remote.LockEntry {
	t.Helper()
	e, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok)
	return e
}

// Attack (a) through `deps upgrade`: the branch tip is moved to a tree the
// publisher really signed, at an older version. The signature verifies; the
// floor refuses it, and the pin stays on the release it had.
func TestUpgrade_ARollbackToAnOlderSignedReleaseIsRefused(t *testing.T) {
	baseDir, src, ref, signer, first := floorRepo(t, "1.2.0")
	cfg := testConfigWithSCMPath(baseDir)
	commitTree(t, src, demoTreeFilesAt(t, signer, "old\n", "1.1.0"), false)

	res, err := UpgradeDependencies(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.Len(t, res.Refused, 1)
	assert.Contains(t, res.Refused[0].Detail, "below")
	got := floorEntry(t, baseDir, ref)
	assert.Equal(t, first, got.SHA)
	assert.Equal(t, "1.2.0", got.SignedVersion)
}

func TestUpgrade_AllowDowngradeNamingTheRefMovesItAndLowersTheFloor(t *testing.T) {
	baseDir, src, ref, signer, _ := floorRepo(t, "1.2.0")
	cfg := testConfigWithSCMPath(baseDir)
	older := commitTree(t, src, demoTreeFilesAt(t, signer, "old\n", "1.1.0"), false)

	res, err := UpgradeDependencies(context.Background(), cfg, []string{ref})
	require.NoError(t, err)
	assert.Empty(t, res.Refused)
	got := floorEntry(t, baseDir, ref)
	assert.Equal(t, older, got.SHA)
	assert.Equal(t, "1.1.0", got.SignedVersion)
}

func TestUpgrade_AllowDowngradeNamingAnotherRefDoesNotApply(t *testing.T) {
	baseDir, src, ref, signer, first := floorRepo(t, "1.2.0")
	cfg := testConfigWithSCMPath(baseDir)
	commitTree(t, src, demoTreeFilesAt(t, signer, "old\n", "1.1.0"), false)

	res, err := UpgradeDependencies(context.Background(), cfg, []string{"https://example.test/other@bundles/x"})
	require.NoError(t, err)
	require.Len(t, res.Refused, 1)
	assert.Equal(t, first, floorEntry(t, baseDir, ref).SHA)
}

func TestUpgrade_AForwardMoveRecordsTheNewFloorAndPublisher(t *testing.T) {
	baseDir, src, ref, signer, _ := floorRepo(t, "1.2.0")
	cfg := testConfigWithSCMPath(baseDir)
	newer := commitTree(t, src, demoTreeFilesAt(t, signer, "new\n", "1.3.0"), false)

	res, err := UpgradeDependencies(context.Background(), cfg, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Refused)
	got := floorEntry(t, baseDir, ref)
	assert.Equal(t, newer, got.SHA)
	assert.Equal(t, "1.3.0", got.SignedVersion)
	assert.Equal(t, "publisher@example.com", got.Publisher)
}

func TestUpgrade_StrippingTheSignatureDoesNotEscapeTheFloor(t *testing.T) {
	baseDir, src, ref, _, first := floorRepo(t, "1.2.0")
	cfg := testConfigWithSCMPath(baseDir)
	commitTree(t, src, demoTreeFilesAt(t, nil, "unsigned\n", "9.0.0"), false)

	res, err := UpgradeDependencies(context.Background(), cfg, nil)
	require.NoError(t, err)
	require.Len(t, res.Refused, 1)
	assert.Contains(t, res.Refused[0].Detail, "no longer signed")
	assert.Equal(t, first, floorEntry(t, baseDir, ref).SHA)
}

func TestLockDependencies_CarriesTheFloorForward(t *testing.T) {
	baseDir, _, ref, _, _ := floorRepo(t, "1.2.0")
	_, err := LockDependencies(context.Background(), testConfigWithSCMPath(baseDir), LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	got := floorEntry(t, baseDir, ref)
	assert.Equal(t, "1.2.0", got.SignedVersion)
	assert.Equal(t, "publisher@example.com", got.Publisher)
}

// A relock whose closure moves a pin (here: the profile now names the older
// commit outright) is a writer of LockEntry.SHA like any other, and the floor
// applies to it.
func TestLockDependencies_RefusesToMoveAPinBelowItsFloor(t *testing.T) {
	baseDir, src, ref, signer, first := floorRepo(t, "1.2.0")
	older := commitTree(t, src, demoTreeFilesAt(t, signer, "old\n", "1.1.0"), false)
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"@"+older+"\n")
	// A relock reads the clone cache as it stands; sync refreshes it first, and
	// so does this test, so the older commit is actually readable and the
	// refusal is the rollback rather than "not found, so not signed".
	refreshRepoCaches(context.Background(), NewRepoCache(testConfigWithSCMPath(baseDir)), []string{"file://" + src})

	_, err := LockDependencies(context.Background(), testConfigWithSCMPath(baseDir), LockDependenciesRequest{FailOnConflict: true})
	require.Error(t, err)
	assert.True(t, errors.Is(err, release.ErrRollback), "got %v", err)

	_, err = LockDependencies(context.Background(), testConfigWithSCMPath(baseDir), LockDependenciesRequest{})
	require.NoError(t, err, "the startup relock never blocks: it keeps the pin and warns")
	got := floorEntry(t, baseDir, ref)
	assert.Equal(t, first, got.SHA)
	assert.Equal(t, "1.2.0", got.SignedVersion)
}

// RULED: a lockfile that cannot be read may hold version floors, and a rebuild
// that started from an empty lock would drop every one of them.
func TestLockDependencies_AnUnreadableLockfileFailsRatherThanStartingEmpty(t *testing.T) {
	baseDir, _, _, _, _ := floorRepo(t, "1.2.0")
	lockPath := remote.NewLockfileManager(baseDir).Path()
	require.NoError(t, os.WriteFile(lockPath, []byte("bundles: [this is not a map\n"), 0o644))

	_, err := LockDependencies(context.Background(), testConfigWithSCMPath(baseDir), LockDependenciesRequest{})
	require.Error(t, err)
}

func TestDowngradeSet_IsScopedToNamedRefs(t *testing.T) {
	_, err := newDowngradeSet([]string{"not-a-ref"})
	require.Error(t, err, "a name that cannot match any pin must not pass silently")

	d, err := newDowngradeSet([]string{"https://example.test/r@bundles/a"})
	require.NoError(t, err)
	assert.True(t, d.allows("https://example.test/r@bundles/a"))
	assert.True(t, d.allows("https://example.test/r@bundles/a@v1.2.0"), "the same ref at a version is the same pin")
	assert.False(t, d.allows("https://example.test/r@bundles/b"))

	var none downgradeSet
	assert.False(t, none.allows("https://example.test/r@bundles/a"), "no names waives nothing")
}
