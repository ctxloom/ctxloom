package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// failChmodFs fails Chmod for exactly one path, passing everything else
// through to the wrapped Fs — the seam this regression test uses to
// force the exec-bit re-assert to fail. Matched by SUFFIX rather than exact
// equality: the render-to-temp-then-swap rewrite applies the chmod inside a
// freshly created, randomly-named temp tree (packagefiles.go phase 2), not at
// the final dir-rooted path, so the fixed relative tail ("humanize/scripts/run.sh")
// is what's stable across runs.
type failChmodFs struct {
	afero.Fs
	pathSuffix string
}

func (f failChmodFs) Chmod(name string, mode os.FileMode) error {
	if strings.HasSuffix(name, f.pathSuffix) {
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
	files := safefs.New()
	fs := files.Fs
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

	delivered, err := WriteManagedPackageFiles(files, dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)

	skillMD, err := afero.ReadFile(fs, filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(skillMD), "Body")

	info, err := fs.Stat(filepath.Join(skillDir, "scripts", "run.sh"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o755, info.Mode(), "the exec bit on scripts/run.sh must survive materialize")

	info, err = fs.Stat(filepath.Join(skillDir, "assets", "data.txt"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o644, info.Mode())

	assert.ElementsMatch(t, []string{
		filepath.Join(skillDir, "SKILL.md"),
		filepath.Join(skillDir, "scripts", "run.sh"),
		filepath.Join(skillDir, "assets", "data.txt"),
	}, delivered, "the writer returns the host path of every file it placed")
}

// TestWriteManagedPackageFiles_ReMaterializeIsIdempotent proves writing the
// SAME package twice delivers the identical set — no duplicate entries, no
// drift — and the exec bit still holds.
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

	delivered1, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)
	delivered2, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)

	assert.Equal(t, delivered1, delivered2, "re-materializing an unchanged package delivers the identical set")

	info, err := fs.Stat(filepath.Join(dir, "humanize", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit still holds after a re-materialize")
}

// TestWriteManagedPackageFiles_RemovesNothing: the writer places what it
// renders and removes nothing — not a file it placed on an earlier call and
// does not render now, and not a user's file beside it. Removing an earlier
// delivery's files is the static writer's release (fsstatic), which only the
// caller's declaration can direct.
func TestWriteManagedPackageFiles_RemovesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"
	foreign := filepath.Join(dir, "my-own-skill", "SKILL.md")
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("hand authored"), 0644))

	items := []fakeSkillItem{{
		name:    "humanize",
		enabled: true,
		files: []PackageFile{
			{RelPath: "humanize/SKILL.md", Content: []byte("managed"), Mode: 0644},
		},
	}}
	_, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)

	delivered, err := WriteManagedPackageFiles[fakeSkillItem](safefs.NewMem(fs), dir, nil, skillEnabled, skillName, skillRender)
	require.NoError(t, err)
	assert.Empty(t, delivered, "nothing rendered, nothing delivered")

	managed, err := afero.ReadFile(fs, filepath.Join(dir, "humanize", "SKILL.md"))
	require.NoError(t, err, "the earlier call's file is left for the static writer's release")
	assert.Equal(t, "managed", string(managed))
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
	delivered, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)
	assert.Empty(t, delivered)

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
	fs := failChmodFs{Fs: base, pathSuffix: filepath.Join("humanize", "scripts", "run.sh")}

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
	_, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)

	assert.NotEmpty(t, buf.String(), "a chmod failure on the exec-bit re-assert must be warned about, not silently ignored")
}

// TestWriteManagedPackageFiles_DisabledItemNotWritten proves a disabled item
// (Enabled == false) is never written and never delivered.
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
	delivered, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, items, skillEnabled, skillName, skillRender, WithWriteReporter(termRep().Sink))
	require.NoError(t, err)
	assert.Empty(t, delivered, "a disabled item must not be delivered")

	exists, _ := afero.Exists(fs, filepath.Join(dir, "off", "SKILL.md"))
	assert.False(t, exists, "a disabled item must not be written")
}
