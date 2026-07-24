package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// stateMount is a stand-in for one of the session-state mounts
// (sessionStateMounts) every container run threads in — the §6.4 mount whose
// PRESENCE the queer-shrug transcript-survival guard depends on.
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

// TestBuildRunnerSpec_NoPluginTransport is the queer-shrug Phase 1 unit gate:
// the docker-direct runner spec renders NO plugin socket mount, NO -p publish,
// NO PLUGIN_*/magic-cookie env, runs `llm host` (not `llm serve`), rides the
// spawn env as BARE-NAME `-e` (values never in argv), and PRESERVES the
// session-state + auth + overlay mounts (§6.4). This is the spec-level proof
// that closes the mauve-state peer-container hole for the delegated path.
func TestBuildRunnerSpec_NoPluginTransport(t *testing.T) {
	c := NewContainer(fakeRuntime{name: "docker", binary: "docker", available: true}, "img")
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

	// NO published port — the mauve-state listener never exists on this path
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
	assert.Contains(t, spec.Mounts, Mount{Host: "/proj", Container: "/proj"}, "identical-path project mount")
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
