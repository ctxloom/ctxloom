package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// This file pins render-then-swap: nothing under dir is touched until every
// item has rendered off the live tree. What a concurrent
// reader sees during a redelivery is the static writer's to guarantee — it
// is what lands these files on disk — and is pinned in fsstatic
// (TestDeliver_AConcurrentReaderNeverSeesARedeliveredFileMissing).

// fakeItem is a second WriteManagedPackageFiles test double, alongside
// fakeSkillItem in packagefiles_test.go: it additionally carries an
// injectable render error, the seam TestWriteManagedPackageFiles_
// RenderFailureTouchesNothing needs and fakeSkillItem has no room for.
type fakeItem struct {
	name    string
	enabled bool
	files   []PackageFile
	err     error
}

func fakeItemEnabled(i fakeItem) bool                  { return i.enabled }
func fakeItemName(i fakeItem) string                   { return i.name }
func fakeItemRender(i fakeItem) ([]PackageFile, error) { return i.files, i.err }

// TestWriteManagedPackageFiles_RenderFailureTouchesNothing: an item whose
// render fails is skipped with a warning and delivers nothing, and the file an
// earlier call placed stands byte for byte — whether it then goes is the
// static writer's release to decide, from what the caller declares.
func TestWriteManagedPackageFiles_RenderFailureTouchesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"

	seed := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		files: []PackageFile{
			{RelPath: "reviewer/SKILL.md", Content: []byte("---\nname: reviewer\n---\nv1"), Mode: 0644},
			{RelPath: "reviewer/scripts/run.sh", Content: []byte("#!/bin/sh\necho v1\n"), Mode: 0755},
		},
	}}
	_, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, seed, fakeItemEnabled, fakeItemName, fakeItemRender)
	require.NoError(t, err)
	beforeScript, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err, "precondition: the seed materialize wrote the script")

	failing := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		err:     errors.New("boom: template render failed"),
	}}
	delivered, err := WriteManagedPackageFiles(safefs.NewMem(fs), dir, failing, fakeItemEnabled, fakeItemName, fakeItemRender)
	require.NoError(t, err, "a render failure is a per-item warn-and-skip")
	assert.Empty(t, delivered, "a skipped item delivers nothing")

	afterScript, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err, "the writer removes nothing")
	assert.Equal(t, beforeScript, afterScript, "old content must be byte-identical; a render failure must not touch it")
}

// TestWriteManagedPackageFiles_FirstDeliveryIntoWhollyNonexistentTree pins the
// merge-gate defect found across this writer's consumer packages: on a
// FIRST-EVER
// delivery, dir's own parent (e.g. .claude/, the engine's config dir) does not
// exist yet either. Phase 2 creates dir's temp SIBLING via
// afero.TempDir(fs, filepath.Dir(dir), …), which — unlike the pre-rewrite
// writer's single recursive fs.MkdirAll(dir, …) — needs filepath.Dir(dir) to
// already exist; afero.TempDir's underlying Mkdir is not recursive. Mutation:
// remove the fs.MkdirAll(filepath.Dir(dir), …) call added ahead of the
// afero.TempDir call and this goes red with exactly the reported error shape
// ("mkdir .../.skills.tmp-…: no such file or directory").
func TestWriteManagedPackageFiles_FirstDeliveryIntoWhollyNonexistentTree(t *testing.T) {
	files := safefs.New()
	fs := files.Fs
	root := t.TempDir()
	// Nothing below root exists yet — not "project", not ".claude", not
	// ".claude/skills" — mirroring a fresh checkout with no prior ctxloom
	// delivery at all.
	dir := filepath.Join(root, "project", ".claude", "skills")

	items := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		files: []PackageFile{
			{RelPath: "reviewer/SKILL.md", Content: []byte("---\nname: reviewer\n---\nBody"), Mode: 0644},
			{RelPath: "reviewer/scripts/run.sh", Content: []byte("#!/bin/sh\necho reviewer\n"), Mode: 0755},
		},
	}}

	_, err := WriteManagedPackageFiles(files, dir, items, fakeItemEnabled, fakeItemName, fakeItemRender)
	require.NoError(t, err, "a first-ever delivery into a wholly nonexistent parent chain must succeed, not fail on temp-tree creation")

	skillMD, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "SKILL.md"))
	require.NoError(t, err, "SKILL.md must actually be on disk, not just report success")
	assert.Contains(t, string(skillMD), "Body")

	script, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err, "the sibling script must actually be on disk")
	assert.Contains(t, string(script), "echo reviewer")

	info, err := fs.Stat(filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err)
	fileperm.Equal(t, 0o755, info.Mode(), "the exec bit must survive a from-scratch delivery")
}
