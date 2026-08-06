package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// failChmodFs fails Chmod for exactly one path, passing everything else
// through to the wrapped Fs — the seam this regression test uses to
// force the exec-bit re-assert to fail.
type failChmodFs struct {
	afero.Fs
	path string
}

func (f failChmodFs) Chmod(name string, mode os.FileMode) error {
	if name == f.path {
		return &os.PathError{Op: "chmod", Path: name, Err: os.ErrPermission}
	}
	return f.Fs.Chmod(name, mode)
}

// fakeSkillItem is the minimal test double WriteManagedPackageFiles is driven
// through directly (independent of agent.SkillExport, which lives one layer up
// and is exercised by its own tests) — just a name, an enabled flag, and the
// files a "render" would produce for it.
type fakeSkillItem struct {
	name    string
	enabled bool
	files   []PackageFile
}

func skillEnabled(i fakeSkillItem) bool                  { return i.enabled }
func skillName(i fakeSkillItem) string                   { return i.name }
func skillRender(i fakeSkillItem) ([]PackageFile, error) { return i.files, nil }

// TestWriteManagedPackageFiles_ExecBitPreserved proves a skill-shaped package
// (SKILL.md + scripts/run.sh at 0755 + assets/data.txt at 0644) materializes
// with every file's mode intact — the exec bit on scripts/run.sh is
// load-bearing (skill-command-split.plan.md §3.1) and must survive the write.
func TestWriteManagedPackageFiles_ExecBitPreserved(t *testing.T) {
	fs := afero.NewOsFs()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "humanize")

	items := []fakeSkillItem{{
		name:    "humanize",
		enabled: true,
		files: []PackageFile{
			{RelPath: "humanize/SKILL.md", Content: []byte("---\nname: humanize\n---\nBody"), Mode: 0644},
			{RelPath: "humanize/scripts/run.sh", Content: []byte("#!/bin/sh\necho hi\n"), Mode: 0755},
			{RelPath: "humanize/assets/data.txt", Content: []byte("data"), Mode: 0644},
		},
	}}

	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))

	skillMD, err := afero.ReadFile(fs, filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(skillMD), "Body")

	info, err := fs.Stat(filepath.Join(skillDir, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit on scripts/run.sh must survive materialize")

	info, err = fs.Stat(filepath.Join(skillDir, "assets", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())

	manifest, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "humanize/SKILL.md")
	assert.Contains(t, string(manifest), "humanize/scripts/run.sh")
	assert.Contains(t, string(manifest), "humanize/assets/data.txt")
}

// TestWriteManagedPackageFiles_ReMaterializeIsIdempotent proves writing the
// SAME package twice produces the identical on-disk tree and manifest — no
// duplicate entries, no drift, no leftover files from the first pass.
func TestWriteManagedPackageFiles_ReMaterializeIsIdempotent(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	items := []fakeSkillItem{{
		name:    "humanize",
		enabled: true,
		files: []PackageFile{
			{RelPath: "humanize/SKILL.md", Content: []byte("v1"), Mode: 0644},
			{RelPath: "humanize/scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
		},
	}}

	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))
	manifest1, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	require.NoError(t, err)

	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))
	manifest2, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	require.NoError(t, err)

	assert.Equal(t, string(manifest1), string(manifest2), "re-materializing an unchanged package produces the identical manifest")

	info, err := fs.Stat(filepath.Join(dir, "humanize", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit still holds after a re-materialize")
}

// TestWriteManagedPackageFiles_CleanupPreservesForeignFiles proves cleanup
// (re-invoking the writer with zero items) removes only the manifest-tracked
// ctxloom-written set — a foreign, user-authored file planted in the same
// directory survives.
func TestWriteManagedPackageFiles_CleanupPreservesForeignFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	foreign := filepath.Join(dir, "my-own-skill", "SKILL.md")
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("hand authored"), 0644))

	items := []fakeSkillItem{{
		name:    "humanize",
		enabled: true,
		files: []PackageFile{
			{RelPath: "humanize/SKILL.md", Content: []byte("managed"), Mode: 0644},
			{RelPath: "humanize/scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
		},
	}}
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))

	managedExists, _ := afero.Exists(fs, filepath.Join(dir, "humanize", "SKILL.md"))
	require.True(t, managedExists, "precondition: the managed package was written")

	// Cleanup: re-invoke with no items — reverts exactly the manifest-tracked set.
	require.NoError(t, WriteManagedPackageFiles[fakeSkillItem](fs, dir, ledger.SurfaceSkills, nil, skillEnabled, skillName, skillRender))

	managedExists, _ = afero.Exists(fs, filepath.Join(dir, "humanize", "SKILL.md"))
	assert.False(t, managedExists, "cleanup removes the ctxloom-managed package")
	managedDirExists, _ := afero.DirExists(fs, filepath.Join(dir, "humanize"))
	assert.False(t, managedDirExists, "cleanup prunes the now-empty managed package directory")

	foreignExists, err := afero.Exists(fs, foreign)
	require.NoError(t, err)
	assert.True(t, foreignExists, "a foreign, non-manifest-tracked file must survive cleanup")

	content, err := afero.ReadFile(fs, foreign)
	require.NoError(t, err)
	assert.Equal(t, "hand authored", string(content), "the foreign file's content is untouched")
}

