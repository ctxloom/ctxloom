package isolation

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// stateMount is a stand-in for one of the session-state mounts
// (sessionStateMounts) every container run threads in — the §6.4 mount whose
// PRESENCE the transcript-survival guard depends on.
var stateMount = Mount{
	Host:      "/home/u/.ctxloom/sessions/regal-rash-dash/persist",
	Container: "/home/ctxloom/.ctxloom/sessions/regal-rash-dash/persist",
}

// newRunnerTestWorkspace builds a containerWorkspace with the same fields
// PrepareWorkspace produces — scoped auth/TERM env, and the auth/overlay/
// session-state mounts — so buildRunnerSpec can be rendered without a runtime.
func newRunnerTestWorkspace() *containerWorkspace {
	return &containerWorkspace{
		dir:         "/proj",
		scratchRoot: "/scratch",
		socketDir:   "/scratch/sock",
		extraEnv:    []string{"ANTHROPIC_API_KEY=scoped", "TERM=xterm-256color"},
		extraMounts: []Mount{
			{Host: "/scratch/cfg0", Container: "/proj/.claude"}, // a config overlay
			stateMount, // a session-state mount (§6.4)
		},
		agentID: "builder",
	}
}

// TestBuildRunnerSpec_NoPluginTransport is the Phase 1 unit gate:
// the docker-direct runner spec renders NO plugin socket mount, NO -p publish,
// NO PLUGIN_*/magic-cookie env, runs `llm host` (not `llm serve`), rides the
// spawn env as BARE-NAME `-e` (values never in argv), and PRESERVES the
// session-state + auth + overlay mounts (§6.4). This is the spec-level proof
// that closes the peer-container hole for the delegated path.
func TestBuildRunnerSpec_NoPluginTransport(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "docker", available: true}, "mock").WithImage("img")
	cw := newRunnerTestWorkspace()
	spawnEnv := map[string]string{
		"CTXLOOM_COORD_URL":  "http://host:9000",
		"CTXLOOM_COORD_CRED": "super-secret-token",
		"CTXLOOM_RUN_ID":     "run-123",
	}

	spec := c.buildRunnerSpec("mock", "fast", "ctxloom-iso-builder-abc123", cw, spawnEnv)

	// Command: `llm host <backend> --label <l>` — the runner mode with no
	// plugin.Serve, NOT `llm serve`.
	assert.Equal(t, []string{defaultContainerBinary, "llm", "host", "mock", "--label", "fast"}, spec.Command)
	assert.NotContains(t, spec.Command, "serve", "the docker-direct runner never runs the plugin-serving `llm serve`")

	// NO plugin socket mount (neither the host scratch socket dir nor the fixed
	// in-container socket target crosses).
	for _, m := range spec.Mounts {
		assert.NotEqual(t, defaultContainerSocketDir, m.Container, "no plugin socket-dir mount target")
		assert.NotEqual(t, cw.socketDir, m.Host, "the host plugin socket scratch is never mounted")
	}

	// NO published port — the listener never exists on this path
	// (asserted on the rendered argv below; RunSpec has no port field since 0.7).

	// NO go-plugin handshake env: no magic cookie, no PLUGIN_* of any kind.
	for _, e := range spec.Env {
		key, _, _ := strings.Cut(e, "=")
		assert.NotEqual(t, pb.HandshakeConfig.MagicCookieKey, key, "no go-plugin magic cookie crosses")
		assert.False(t, strings.HasPrefix(key, "PLUGIN_"), "no PLUGIN_* handshake env crosses (%s)", e)
	}

	// The spawn env rides as BARE NAMES — the value stays on the run-process
	// env, never in this (world-readable) argv/env-list.
	assert.Contains(t, spec.Env, "CTXLOOM_COORD_CRED", "coordinator trio crosses as a bare name")
	assert.Contains(t, spec.Env, "CTXLOOM_COORD_URL")
	assert.NotContains(t, spec.Env, "CTXLOOM_COORD_CRED=super-secret-token", "the credential value never enters the spec env")

	// PRESERVED: the project mount, the scoped auth env, IS_SANDBOX, the config
	// overlay, and — the §6.4 guard — the session-state mount.
	assert.Contains(t, spec.Mounts, Mount{Host: "/proj", Container: "/ctr/proj"}, "project mount mapped through the runtime's pathMapper")
	assert.Contains(t, spec.Mounts, stateMount, "the session-state mount is preserved (transcript survival, §6.4)")
	assert.Contains(t, spec.Mounts, Mount{Host: "/scratch/cfg0", Container: "/proj/.claude"}, "config overlay preserved")
	assert.Contains(t, spec.Env, "ANTHROPIC_API_KEY=scoped", "scoped auth env preserved")
	assert.Contains(t, spec.Env, "IS_SANDBOX=1", "the container-is-the-boundary base env is preserved")

	// Rendered argv: no -p publish, no socket mount, and the trio is a bare -e.
	argv := strings.Join(Docker{rootless: true}.RunArgs(spec), " ")
	assert.NotContains(t, argv, "-p ", "no port publish rendered")
	assert.NotContains(t, argv, defaultContainerSocketDir, "no socket-dir mount rendered")
	assert.Contains(t, argv, "-e CTXLOOM_COORD_CRED ", "the trio renders as a bare-name -e")
	assert.NotContains(t, argv, "super-secret-token", "the credential value never enters the rendered argv")
}

