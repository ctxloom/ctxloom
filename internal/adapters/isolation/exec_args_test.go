package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExecArgs_Docker renders `exec -i -t <name> …` with bare `-e NAME` env
// forwarding (values NEVER on the argv — the same never-on-cmdline discipline
// buildRunnerSpec/renderRunSpec use) and the in-container command tail.
func TestExecArgs_Docker(t *testing.T) {
	args := Docker{}.ExecArgs(
		"ctxloom-iso-m-abc",
		true,
		[]string{"CTXLOOM_COORD_URL", "CTXLOOM_SESSION_HARP"},
		[]string{"ctxloom", "llm", "turn", "mock", "--start", "/home/ctxloom/.ctxloom/sessions/h/persist/runstart.json"},
	)
	joined := strings.Join(args, " ")

	assert.Equal(t, "exec", args[0], "exec, not run")
	assert.Equal(t, []string{"exec", "-i", "-t"}, args[:3], "interactive TTY head")
	assert.Contains(t, joined, "-e CTXLOOM_COORD_URL", "coord url forwarded by NAME")
	assert.Contains(t, joined, "-e CTXLOOM_SESSION_HARP", "harp forwarded by NAME")
	assert.NotContains(t, joined, "=", "no KEY=VAL value ever lands on the exec argv")
	// name precedes the in-container command.
	assert.Equal(t,
		[]string{"ctxloom-iso-m-abc", "ctxloom", "llm", "turn", "mock", "--start", "/home/ctxloom/.ctxloom/sessions/h/persist/runstart.json"},
		args[len(args)-7:], "container name then in-container argv")
}

// TestExecArgs_NoTTY omits -t for a non-interactive exec (the `exec -i` form a
// structured/oneshot fallback would use).
func TestExecArgs_NoTTY(t *testing.T) {
	args := Docker{}.ExecArgs("c", false, nil, []string{"ctxloom", "llm", "turn", "mock"})
	assert.Equal(t, []string{"exec", "-i"}, args[:2], "no -t without a tty")
	assert.NotContains(t, args, "-t")
}

// TestExecArgs_PodmanMatchesDocker: podman's exec CLI grammar is docker-
// compatible, so the two render identically (no SDK, plain CLI on both).
func TestExecArgs_PodmanMatchesDocker(t *testing.T) {
	name := "ctxloom-iso-m-xyz"
	env := []string{"CTXLOOM_COORD_CRED"}
	cmd := []string{"ctxloom", "llm", "turn", "claude"}
	assert.Equal(t,
		Docker{}.ExecArgs(name, true, env, cmd),
		Podman{}.ExecArgs(name, true, env, cmd),
		"docker and podman exec argv are identical")
}

// TestExecArgs_HostNoop: the Host runtime launches no container, so it has no
// exec surface (nil), same as its RunArgs/RemoveArgs noops.
func TestExecArgs_HostNoop(t *testing.T) {
	assert.Nil(t, Host{}.ExecArgs("c", true, nil, []string{"ctxloom"}))
}
