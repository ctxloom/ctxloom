package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// This file pins the render-then-swap rewrite (fs-consolidation C11 /
// taskloom humorless-factor): WriteManagedPackageFiles used to delete this
// surface's entire previously-tracked set BEFORE rendering the replacement,
// so a render failure — or even the ordinary happy path, to a concurrent
// reader — left a window where a ledgered file was simply absent while the
// call still reported success. These tests exercise that window directly.

// fakeItem is a second WriteManagedPackageFiles test double, alongside
// fakeSkillItem in packagefiles_test.go: it additionally carries an
// injectable render error, the seam TestWriteManagedPackageFiles_
// RenderFailureLeavesOldSurfaceIntact needs and fakeSkillItem has no room for.
type fakeItem struct {
	name    string
	enabled bool
	files   []PackageFile
	err     error
}

func fakeItemEnabled(i fakeItem) bool                  { return i.enabled }
func fakeItemName(i fakeItem) string                   { return i.name }
func fakeItemRender(i fakeItem) ([]PackageFile, error) { return i.files, i.err }

// TestWriteManagedPackageFiles_RenderFailureLeavesOldSurfaceIntact is the
// mutation kill for phase ordering: revert render-then-swap back to the
// historical delete-first order and this goes red, because the previously
// written scripts/run.sh would already be gone by the time the render error
// is discovered — regardless of which layer catches the failure. Here the
// ONLY enabled item fails to render, so this is also exercising the
// empty-render guard's "would gut a previously-populated surface" branch
// (a render() error is a per-item warn-and-skip, same as an unsafe path —
// see the doc comment above WriteManagedPackageFiles' phase 1 — but skipping
// the only item still drives expectedFileCount to zero against a non-empty
// previous ledger, which the guard refuses).
func TestWriteManagedPackageFiles_RenderFailureLeavesOldSurfaceIntact(t *testing.T) {
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
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, seed, fakeItemEnabled, fakeItemName, fakeItemRender))

	beforeScript, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err, "precondition: the seed materialize wrote the script")
	beforeManifest, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	require.NoError(t, err)

	failing := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		err:     errors.New("boom: template render failed"),
	}}
	writeErr := WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, failing, fakeItemEnabled, fakeItemName, fakeItemRender)
	require.Error(t, writeErr, "a render failure must propagate as a real error, never be swallowed into a quiet success")

	afterScript, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err, "the old file must still exist after a render failure — a missing file here is the silent-no-op regression")
	assert.Equal(t, beforeScript, afterScript, "old content must be byte-identical; a render failure must not touch it")

	afterManifest, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	require.NoError(t, err)
	assert.Equal(t, string(beforeManifest), string(afterManifest), "the ledger must not change when the render step fails")
}

// TestWriteManagedPackageFiles_EmptyRenderGuardRefusesToGutExistingSurface is
// the mutation kill for the empty-render guard: drop the guard and this goes
// red, because the "every rendered path is unsafe" case would fall straight
// into the legitimate-empty-target path and delete the surviving SKILL.md.
func TestWriteManagedPackageFiles_EmptyRenderGuardRefusesToGutExistingSurface(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/work/.claude/skills"

	seed := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		files:   []PackageFile{{RelPath: "reviewer/SKILL.md", Content: []byte("v1"), Mode: 0644}},
	}}
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, seed, fakeItemEnabled, fakeItemName, fakeItemRender))

	// Enabled (the caller still wants content), render succeeds with no error,
	// but its ONLY file has an unsafe path — every file this call would
	// produce is rejected, so the net render output is zero despite a
	// non-empty enabled-items list and a non-empty existing ledger.
	starved := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		files:   []PackageFile{{RelPath: "../escape.md", Content: []byte("x"), Mode: 0644}},
	}}
	writeErr := WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, starved, fakeItemEnabled, fakeItemName, fakeItemRender)
	require.Error(t, writeErr, "zero rendered files against a non-empty ledger must refuse, not silently empty the surface")

	exists, err := afero.Exists(fs, filepath.Join(dir, "reviewer", "SKILL.md"))
	require.NoError(t, err)
	assert.True(t, exists, "the guard must fire BEFORE any destructive step — old content survives a refused call")

	manifest, err := afero.ReadFile(fs, filepath.Join(dir, ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "reviewer/SKILL.md", "the ledger must be untouched by a refused call")
}

