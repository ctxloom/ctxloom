package isolation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// fileDigests walks a workspace and returns every REGULAR file's path (relative
// to root) mapped to its permission bits and the sha256 of its bytes. Directories
// are deliberately absent from the map: the container mapping pre-creates the
// managed-config overlay MOUNTPOINTS inside the tree (empty directories a bind
// mount requires to exist — containerConfigOverlay explains why they cannot be
// left for the daemon to make), and those are plumbing, not workspace content.
// Everything a caller could actually put in the tree is a file, and every one of
// them is compared here by bytes AND mode, so a mapping that rewrote, truncated,
// chmod'd or deleted any of them cannot slip through.
func fileDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = fmt.Sprintf("%04o:%s", info.Mode().Perm(), hex.EncodeToString(sum[:]))
		return nil
	}))
	return out
}

// TestResolveMountGap_MountLeavesWorkspaceContentAlone is the load-bearing test
// for the resolution/containerization split. The split exists so a caller can
// WRITE INTO the resolved workspace between the two halves and have the run mount
// a tree that is already correct; that only holds while Mount treats the tree as
// read-only input. Nothing else in the suite would notice if Mount started
// seeding, rewriting, or clearing workspace content — it would simply begin
// silently discarding whatever the caller put there, which is the exact failure
// the gap was created to make impossible.
//
// So this pins three things, and each one is needed:
//
//   - after ResolveWorkspace the mapping has NOT run: the overlay mountpoint is
//     absent from the project. Without this, moving the mapping back inside
//     resolution would close the gap and every other assertion here would still
//     pass;
//   - Mount really did map something: a non-empty plan and the mountpoint now
//     present. Without this, a Mount gutted to `return MountPlan{}, nil` trivially
//     satisfies the content comparison;
//   - workspace file content is byte-identical across Mount, INCLUDING a file
//     written in the gap, which is the property later phases stand on.
//
// The comparison is guarded against being vacuous: the tree is asserted non-empty
// (comparing two empty reads is trivially identical), and the specific handoff
// file is asserted present in the map by name, so an accidentally-empty walk
// cannot pass as agreement.
func TestResolveMountGap_MountLeavesWorkspaceContentAlone(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	// The hermetic container gate: a fake runtime script that reports the image
	// present and provenance-current, a stubbed shared-fs probe, stubbed auth.
	fake := t.TempDir()
	script := filepath.Join(fake, "fake-docker")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	writeFakeRuntimeScript(t, script, filepath.Join(fake, "builds.log"), fake, labels)
	require.NoError(t, os.WriteFile(filepath.Join(fake, "ctxloom-agent-gap-test_latest"), nil, 0o644))

	prevFS := sharedFSCheck
	sharedFSCheck = func(context.Context, Runtime, string, []string) error { return nil }
	t.Cleanup(func() { sharedFSCheck = prevFS })

	// ".kept" exists WITH content: the overlay seeds FROM it, so it proves the
	// mapping reads the tree without writing back to it. ".claude" is absent, so
	// its mountpoint is one the mapping must create — the observable that says
	// whether the mapping has run yet.
	proj := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(proj, ".kept"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, ".kept", "user.json"), []byte(`{"user":"authored"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "src", "deep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "src", "deep", "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755))

	c := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-gap-test:latest",
		engineSpec: engineContainerSpec{
			engineInstall: []byte("RUN echo fake-install\n"),
			resolveAuth: func(string, string) (containerAuth, bool) {
				return containerAuth{mode: authEnv, envPassthrough: []string{"X"}}, true
			},
			overlayDirs: []string{".kept", ".claude"},
		},
		binaryPath: defaultContainerBinary,
		home:       defaultContainerHome,
		socketDir:  defaultContainerSocketDir,
		base:       hostBase{},
	}

	// STEP 1 — resolution only.
	ws, err := c.ResolveWorkspace(ctx, proj, "member-gap")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })
	require.Equal(t, proj, ws.Dir(), "the plain container resolves to the live project dir")
	assert.NoDirExists(t, filepath.Join(proj, ".claude"),
		"resolution must build no mounts: the overlay mountpoint belongs to the MAPPING, and its presence here means the mapping ran too early — the gap would be closed")

	// STEP 2 — the gap. This is what a later phase does: write into the resolved
	// tree so the container mounts a workspace that is already correct.
	handoff := filepath.Join(proj, "src", "HANDOFF.md")
	handoffBody := []byte("written between resolve and mount\n")
	require.NoError(t, os.WriteFile(handoff, handoffBody, 0o644))

	before := fileDigests(t, proj)
	require.NotEmpty(t, before, "guard: an empty tree would make the comparison below trivially true")
	require.Contains(t, before, filepath.Join("src", "HANDOFF.md"), "guard: the gap write is in the snapshot being compared")
	require.Contains(t, before, filepath.Join(".kept", "user.json"), "guard: the seeded-from directory's content is in the snapshot being compared")

	// STEP 3 — containerization.
	plan, err := c.Mount(ctx, ws)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Mounts,
		"guard: Mount must actually map something, or the content comparison below is satisfied by a Mount that does nothing at all")
	assert.DirExists(t, filepath.Join(proj, ".claude"),
		"the mapping creates the overlay mountpoint it needs — this is what proves the mapping ran between the two snapshots")

	// THE ASSERTION THIS TEST EXISTS FOR.
	assert.Equal(t, before, fileDigests(t, proj),
		"Mount must map the workspace, never modify it: every file's bytes and mode must survive containerization unchanged")

	kept, err := os.ReadFile(handoff)
	require.NoError(t, err)
	assert.Equal(t, handoffBody, kept,
		"a file written in the gap is exactly what the run will mount — this is the property later phases stand on")
}
