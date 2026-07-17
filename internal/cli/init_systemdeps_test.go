package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBinDir builds a directory containing only symlinks to the REAL
// binaries named, so checkSystemDeps' exec.LookPath probes see exactly (and
// only) those binaries on PATH — a hermetic, no-mocking way to drive both the
// present and absent branches of a PATH-based dependency check.
func fakeBinDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		real, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("test environment has no %s on PATH to symlink from", name)
		}
		require.NoError(t, os.Symlink(real, filepath.Join(dir, name)))
	}
	return dir
}

// TestCheckSystemDeps_GitMissing_FailsLoud pins the new hard-block gate: a
// machine with no git on PATH must fail loud, naming git and a fix, BEFORE
// init ever reaches the clone step that would otherwise surface a raw,
// unguided "executable file not found" error.
func TestCheckSystemDeps_GitMissing_FailsLoud(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty: no git, no ssh-keygen, no docker/podman

	err := checkSystemDeps()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git")
	assert.Contains(t, err.Error(), "ctxloom init", "the fix must tell the user to re-run init")
}

// TestCheckSystemDeps_GitPresent_MissingExtrasWarnButDoNotBlock: with git on
// PATH but ssh-keygen and a container runtime absent, checkSystemDeps must
// still succeed (nil) — those two are informational-only, needed by LATER
// phases, not by PRIME itself — while still surfacing a warning for each so
// the user sees the full picture up front.
func TestCheckSystemDeps_GitPresent_MissingExtrasWarnButDoNotBlock(t *testing.T) {
	dir := fakeBinDir(t, "git")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps()
	})

	require.NoError(t, err, "missing ssh-keygen/container runtime must not block init")
	assert.Contains(t, stderr, "ssh-keygen")
	assert.Contains(t, stderr, "container runtime")
}

// TestCheckSystemDeps_AllPresent_Succeeds is the control case: with git,
// ssh-keygen on PATH (this dev/CI environment's real ones), the git+ssh-keygen
// half of the gate is silent — this only pins that having them present never
// itself trips an error.
func TestCheckSystemDeps_AllPresent_Succeeds(t *testing.T) {
	dir := fakeBinDir(t, "git", "ssh-keygen")
	t.Setenv("PATH", dir)

	err := checkSystemDeps()
	require.NoError(t, err)
}
