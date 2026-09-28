package isolation

import (
	"context"
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
// no handoff file and no listener. The reach-back trio rides the run
// PROCESS's env as values, never its argv.
func TestInteractiveRunner_Container_IsTheForegroundRunnerOnATTY(t *testing.T) {
	// The real docker renderer, so the argv is the one a daemon would see.
	c := NewContainerFor(Docker{rootless: true}, "mock").WithImage("img")
	cw := newRunnerTestWorkspace()
	spawnEnv := map[string]string{"CTXLOOM_COORD_URL": "http://host:9000", "CTXLOOM_COORD_CRED": "super-secret-token", "CTXLOOM_RUN_ID": "run-123"}

	cmd, name, err := c.interactiveRunner(context.Background(), "mock", cw, spawnEnv)
	require.NoError(t, err)
	require.NotEmpty(t, name, "the container is named so teardown can target it")
	assert.Equal(t, "docker", filepath.Base(cmd.Path))
	argv := strings.Join(cmd.Args, " ")
	assert.Contains(t, argv, " run ")
	assert.Contains(t, argv, "--name "+name)
	assert.Contains(t, argv, " -i -t ", "attached to a terminal: the runner's stdio is the tty")
	assert.True(t, strings.HasSuffix(argv, " "+defaultContainerBinary+" runner mock"), "the foreground process is the runner: %s", argv)
	assert.NotContains(t, argv, "exec", "no exec-into")
	assert.NotContains(t, argv, "--start", "no handoff file")
	assert.NotContains(t, argv, "-p ", "no published port")
	assert.NotContains(t, argv, "super-secret-token", "the credential never rides the argv")
	assert.Contains(t, argv, "-e CTXLOOM_COORD_CRED ", "the trio crosses as a bare name")
	assert.Contains(t, strings.Join(cmd.Env, "\n"), "CTXLOOM_COORD_CRED=super-secret-token", "its value rides the run process's env")
	assert.Contains(t, argv, "source="+stateMount.Host+",target="+stateMount.Container, "the session-state mount is preserved")
	assert.True(t, strings.LastIndex(argv, "-e TERM="+RunnerTerm) > strings.LastIndex(argv, "-e TERM=xterm-256color"), "the runner runs under RunnerTerm, after the workspace's TERM: %s", argv)
}

// TestInteractiveRunner_Host_IsTheSelfExecdRunner: a host cell's interactive
// runner is the same binary self-exec'd as `runner <engine>`, with the trio on
// its env; there is no container to name.
func TestInteractiveRunner_Host_IsTheSelfExecdRunner(t *testing.T) {
	for _, p := range []policy{None{}, Worktree{}} {
		cmd, name, err := p.interactiveRunner(context.Background(), "mock", hostWorkspace{dir: "/proj"}, map[string]string{"CTXLOOM_RUN_ID": "run-1"})
		require.NoError(t, err, p.Name())
		assert.Empty(t, name, "%s: nothing to remove by name", p.Name())
		assert.Equal(t, []string{"runner", "mock"}, cmd.Args[1:], p.Name())
		assert.Contains(t, strings.Join(cmd.Env, "\n"), "CTXLOOM_RUN_ID=run-1", p.Name())
		assert.Contains(t, strings.Join(cmd.Env, "\n"), "TERM="+RunnerTerm, p.Name())
	}
}
