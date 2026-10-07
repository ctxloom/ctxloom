package remote

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstallPulledItem_ReinstalledReflectsExistingEntry pins that
// PullResult.Reinstalled used to be hard-coded false, making
// operations/sync.go's "reinstalled" status unreachable — a re-pull of an
// already-installed item was always reported as "installed". Reinstalled must
// be true exactly when localName already had a lockfile entry before this
// write.
func TestInstallPulledItem_ReinstalledReflectsExistingEntry(t *testing.T) {
	const baseDir = "/proj/.ctxloom"
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "mybundle"}
	rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(baseDir, 0755))
	lm := NewLockfileManager(baseDir, WithLockfileFS(fs))
	p := &Puller{lockfileManager: lm, treeInstall: stubTreeInstaller()}
	opts := PullOptions{ItemType: ItemTypeBundle}

	// Tree-shaped: item.tree == nil is what installPulledItem now refuses
	// ("bundles are distributed as trees"), so a content-only fetchedItem
	// (the old single-file shape) can no longer drive this path at all.
	first, err := p.installPulledItem(context.Background(), ref, opts, &fetchedItem{
		rem: rem, localName: "ctxloom+git://github.com/alice/ctxloom//bundles/mybundle", sha: "abc123", treeRoot: ref.TreeRepoPath(),
		tree: map[string]TreeFile{"bundle.yaml": {Data: []byte("version: \"1.0.0\"\n")}},
	})
	require.NoError(t, err)
	assert.False(t, first.Reinstalled, "the first pull of a new item is not a reinstall")

	second, err := p.installPulledItem(context.Background(), ref, opts, &fetchedItem{
		rem: rem, localName: "ctxloom+git://github.com/alice/ctxloom//bundles/mybundle", sha: "def456", treeRoot: ref.TreeRepoPath(),
		tree: map[string]TreeFile{"bundle.yaml": {Data: []byte("version: \"2.0.0\"\n")}},
	})
	require.NoError(t, err)
	assert.True(t, second.Reinstalled, "re-pulling an already-installed item must report Reinstalled so sync can report status \"reinstalled\"")
}