// TestWriteManagedPackageFiles_UnsafeItemPathSkipsWholeItem proves a package
// with one path-unsafe file is skipped ENTIRELY (no partial tree on disk) —
// the silent-no-op / partial-materialize discipline this codebase holds
// writers to.
func TestWriteManagedPackageFiles_UnsafeItemPathSkipsWholeItem(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	items := []fakeSkillItem{{
		name:    "bad",
		enabled: true,
		files: []PackageFile{
			{RelPath: "bad/SKILL.md", Content: []byte("ok"), Mode: 0644},
			{RelPath: "../escape.md", Content: []byte("evil"), Mode: 0644},
		},
	}}
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))

	exists, _ := afero.Exists(fs, filepath.Join(dir, "bad", "SKILL.md"))
	assert.False(t, exists, "a package with any unsafe file path writes NONE of its files")
	exists, _ = afero.Exists(fs, filepath.Join(dir, "..", "escape.md"))
	assert.False(t, exists)
}

// TestWriteManagedPackageFiles_ChmodFailureWarns pins the fix: the
// exec-bit re-assert chmod's own comment argues it is needed for correctness
// ("would silently let the exec bit drift out of sync"), then ignored its own
// error — so a chmod failure was invisible even though the writer knows
// exactly why it matters.
func TestWriteManagedPackageFiles_ChmodFailureWarns(t *testing.T) {
	base := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	scriptPath := filepath.Join(dir, "humanize", "scripts", "run.sh")
	fs := failChmodFs{Fs: base, path: scriptPath}

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	items := []fakeSkillItem{{
		name:    "humanize",
		enabled: true,
		files: []PackageFile{
			{RelPath: "humanize/scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
		},
	}}
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))

	assert.NotEmpty(t, buf.String(), "a chmod failure on the exec-bit re-assert must be warned about, not silently ignored")
}

// TestWriteManagedPackageFiles_DisabledItemNotWritten proves a disabled item
// (Enabled == false) is never written and never manifest-tracked.
func TestWriteManagedPackageFiles_DisabledItemNotWritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	items := []fakeSkillItem{{
		name:    "off",
		enabled: false,
		files: []PackageFile{
			{RelPath: "off/SKILL.md", Content: []byte("nope"), Mode: 0644},
		},
	}}
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, skillEnabled, skillName, skillRender))

	exists, _ := afero.Exists(fs, filepath.Join(dir, "off", "SKILL.md"))
	assert.False(t, exists, "a disabled item must not be written")
	_, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	assert.Error(t, err, "nothing written means no manifest at all")
}