// scriptRuntime is a Runtime whose RunArgs is a fixed script argv, so
// startDirectRunner can be exercised against a real (cheap) subprocess without
// docker — here a process that writes to stderr and exits non-zero.
type scriptRuntime struct {
	fakeRuntime
	args []string
}

func (s scriptRuntime) RunArgs(RunSpec) []string { return s.args }

// TestStartDirectRunner_StderrTailSurfacesOnExit is the failure-path gate: a
// runner that dies (the container that never dials home) surfaces its stderr
// TAIL in the Wait error — the diagnostic that replaces go-plugin's Diagnose —
// not just a bare "exit status N".
func TestStartDirectRunner_StderrTailSurfacesOnExit(t *testing.T) {
	rt := scriptRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "sh", available: true},
		args:        []string{"-c", "echo RUNNER-BOOM-DIAGNOSTIC 1>&2; exit 7"},
	}
	// Name "" so the handle's Kill remove is a no-op (no real container).
	h, err := startDirectRunner(rt, RunSpec{Name: ""}, nil)
	require.NoError(t, err, "the process must start")

	werr := h.Wait()
	require.Error(t, werr, "a non-zero exit is an error")
	assert.Contains(t, werr.Error(), "RUNNER-BOOM-DIAGNOSTIC", "the stderr tail must surface in the exit error (Diagnose replacement)")
	assert.Contains(t, werr.Error(), "exit status 7", "the underlying exit failure is still reported")
}

