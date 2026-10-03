package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInteractiveRunner_Container_IsTheForegroundRunnerOnATTY: a
// container-cell INTERACTIVE launch runs `ctxloom runner <engine>` as the
// container's foreground process attached to a terminal — `docker run -i -t`
// on the same spec every container runner gets (the session-state, auth and
// overlay mounts, the bare-name spawn env) — with no keepalive to exec into,
// no handoff file and no listener. The reach-back rides the run PROCESS's env
// as values, never its argv — and the credential not even there: it is in the
// run's read-only secret dir, and only its file's name crosses.
func TestInteractiveRunner_Container_IsTheForegroundRunnerOnATTY(t *testing.T) {
	// The real docker renderer, so the argv is the one a daemon would see.
	c := NewContainerFor(Docker{rootless: true}, "mock").WithImage("img")
	cw := newRunnerTestWorkspace()
	sec, err := newOwnedScratch(t.TempDir(), secretScratchPrefix)
	require.NoError(t, err)
	t.Cleanup(sec.release)
	cw.secrets = sec
	spawnEnv := map[string]string{"CTXLOOM_COORD_URL": "http://host:9000", "CTXLOOM_COORD_CRED": "super-secret-token", "CTXLOOM_RUN_ID": "run-123"}

	cmd, name, err := c.interactiveRunner(context.Background(), "mock", cw, spawnEnv)
	require.NoError(t, err)
	require.NotEmpty(t, name, "the container is named so teardown can target it")
	assert.Equal(t, "docker", strings.TrimSuffix(filepath.Base(cmd.Path), ".exe"), "the runtime binary, however PATH resolved it")
	argv := strings.Join(cmd.Args, " ")
	assert.Contains(t, argv, " run ")
	assert.Contains(t, argv, "--name "+name)
	assert.Contains(t, argv, " -i -t ", "attached to a terminal: the runner's stdio is the tty")
	assert.True(t, strings.HasSuffix(argv, " "+defaultContainerBinary+" runner mock"), "the foreground process is the runner: %s", argv)
	assert.NotContains(t, argv, "exec", "no exec-into")
	assert.NotContains(t, argv, "--start", "no handoff file")
	assert.NotContains(t, argv, "-p ", "no published port")
	assert.NotContains(t, argv, "super-secret-token", "the credential never rides the argv")
	assert.NotContains(t, argv, "-e CTXLOOM_COORD_CRED ", "the credential does not cross as an env var")
	assert.Contains(t, argv, "-e CTXLOOM_COORD_CRED_FILE ", "the name of its secret file does, as a bare name")
	assert.NotContains(t, strings.Join(cmd.Env, "\n"), "super-secret-token", "nor does its value ride the run process's env")
	got, err := os.ReadFile(filepath.Join(sec.dir, "CTXLOOM_COORD_CRED"))
	require.NoError(t, err)
	assert.Equal(t, "super-secret-token", string(got), "the runner reads it back from the secret file")
	assert.Contains(t, argv, "source="+stateMount.Host+",target="+stateMount.Container, "the session-state mount is preserved")
	assert.True(t, strings.LastIndex(argv, "-e TERM="+RunnerTerm) > strings.LastIndex(argv, "-e TERM=xterm-256color"), "the runner runs under RunnerTerm, after the workspace's TERM: %s", argv)
}
