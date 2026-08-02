package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// TestInstallPulledItem_SyntheticNoDiskWrite covers the pull write path: a
// remote bundle is a pure reference (git clone cache + lockfile are the
// storage), so it gets a synthetic LocalPath and never touches disk. This
// synthetic-path assembly used to live in its own writePulledContent
// method (3 of its 5 parameters unused); it is now inlined into
// installPulledItem, so this test drives that entry point directly instead.
func TestInstallPulledItem_SyntheticNoDiskWrite(t *testing.T) {
	const baseDir = "/proj/.ctxloom"
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "mybundle"}

	t.Run("bundle_is_synthetic_no_disk_write", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll(baseDir, 0755))
		lm := NewLockfileManager(baseDir, WithLockfileFS(fs))
		p := &Puller{lockfileManager: lm, now: func() time.Time { return time.Now().UTC() }}
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: baseDir, Stdout: &bytes.Buffer{}}
		item := &fetchedItem{
			rem:       &Remote{Name: "alice", URL: "https://github.com/alice/ctxloom"},
			localName: "alice/mybundle",
			sha:       "abc123",
			content:   []byte("x"),
		}

		result, err := p.installPulledItem(context.Background(), ref, opts, item)
		require.NoError(t, err)
		assert.False(t, result.Overwritten)
		assert.Equal(t, "<remote>:alice/mybundle@abc123", result.LocalPath)

		// Nothing was written anywhere under the project dir except the lockfile.
		_, statErr := fs.Stat(ref.LocalPath(baseDir, ItemTypeBundle))
		assert.True(t, os.IsNotExist(statErr), "remote items must not be materialized to disk")
	})
}

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
	p := &Puller{lockfileManager: lm, now: func() time.Time { return time.Now().UTC() }}
	opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}}

	first, err := p.installPulledItem(context.Background(), ref, opts, &fetchedItem{
		rem: rem, localName: "alice/mybundle", sha: "abc123", content: []byte("x"),
	})
	require.NoError(t, err)
	assert.False(t, first.Overwritten, "the first pull of a new item is not an overwrite")

	second, err := p.installPulledItem(context.Background(), ref, opts, &fetchedItem{
		rem: rem, localName: "alice/mybundle", sha: "def456", content: []byte("y"),
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
		data, err := yaml.Marshal(Manifest{
			Version:   1,
			Retracted: []RetractEntry{{Type: ItemTypeBundle, Name: "mybundle", Reason: "security hole"}},
		})
		require.NoError(t, err)
		m.files[".ctxloom/content/manifest.yaml"] = data
		return m
	}

	p := &Puller{
		lockfileManager: NewLockfileManager(t.TempDir()),
		now:             func() time.Time { return time.Now().UTC() },
	}
	const localName = "https://github.com/alice/repo@bundles/mybundle"

	t.Run("not_retracted_passes", func(t *testing.T) {
		fetcher := newMockFetcher() // no manifest at all
		opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), fetcher, "alice", "repo", ref, localName, opts)
		assert.NoError(t, err)
		assert.False(t, retracted)
		assert.Empty(t, reason)
	})

	t.Run("retracted_force_proceeds_with_warning", func(t *testing.T) {
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeBundle, Force: true, Stdout: &out}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, localName, opts)
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
		retracted, reason, _, err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, localName, opts)
		assert.NoError(t, err)
		assert.True(t, retracted)
		assert.Equal(t, "security hole", reason)
	})

	t.Run("retracted_prompt_no_cancels", func(t *testing.T) {
		opts := PullOptions{ItemType: ItemTypeBundle, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("n\n")}
		retracted, reason, _, err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, localName, opts)
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrCancelled))
		// Even the cancelled path reports what it found — informational, not
		// consumed by the caller here, but confirmRetraction's contract is to
		// always report its verdict.
		assert.True(t, retracted)
		assert.Equal(t, "security hole", reason)
	})
}