// TestWriteManagedPackageFiles_ConcurrentReaderNeverObservesMissingLedgeredFile
// is the dutiful-water stress test: it races a writer re-materializing the
// SAME multi-file skill package (SKILL.md + a mode-bearing scripts/run.sh,
// the exact shape of the J20 repro row) against a reader that lists+reads the
// surface, and fails the instant the reader observes a ledgered file
// (previously seen present) go missing. Bounded by an iteration count, not
// wall-clock, so it stays fast and deterministic-ish; run against a real
// OS filesystem (not MemMapFs) because the property under test is
// os.Rename's atomicity, which a fake filesystem does not necessarily model.
func TestWriteManagedPackageFiles_ConcurrentReaderNeverObservesMissingLedgeredFile(t *testing.T) {
	fs := afero.NewOsFs()
	dir := t.TempDir()

	const iterations = 400
	item := []fakeItem{{
		name:    "reviewer",
		enabled: true,
		files: []PackageFile{
			{RelPath: "reviewer/SKILL.md", Content: []byte("---\nname: reviewer\n---\nBody"), Mode: 0644},
			{RelPath: "reviewer/scripts/run.sh", Content: []byte("#!/bin/sh\necho reviewer\n"), Mode: 0755},
		},
	}}
	require.NoError(t, WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, item, fakeItemEnabled, fakeItemName, fakeItemRender),
		"seed materialize must succeed before the race begins")

	skillPath := filepath.Join(dir, "reviewer", "SKILL.md")
	scriptPath := filepath.Join(dir, "reviewer", "scripts", "run.sh")

	var everSeen atomic.Bool
	var violated atomic.Bool
	var detail atomic.Value
	detail.Store("")
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < iterations; i++ {
			if writeErr := WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, item, fakeItemEnabled, fakeItemName, fakeItemRender); writeErr != nil {
				violated.Store(true)
				detail.Store(fmt.Sprintf("iteration %d: re-materialize failed: %v", i, writeErr))
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			skillExists, _ := afero.Exists(fs, skillPath)
			scriptExists, _ := afero.Exists(fs, scriptPath)
			if skillExists && scriptExists {
				everSeen.Store(true)
				continue
			}
			if everSeen.Load() && !violated.Load() {
				violated.Store(true)
				detail.Store(fmt.Sprintf("observed a ledgered file missing after previously seeing both present: SKILL.md exists=%v scripts/run.sh exists=%v", skillExists, scriptExists))
			}
		}
	}()

	wg.Wait()

	assert.False(t, violated.Load(), "%s", detail.Load())
	assert.True(t, everSeen.Load(), "the reader must have observed the surface present at least once, or this test proves nothing")
}

// TestWriteManagedPackageFiles_FirstDeliveryIntoWhollyNonexistentTree pins the
// merge-gate defect found in four consumer packages (internal/claude,
// internal/kiro, internal/opencode, internal/operations): on a FIRST-EVER
// delivery, dir's own parent (e.g. .claude/, the engine's config dir) does not
// exist yet either. Phase 2 creates dir's temp SIBLING via
// afero.TempDir(fs, filepath.Dir(dir), …), which — unlike the pre-rewrite
// writer's single recursive fs.MkdirAll(dir, …) — needs filepath.Dir(dir) to
// already exist; afero.TempDir's underlying Mkdir is not recursive. Mutation:
// remove the fs.MkdirAll(filepath.Dir(dir), …) call added ahead of the
// afero.TempDir call and this goes red with exactly the reported error shape
// ("mkdir .../.skills.tmp-…: no such file or directory").
func TestWriteManagedPackageFiles_FirstDeliveryIntoWhollyNonexistentTree(t *testing.T) {
	fs := afero.NewOsFs()
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

	err := WriteManagedPackageFiles(fs, dir, ledger.SurfaceSkills, items, fakeItemEnabled, fakeItemName, fakeItemRender)
	require.NoError(t, err, "a first-ever delivery into a wholly nonexistent parent chain must succeed, not fail on temp-tree creation")

	skillMD, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "SKILL.md"))
	require.NoError(t, err, "SKILL.md must actually be on disk, not just report success")
	assert.Contains(t, string(skillMD), "Body")

	script, err := afero.ReadFile(fs, filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err, "the sibling script must actually be on disk")
	assert.Contains(t, string(script), "echo reviewer")

	info, err := fs.Stat(filepath.Join(dir, "reviewer", "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "the exec bit must survive a from-scratch delivery")
}