// TestStartDirectRunner_ContextIsNotTheTeardownHandle REFUTES a finding, which
// claimed a cancelled context "does not tear down the runner container" and
// pointed at startDirectRunner's use of exec.Command rather than
// CommandContext. The mechanism is real — the context is deliberately not
// bound to the process — but the consequence is not, and the proposed remedy
// is actively harmful:
//
//   - `docker run` here is ATTACHED and carries no --rm, so killing the run
//     CLI (all CommandContext does) leaves the CONTAINER running. It orphans
//     exactly the resource the row wants reclaimed.
//   - Teardown is Kill, which force-REMOVES the container by name first and
//     only then signals our own CLI. Every production caller registers it up
//     front (`defer runnerHandle.Kill()` in cli/run.go; run_owned.go hands
//     h.Kill to the coordinator as the run's teardown), so a cancelled context
//     unwinds the caller and Kill runs on the way out.
//
// This pins both halves: a cancelled context leaves the handle usable, and
// Kill is what issues the remove. Binding the runner's lifetime to the context
// turns it red.
func TestStartDirectRunner_ContextIsNotTheTeardownHandle(t *testing.T) {
	var removed []string
	orig := probeExec
	probeExec = func(_ context.Context, _ string, args []string) (string, error) {
		removed = append(removed, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { probeExec = orig })

	ctx, cancel := context.WithCancel(context.Background())
	rt := scriptRuntime{
		fakeRuntime: fakeRuntime{name: "docker", binary: "sh", available: true},
		args:        []string{"-c", "sleep 0.4"},
	}
	c := NewContainerFor(rt, "mock").WithImage("img")
	cw := newRunnerTestWorkspace()

	h, err := c.StartRunner(ctx, "mock", "", 0, cw, nil)
	require.NoError(t, err)

	cancel()

	// The run process must OUTLIVE the cancelled context. Binding it to ctx
	// (exec.CommandContext) kills this CLI here — and killing an attached
	// `docker run` that carries no --rm leaves the CONTAINER behind, orphaning
	// the very resource the row wanted reclaimed. A clean exit proves the
	// context is not the lifetime.
	assert.NoError(t, h.Wait(), "the runner is not torn down by a cancelled context")
	assert.Empty(t, removed, "and a cancelled context never reclaims the container either")

	// Kill is the teardown, and it force-REMOVES by name before signalling our
	// own CLI — which is why it, not the context, is what production defers.
	require.NotNil(t, h.Kill, "the handle stays usable after the context is cancelled")
	h.Kill()
	require.Len(t, removed, 1, "Kill force-removes the container by name — this is the teardown")
	assert.Equal(t, strings.Join(rt.RemoveArgs(h.Name), " "), removed[0])
}

// TestRemoveContainer_SurvivingContainerWarnsWithAManualRemove REFUTES
// a finding, which claimed RunnerHandle.Kill being `func()` means "a container
// that survives `rm -f` is only a stderr warning; the caller has no way to
// know". The mechanism is true — Kill cannot return an error — but the
// consequence assumes the caller is who needs to know, and no caller can act:
// every production call site is a teardown defer on the way out of a run
// (cli/run.go) or the func() the coordinator holds as a run's teardown
// (run_owned.go), and both would discard an error. The party who CAN act is
// the human, and the warning already names the container, the daemon's reason
// and a copy-pasteable remove command.
//
// This pins that warning. Replacing it with a returned error every caller
// drops turns it red.
func TestRemoveContainer_SurvivingContainerWarnsWithAManualRemove(t *testing.T) {
	buf := captureWarnings(t)
	orig := probeExec
	probeExec = func(context.Context, string, []string) (string, error) {
		return "", errors.New("daemon is wedged")
	}
	t.Cleanup(func() { probeExec = orig })

	removeContainer(context.Background(), Docker{}, "ctxloom-iso-member-7")

	warning := buf.String()
	assert.Contains(t, warning, "ctxloom-iso-member-7", "the surviving container is named")
	assert.Contains(t, warning, "daemon is wedged", "the daemon's own reason survives")
	assert.Contains(t, warning, "remove it manually", "the human is told what to do")
	assert.Contains(t, warning, "rm", "and given the command to do it with")
}

// TestRemoveContainer_AlreadyGoneIsNotALeak: a racing --rm that reports the
// container already gone is teardown SUCCESS, not a leak, so it must not draw
// the warning above.
func TestRemoveContainer_AlreadyGoneIsNotALeak(t *testing.T) {
	buf := captureWarnings(t)
	orig := probeExec
	// removeReportsGone reads the daemon's stderr off an *exec.ExitError, so
	// the already-gone race must be presented the way the real probeExec
	// surfaces it.
	probeExec = func(context.Context, string, []string) (string, error) {
		return "", &exec.ExitError{Stderr: []byte("Error response from daemon: No such container: ctxloom-iso-x")}
	}
	t.Cleanup(func() { probeExec = orig })

	removeContainer(context.Background(), Docker{}, "ctxloom-iso-x")
	assert.Empty(t, buf.String(), "already-gone is teardown success")
}
