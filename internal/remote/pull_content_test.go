package remote

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// TestWritePulledContent covers the disk-write path of a pull: bundles get a
// synthetic path and never touch disk, other item types write through, and an
// existing differing file is guarded by an overwrite prompt unless forced.
func TestWritePulledContent(t *testing.T) {
	const baseDir = "/proj/.ctxloom"
	ref := &Reference{Remote: "alice", Path: "myprofile"}

	t.Run("bundle_is_synthetic_no_disk_write", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		opts := PullOptions{ItemType: ItemTypeBundle, LocalDir: baseDir, Stdout: &bytes.Buffer{}}

		localPath, overwritten, err := p.writePulledContent(ref, opts, "alice/myprofile", "abc123", []byte("x"))
		require.NoError(t, err)
		assert.False(t, overwritten)
		assert.Equal(t, "<remote>:alice/myprofile@abc123", localPath)

		// Nothing was written under the cache dir.
		entries, _ := afero.ReadDir(fs, baseDir)
		assert.Empty(t, entries, "bundles must not be written to disk")
	})

	t.Run("new_profile_writes_through", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		opts := PullOptions{ItemType: ItemTypeProfile, LocalDir: baseDir, Stdout: &bytes.Buffer{}}

		localPath, overwritten, err := p.writePulledContent(ref, opts, "alice/myprofile", "sha", []byte("hello"))
		require.NoError(t, err)
		assert.False(t, overwritten, "a fresh file is not an overwrite")

		got, readErr := afero.ReadFile(fs, localPath)
		require.NoError(t, readErr)
		assert.Equal(t, "hello", string(got))
	})

	t.Run("existing_identical_content_skips_prompt", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		opts := PullOptions{ItemType: ItemTypeProfile, LocalDir: baseDir, Stdout: &bytes.Buffer{}}
		localPath := ref.LocalPath(baseDir, ItemTypeProfile)
		require.NoError(t, afero.WriteFile(fs, localPath, []byte("same"), 0o644))

		// Stdin is empty: if a prompt were issued the read would fail, so a
		// clean return proves identical content short-circuits the prompt.
		opts.Stdin = strings.NewReader("")
		_, overwritten, err := p.writePulledContent(ref, opts, "alice/myprofile", "sha", []byte("same"))
		require.NoError(t, err)
		assert.True(t, overwritten, "an existing file counts as overwritten even when content is identical")
	})

	t.Run("existing_differing_force_overwrites_without_prompt", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		localPath := ref.LocalPath(baseDir, ItemTypeProfile)
		require.NoError(t, afero.WriteFile(fs, localPath, []byte("old"), 0o644))
		opts := PullOptions{ItemType: ItemTypeProfile, LocalDir: baseDir, Force: true, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("")}

		_, overwritten, err := p.writePulledContent(ref, opts, "alice/myprofile", "sha", []byte("new"))
		require.NoError(t, err)
		assert.True(t, overwritten)
		got, _ := afero.ReadFile(fs, localPath)
		assert.Equal(t, "new", string(got), "force overwrites the differing file")
	})

	t.Run("existing_differing_prompt_yes_overwrites", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		localPath := ref.LocalPath(baseDir, ItemTypeProfile)
		require.NoError(t, afero.WriteFile(fs, localPath, []byte("old"), 0o644))
		opts := PullOptions{ItemType: ItemTypeProfile, LocalDir: baseDir, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("y\n")}

		_, overwritten, err := p.writePulledContent(ref, opts, "alice/myprofile", "sha", []byte("new"))
		require.NoError(t, err)
		assert.True(t, overwritten)
		got, _ := afero.ReadFile(fs, localPath)
		assert.Equal(t, "new", string(got))
	})

	t.Run("existing_differing_prompt_no_cancels", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		p := &Puller{fs: fs}
		localPath := ref.LocalPath(baseDir, ItemTypeProfile)
		require.NoError(t, afero.WriteFile(fs, localPath, []byte("old"), 0o644))
		opts := PullOptions{ItemType: ItemTypeProfile, LocalDir: baseDir, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("n\n")}

		_, _, err := p.writePulledContent(ref, opts, "alice/myprofile", "sha", []byte("new"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrCancelled), "declining the overwrite prompt cancels the write")
		got, _ := afero.ReadFile(fs, localPath)
		assert.Equal(t, "old", string(got), "a cancelled write must not modify the existing file")
	})
}

// TestConfirmRetraction covers the retraction gate: a clean version passes
// silently, while a retracted version warns and (unless forced or confirmed)
// cancels the install.
func TestConfirmRetraction(t *testing.T) {
	ref := &Reference{Path: "myprofile"}

	retractedManifest := func(t *testing.T) *mockFetcher {
		t.Helper()
		m := newMockFetcher()
		data, err := yaml.Marshal(Manifest{
			Version:   1,
			Retracted: []RetractEntry{{Type: ItemTypeProfile, Name: "myprofile", Reason: "security hole"}},
		})
		require.NoError(t, err)
		m.files["ctxloom/manifest.yaml"] = data
		return m
	}

	p := &Puller{}

	t.Run("not_retracted_passes", func(t *testing.T) {
		fetcher := newMockFetcher() // no manifest at all
		opts := PullOptions{ItemType: ItemTypeProfile, Stdout: &bytes.Buffer{}}
		assert.NoError(t, p.confirmRetraction(context.Background(), fetcher, "alice", "repo", ref, opts))
	})

	t.Run("retracted_force_proceeds_with_warning", func(t *testing.T) {
		var out bytes.Buffer
		opts := PullOptions{ItemType: ItemTypeProfile, Force: true, Stdout: &out}
		require.NoError(t, p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, opts))
		assert.Contains(t, out.String(), "retracted", "a forced pull still surfaces the retraction warning")
	})

	t.Run("retracted_prompt_yes_proceeds", func(t *testing.T) {
		opts := PullOptions{ItemType: ItemTypeProfile, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("y\n")}
		assert.NoError(t, p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, opts))
	})

	t.Run("retracted_prompt_no_cancels", func(t *testing.T) {
		opts := PullOptions{ItemType: ItemTypeProfile, Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("n\n")}
		err := p.confirmRetraction(context.Background(), retractedManifest(t), "alice", "repo", ref, opts)
		require.Error(t, err)
		assert.True(t, errors.Is(err, errs.ErrCancelled))
	})
}
