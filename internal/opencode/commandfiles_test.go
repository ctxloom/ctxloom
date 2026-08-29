//go:build parked_engines

package opencode

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// TestWriteCommandFiles_RendersOpencodeCommand proves an enabled export lands at
// .opencode/command/<name>.md with opencode-valid front-matter (description) and
// the body as the command template — the exact shape opencode's `command` config
// map is built from.
func TestWriteCommandFiles_RendersOpencodeCommand(t *testing.T) {
	fs := afero.NewMemMapFs()
	err := WriteCommandFiles("/work", []agent.CommandExport{
		{Name: "reviewer/review", Description: "Review the diff", Content: "Do the review", Enabled: true},
	}, agent.WithCommandFS(fs))
	require.NoError(t, err)

	path := filepath.Join("/work", ".opencode", "command", "reviewer", "review.md")
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err, "enabled export writes a nested command file")
	assert.Equal(t, "---\ndescription: Review the diff\n---\n\nDo the review\n", string(data))
}

// TestWriteCommandFiles_DisabledNotWritten proves a disabled export leaves no
// file (opt-out enablement is honored at the write boundary).
func TestWriteCommandFiles_DisabledNotWritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	err := WriteCommandFiles("/work", []agent.CommandExport{
		{Name: "off", Description: "nope", Content: "x", Enabled: false},
	}, agent.WithCommandFS(fs))
	require.NoError(t, err)

	exists, _ := afero.Exists(fs, filepath.Join("/work", ".opencode", "command", "off.md"))
	assert.False(t, exists, "disabled export must not be written")
}

// TestWriteCommandFiles_DescriptionFallback proves a blank description falls back
// to the command name so opencode never sees an empty description (its command
// loader wants a description).
func TestWriteCommandFiles_DescriptionFallback(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, WriteCommandFiles("/w", []agent.CommandExport{
		{Name: "bare", Content: "body", Enabled: true},
	}, agent.WithCommandFS(fs)))
	data, err := afero.ReadFile(fs, filepath.Join("/w", ".opencode", "command", "bare.md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "description: bare\n")
}

// TestWriteCommandFiles_EmptyContentIsSkippedNotWritten pins the same
// defect class RenderCommandAsSkillFile had: a command with empty
// or whitespace-only Content used to still render a valid-looking
// `<name>.md` carrying only frontmatter — success, zero instruction bytes
// delivered to opencode. WriteManagedPackageFiles already warns loudly and
// skips an item whose render fails, so refusing to render is enough to
// surface the loss instead of materializing a hollow command file.
func TestWriteCommandFiles_EmptyContentIsSkippedNotWritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	err := WriteCommandFiles("/work", []agent.CommandExport{
		{Name: "hollow", Description: "looks fine", Content: "", Enabled: true},
	}, agent.WithCommandFS(fs))
	require.NoError(t, err, "a render failure is warned-and-skipped by the shared writer, not propagated as a hard error")

	exists, _ := afero.Exists(fs, filepath.Join("/work", ".opencode", "command", "hollow.md"))
	assert.False(t, exists, "empty-content command must not materialize a frontmatter-only file")
}

// TestWriteCommandFiles_CleanupRemovesOnlyOurs proves the manifest-scoped cleanup
// (re-write with no exports) removes exactly ctxloom's files and its manifest, and
// never a foreign command file sharing the same dir.
func TestWriteCommandFiles_CleanupRemovesOnlyOurs(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := filepath.Join("/work", ".opencode", "command")
	// A user-authored command already in the dir.
	foreign := filepath.Join(dir, "mine.md")
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("user command"), 0o644))

	require.NoError(t, WriteCommandFiles("/work", []agent.CommandExport{
		{Name: "ours", Description: "ctxloom", Content: "c", Enabled: true},
	}, agent.WithCommandFS(fs)))
	oursExists, _ := afero.Exists(fs, filepath.Join(dir, "ours.md"))
	require.True(t, oursExists)

	// Revert exactly our set.
	require.NoError(t, WriteCommandFiles("/work", nil, agent.WithCommandFS(fs)))

	oursExists, _ = afero.Exists(fs, filepath.Join(dir, "ours.md"))
	assert.False(t, oursExists, "cleanup removes ctxloom's command")
	manifestExists, _ := afero.Exists(fs, filepath.Join(dir, ledger.Name))
	assert.False(t, manifestExists, "cleanup removes the manifest")
	foreignData, err := afero.ReadFile(fs, foreign)
	require.NoError(t, err, "foreign command survives cleanup")
	assert.Equal(t, "user command", string(foreignData))
}

// TestSurfaces_CommandsSurface proves the materialize path's SurfaceSet carries
// a commands surface that writes the command files and reverts them on cleanup.
func TestSurfaces_CommandsSurface(t *testing.T) {
	fs := afero.NewMemMapFs()
	set := NewSurfaces(agent.SurfaceInputs{
		Commands: []agent.CommandExport{{Name: "s", Description: "d", Content: "b", Enabled: true}},
	}, fs)

	s, err := set.SurfaceFor(agent.SurfaceCommands, agent.ApproachUnsafeFile)
	require.NoError(t, err, "opencode declares a commands surface")

	delivered, err := s.Deliver("/proj")
	require.NoError(t, err)
	path := filepath.Join("/proj", ".opencode", "command", "s.md")
	exists, _ := afero.Exists(fs, path)
	assert.True(t, exists, "commands surface materializes the command")

	require.NoError(t, delivered.Cleanup())
	exists, _ = afero.Exists(fs, path)
	assert.False(t, exists, "commands surface cleanup reverts the command")
}
