package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	containerfiles "github.com/ctxloom/ctxloom/container"
)

// entrypointRun executes the EMBEDDED entrypoint script under /bin/sh with a
// shim-only PATH: each shim (id, groupmod, usermod, gosu, setpriv, …) is a
// tiny sh script, so every identity branch — remap, drop, refusal — runs for
// real without root or a container. shims maps name → script body; env is
// the extra environment (PUID/PGID/CTXLOOM_ALLOW_ROOT).
func entrypointRun(t *testing.T, shims map[string]string, env []string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-sh entrypoint harness")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "entrypoint.sh")
	require.NoError(t, os.WriteFile(script, containerfiles.Entrypoint, 0o755))
	shimDir := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(shimDir, 0o755))
	for name, body := range shims {
		require.NoError(t, os.WriteFile(filepath.Join(shimDir, name), []byte("#!/bin/sh\n"+body), 0o755))
	}

	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	cmd.Env = append([]string{"PATH=" + shimDir}, env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else {
		require.NoError(t, err)
	}
	return out.String(), errb.String(), code
}

// shimLog returns a shim body that appends its argv to logFile.
func shimLog(logFile, then string) string {
	return "echo \"$@\" >> " + logFile + "\n" + then
}

// rootID is the id shim for a container started as root.
const rootID = "echo 0"

// TestEntrypoint_RemapOKDropsViaGosu: the standard locally-built-image path —
// remap succeeds, gosu drops to the (now remapped) named ctxloom user.
func TestEntrypoint_RemapOKDropsViaGosu(t *testing.T) {
	dir := t.TempDir()
	gosuLog := filepath.Join(dir, "gosu.log")
	stdout, _, code := entrypointRun(t, map[string]string{
		"id":       rootID,
		"groupmod": "exit 0",
		"usermod":  "exit 0",
		"gosu":     shimLog(gosuLog, "shift\nexec \"$@\""),
		"setpriv":  "echo setpriv-must-not-run >&2; exit 1",
	}, []string{"PUID=1234", "PGID=4321"}, "/bin/echo", "done")

	assert.Equal(t, 0, code)
	assert.Equal(t, "done\n", stdout)
	log, err := os.ReadFile(gosuLog)
	require.NoError(t, err)
	assert.Contains(t, string(log), "ctxloom /bin/echo done", "gosu drops to the named remapped user")
}

// TestEntrypoint_FailedRemapPrefersSetprivNumeric: when the remap FAILS (base
// without usermod/groupmod), gosu would drop to the UN-remapped 1000:1000 —
// only coincidentally the launching user — so the numeric-id setpriv path,
// which is immune to a failed remap, must win over a present gosu.
func TestEntrypoint_FailedRemapPrefersSetprivNumeric(t *testing.T) {
	dir := t.TempDir()
	gosuLog := filepath.Join(dir, "gosu.log")
	setprivLog := filepath.Join(dir, "setpriv.log")
	stdout, stderr, code := entrypointRun(t, map[string]string{
		"id":       rootID,
		"groupmod": "exit 0",
		"usermod":  "exit 1", // remap fails
		"gosu":     shimLog(gosuLog, "shift\nexec \"$@\""),
		"setpriv":  shimLog(setprivLog, "shift 5\nexec \"$@\""),
	}, []string{"PUID=1234", "PGID=4321"}, "/bin/echo", "done")

	assert.Equal(t, 0, code)
	assert.Equal(t, "done\n", stdout)
	assert.Contains(t, stderr, "remapping ctxloom", "the failed remap is announced")
	_, err := os.Stat(gosuLog)
	assert.True(t, os.IsNotExist(err), "gosu must not run after a failed remap (it would drop to un-remapped 1000:1000)")
	log, err := os.ReadFile(setprivLog)
	require.NoError(t, err)
	assert.Contains(t, string(log), "--reuid 1234 --regid 4321", "setpriv gets the numeric launching ids directly")
}

// TestEntrypoint_RefusesRootAfterFailedRemapWithOnlyGosu: failed remap + gosu
// but no setpriv — there is no way to BECOME the requested identity, and
// running as root would root-own files in the mounted project. Refuse.
func TestEntrypoint_RefusesRootAfterFailedRemapWithOnlyGosu(t *testing.T) {
	dir := t.TempDir()
	gosuLog := filepath.Join(dir, "gosu.log")
	stdout, stderr, code := entrypointRun(t, map[string]string{
		"id":       rootID,
		"groupmod": "exit 0",
		"usermod":  "exit 1",
		"gosu":     shimLog(gosuLog, "shift\nexec \"$@\""),
	}, []string{"PUID=1234"}, "/bin/echo", "done")

	assert.NotEqual(t, 0, code, "a wrong-identity start must FAIL the launch, not proceed")
	assert.Empty(t, stdout, "the engine never runs")
	assert.Contains(t, stderr, "refusing to run the engine as root")
	assert.Contains(t, stderr, "CTXLOOM_ALLOW_ROOT", "the refusal names the escape hatch")
	_, err := os.Stat(gosuLog)
	assert.True(t, os.IsNotExist(err))
}

// TestEntrypoint_RefusesRootWithoutDropHelpers: PUID requested but the base
// has neither gosu nor setpriv — refuse loudly (the old behavior ran the
// engine as root behind a buried warning).
func TestEntrypoint_RefusesRootWithoutDropHelpers(t *testing.T) {
	stdout, stderr, code := entrypointRun(t, map[string]string{
		"id":       rootID,
		"groupmod": "exit 0",
		"usermod":  "exit 0",
	}, []string{"PUID=1234"}, "/bin/echo", "done")

	assert.NotEqual(t, 0, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "refusing to run the engine as root")
}

// TestEntrypoint_AllowRootEscapeHatch: CTXLOOM_ALLOW_ROOT=1 (set by the
// isolation runtime in --degraded mode) downgrades the refusal to the old
// warn-and-run-as-root behavior — degraded is the one warn-and-continue home.
func TestEntrypoint_AllowRootEscapeHatch(t *testing.T) {
	stdout, stderr, code := entrypointRun(t, map[string]string{
		"id":       rootID,
		"groupmod": "exit 0",
		"usermod":  "exit 0",
	}, []string{"PUID=1234", "CTXLOOM_ALLOW_ROOT=1"}, "/bin/echo", "done")

	assert.Equal(t, 0, code)
	assert.Equal(t, "done\n", stdout)
	assert.Contains(t, stderr, "running as root", "the fallback still warns")
}

// TestEntrypoint_NoPUIDRunsAsIs: rootless docker passes no PUID — container
// root IS the launching user host-side, so the run proceeds as root with no
// remap, no refusal, no helpers needed.
func TestEntrypoint_NoPUIDRunsAsIs(t *testing.T) {
	stdout, stderr, code := entrypointRun(t, map[string]string{"id": rootID}, nil, "/bin/echo", "done")
	assert.Equal(t, 0, code)
	assert.Equal(t, "done\n", stdout)
	assert.Empty(t, stderr)
}

// TestEntrypoint_NonRootExecsDirectly: started non-root (a user's own --user
// override) there is nothing to remap and nothing to refuse.
func TestEntrypoint_NonRootExecsDirectly(t *testing.T) {
	stdout, stderr, code := entrypointRun(t, map[string]string{"id": "echo 1000"}, []string{"PUID=1234"}, "/bin/echo", "done")
	assert.Equal(t, 0, code)
	assert.Equal(t, "done\n", stdout)
	assert.Empty(t, stderr)
}
