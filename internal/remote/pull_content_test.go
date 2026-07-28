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

// TestWritePulledContent covers the pull write path: a remote bundle is a pure
// reference (git clone cache + lockfile are the storage), so it gets a synthetic
// path and never touches disk.
func TestWritePulledContent(t *testing.T) {
	const baseDir = "/proj/.ctxloom"
	ref := &Reference{URL: "https://github.com/alice/ctxloom", ItemType: ItemTypeBundle, Path: "mybundle"}

	t.Run("bundle_is_synthetic_no_disk_write", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: baseDir, Stdout: &bytes.Buffer{}}

		localPath, overwritten, err := p.writePulledContent(ref, opts, "alice/mybundle", "abc123", []byte("x"))
		require.NoError(t, err)
		assert.False(t, overwritten)
		assert.Equal(t, "<remote>:alice/mybundle@abc123", localPath)

		// Nothing was written anywhere under the project dir.
		_, statErr := fs.Stat(ref.LocalPath(baseDir, ItemTypeBundle))
		assert.True(t, os.IsNotExist(statErr), "remote items must not be materialized to disk")
	})
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
