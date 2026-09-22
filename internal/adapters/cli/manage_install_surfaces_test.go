package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The project-side runtime surfaces: everything an engine launched DIRECTLY
// in the project tree would read. None of them is install's to write —
// a `ctxloom run` session carries its own copy in its session home, and a
// copy in the project is what let a bare engine pick up ctxloom's MCP shim
// with no session behind it. Asserted on the FILESYSTEM, never on the exit
// code: this project's characteristic defect is exit 0 with the wrong bytes.
var projectRuntimeSurfaces = []string{
	".mcp.json",
	".claude",
	"CLAUDE.md",
	".mock",
	"MOCK_CONTEXT.md",
}

// topLevelEntries lists dir's immediate children, sorted.
func topLevelEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// treeSnapshot maps every path under dir (files and directories) to its
// bytes, so a no-op can be proven byte for byte rather than by exit code.
func treeSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			snap[rel] = "<dir>"
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		snap[rel] = string(body)
		return nil
	}))
	return snap
}

// assertNoProjectRuntimeSurface asserts none of the project-side runtime
// surfaces exists under dir.
func assertNoProjectRuntimeSurface(t *testing.T, dir string) {
	t.Helper()
	for _, name := range projectRuntimeSurfaces {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err), "%s must not be written into the project", name)
	}
}

// TestManageInstall_WritesNoRuntimeSurfaceIntoTheProject pins the ruling
// (2026-09-21): `manage install` scaffolds .ctxloom and the git-ignore of
// ctxloom's private state, and NOTHING else project-side. The engine is
// still recorded (the --engine flag names what the scaffold writes into
// config.yaml), but no engine file is written for it — for a hook-route
// engine and a file-route engine alike.
func TestManageInstall_WritesNoRuntimeSurfaceIntoTheProject(t *testing.T) {
	for _, engine := range []string{"claude-code", "mock"} {
		t.Run(engine, func(t *testing.T) {
			dir := testsupport.ProjectDir(t)
			before := topLevelEntries(t, dir)

			_, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", engine)
			require.NoError(t, err)

			assert.FileExists(t, filepath.Join(dir, ".ctxloom", "config.yaml"), "the scaffold is install's job")
			assert.FileExists(t, filepath.Join(dir, ".ctxloom", ".gitignore"), "so is the git-ignore of private state")
			assertNoProjectRuntimeSurface(t, dir)

			// The whole tree, not just the named surfaces: the only thing
			// install adds at the top level is .ctxloom.
			assert.Equal(t, append(append([]string{}, before...), ".ctxloom"), topLevelEntries(t, dir),
				"install must add exactly .ctxloom to the project's top level")
		})
	}
}

// TestManageInstall_PrintNamesNoHookApply pins the --print plan to what
// install does: a plan that still lists an apply step describes a command
// that no longer exists.
func TestManageInstall_PrintNamesNoHookApply(t *testing.T) {
	testsupport.ProjectDir(t)
	out := captureStdout(t, func() {
		_, err := runCLIErr(t, "manage", "install", "--print", "--engine", "claude-code")
		require.NoError(t, err)
	})
	assert.NotContains(t, out, "apply hooks", "the plan must not promise a hook apply install no longer performs")
	assert.NotContains(t, out, "MCP registration")
}

// TestManageUninstall_RemovesWhatAnEarlierInstallWrote pins uninstall's
// migration role: a project carrying the engine files an earlier ctxloom
// wrote (today, what the explicit `manage hooks install` still writes) has
// them removed, and the scaffold stays. The wired state is asserted BEFORE
// the removal, so the scenario starts from a project that demonstrably has
// something to remove.
func TestManageUninstall_RemovesWhatAnEarlierInstallWrote(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	// The subject is the install/uninstall ROUND TRIP over the surfaces
	// ctxloom itself writes. An admitted companion changes the CONTENT of
	// assembled context, so the two installs below cache different context
	// hashes and the diff picks up a derived cache file neither install was
	// asked about — on developer machines that happen to have a companion
	// installed, and only those. Pin the gate shut so the round trip is
	// measured, not the PATH.
	t.Cleanup(companions.AdmitNoCompanionForTesting())

	_, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", "claude-code")
	require.NoError(t, err)
	scaffolded := treeSnapshot(t, dir)

	_, err = runCLIErr(t, "manage", "hooks", "install")
	require.NoError(t, err)

	// What the project-side writer measurably added: the files uninstall
	// is asked to take back. Asserted non-empty and by name, so the removal
	// below is proven against something rather than against nothing.
	var wrote []string
	for rel, body := range treeSnapshot(t, dir) {
		if _, had := scaffolded[rel]; !had && body != "<dir>" {
			wrote = append(wrote, rel)
		}
	}
	sort.Strings(wrote)
	settings := filepath.Join(".claude", "settings.json")
	require.Contains(t, wrote, settings, "the old install's hook surface must exist before uninstall is asked to remove it; wrote: %v", wrote)

	_, err = runCLIErr(t, "manage", "uninstall")
	require.NoError(t, err)

	for _, rel := range wrote {
		assert.NoFileExists(t, filepath.Join(dir, rel), "uninstall must remove what the earlier install wrote")
	}
	assert.FileExists(t, filepath.Join(dir, ".ctxloom", "config.yaml"), "uninstall leaves the project's own ctxloom content")
}

// TestManageUninstall_OnAFreshInstallIsANoOp pins the other half of the
// migration role: a project that only ever saw the new install has nothing
// for uninstall to remove, and the tree is byte-for-byte what install left.
func TestManageUninstall_OnAFreshInstallIsANoOp(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	_, err := runCLIErr(t, "manage", "install", "--print=false", "--engine", "claude-code")
	require.NoError(t, err)
	before := treeSnapshot(t, dir)

	_, err = runCLIErr(t, "manage", "uninstall")
	require.NoError(t, err, "uninstall over nothing is a success, not a failure")

	assert.Equal(t, before, treeSnapshot(t, dir), "uninstall over a fresh install must change nothing")
}
