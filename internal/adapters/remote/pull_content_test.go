package remote

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// TestInstallPulledItem_OverwrittenReflectsExistingEntry pins that
// PullResult.Overwritten used to be hard-coded false, making
// operations/sync.go's "updated" status unreachable — a re-pull of an
// already-installed item was always reported as "installed". Overwritten must
// be true exactly when localName already had a lockfile entry before this
// write.
func TestInstallPulledItem_OverwrittenReflectsExistingEntry(t *testing.T) {
	const baseDir = "/proj/.ctxloom"
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "mybundle"}
	rem := &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"}

	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(baseDir, 0755))
	lm := NewLockfileManager(baseDir, WithLockfileFS(fs))
	p := &Puller{lockfileManager: lm, now: func() time.Time { return time.Now().UTC() }, treeInstall: stubTreeInstaller(), treeVerify: stubTreeVerifier()}
	opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}}

	// Tree-shaped: item.tree == nil is what installPulledItem now refuses
	// ("bundles are distributed as trees"), so a content-only fetchedItem
	// (the old single-file shape) can no longer drive this path at all.
	first, err := p.installPulledItem(context.Background(), ref, opts, &fetchedItem{
		rem: rem, localName: "alice/mybundle", sha: "abc123", treeRoot: ref.TreeRepoPath(),
		tree: map[string]TreeFile{"bundle.yaml": {Data: []byte("version: \"1.0.0\"\n")}},
	})
	require.NoError(t, err)
	assert.False(t, first.Overwritten, "the first pull of a new item is not an overwrite")

	second, err := p.installPulledItem(context.Background(), ref, opts, &fetchedItem{
		rem: rem, localName: "alice/mybundle", sha: "def456", treeRoot: ref.TreeRepoPath(),
		tree: map[string]TreeFile{"bundle.yaml": {Data: []byte("version: \"2.0.0\"\n")}},
	})
	require.NoError(t, err)
	assert.True(t, second.Overwritten, "re-pulling an already-installed item must report Overwritten so sync can report status \"updated\"")
}

// TestConfirmRetraction covers the retraction gate: a clean version passes
// silently, while a retracted version warns and (unless forced or confirmed)
// cancels the install.
func TestConfirmRetraction(t *testing.T) {
	ref := &Reference{Path: "mybundle"}

	retractedManifest := func(t *testing.T) *mockFetcher {
		t.Helper()
		m := newMockFetcher()
		m.files[ref.TreeRepoPath()+"/SHA256SUMS"] = []byte("withdrawn")
		return m
	}

	p := &Puller{
		lockfileManager: NewLockfileManager(t.TempDir()),
		now:             func() time.Time { return time.Now().UTC() },
		manifestVerify:  verifierFor(map[string]Verified{"withdrawn": signedTip("mybundle", "2.0.0", "security hole")}),
	}
	const localName = "https://github.com/alice/repo@bundles/mybundle"

	t.Run("not_retracted_passes", func(t *testing.T) {
		fetcher := newMockFetcher() // no manifest at all
		opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), fetcher, "alice", "repo", ref, localName, opts, LockEntry{})
		assert.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("retracted_force_proceeds_with_warning", func(t *testing.T) {
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, Force: true, Stdout: &out}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, localName, opts, LockEntry{})
		require.NoError(t, err)
		assert.Contains(t, out.String(), "retracted", "a forced pull still surfaces the retraction warning")
		// Force bypasses the block, but the verdict must still be reported so
		// installPulledItem can persist it — this is the fix: a forced/
		// non-interactive pull no longer records nothing.
		assert.True(t, retracted)
		assert.Equal(t, "security hole", reason)
	})

	t.Run("retracted_prompt_yes_proceeds", func(t *testing.T) {
		opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("y\n")}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, localName, opts, LockEntry{})
		assert.NoError(t, err)
		assert.True(t, retracted)
		assert.Equal(t, "security hole", reason)
	})

	t.Run("retracted_prompt_no_cancels", func(t *testing.T) {
		opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("n\n")}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, localName, opts, LockEntry{})
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrCancelled))
		// Even the cancelled path reports what it found — informational, not
		// consumed by the caller here, but confirmRetraction's contract is to
		// always report its verdict.
		assert.True(t, retracted)
		assert.Equal(t, "security hole", reason)
	})
}
