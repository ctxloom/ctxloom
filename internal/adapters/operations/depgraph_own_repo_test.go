package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
)

// The lock walk reads a remote parent profile straight out of its fetched
// bundle, never through the seeded loader, so it holds that profile to its own
// repository itself: a parent reaching into another repository is not
// expanded, and nothing it names there is pinned.
func TestLockDependencies_RemoteParentReachingIntoAnotherRepoIsNotExpanded(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")
	src := filepath.Join(tmp, "src")
	elsewhere := filepath.Join(tmp, "elsewhere")
	parentBundleID := "file://" + src + "@bundles/kit"
	foreignID := "file://" + elsewhere + "@bundles/payload"

	initLocalRepoWithFile(t, elsewhere, repoV2("payload")+"/bundle.yaml", "version: 1.0.0\n")
	initLocalRepoWithFile(t, src, repoV2("kit")+"/bundle.yaml", "version: 1.0.0\n")
	addFileToLocalRepo(t, src, repoV2("kit")+"/profiles/parent.yaml", "bundles:\n  - "+foreignID+"\n")
	writeLocalProfile(t, baseDir, "default", "parents:\n  - "+parentBundleID+"#profiles/parent\n")
	// Both registered, so what refuses the reach is the own-repository rule.
	registerRefRemotes(t, baseDir, parentBundleID, foreignID)
	cfg := testConfigWithSCMPath(baseDir)

	stderr := captureStderr(t, func() {
		_, err := LockDependencies(context.Background(), cfg, LockDependenciesRequest{FailOnConflict: true})
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, profiles.ErrCrossRepoReference.Error(), "the walk says why the parent was not expanded")
	assert.Contains(t, stderr, string(lockKeyOf(t, foreignID)), "and names the reference that broke the rule")

	active := mustLoadActive(t, baseDir)
	_, okP := active.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, parentBundleID))
	assert.True(t, okP, "the parent bundle the local profile names is still pinned")
	_, okF := active.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, foreignID))
	assert.False(t, okF, "nothing the violating profile names in another repository is pinned")
}
