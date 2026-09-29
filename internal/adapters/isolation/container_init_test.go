package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunArgs_EveryContainerRuntimeAsksForAnInit pins that both shipped OCI
// runtimes put a real init at PID 1.
//
// This is asserted rather than left to review because the failure it guards is
// SILENT and remote from its cause: without an init the image entrypoint's
// `exec "$@"` makes the engine PID 1, an engine that does not reap orphans
// leaves zombies, a zombie still answers signal 0 so pidalive.Probe calls it
// ALIVE, its stale discovery marker is never reaped, and the NEXT `ctxloom mcp`
// in that cell refuses to start. Nothing in that chain says "the init flag is
// missing" — it presents as an unrelated MCP startup refusal two scenarios
// later.
//
// It covers both runtimes deliberately: the flag resolves to a DIFFERENT binary
// on each (docker-init/tini vs catatonit), so "docker was fine" says nothing
// about podman.
func TestRunArgs_EveryContainerRuntimeAsksForAnInit(t *testing.T) {
	spec := RunSpec{Name: "ctxloom-iso-test"}

	for _, rt := range []struct {
		name string
		args []string
	}{
		{"docker-rootless", mustRunArgs(t, Docker{rootless: true}, spec)},
		{"docker-rootful", mustRunArgs(t, Docker{rootless: false}, spec)},
		{"podman-rootless", mustRunArgs(t, Podman{rootless: true}, spec)},
		{"podman-rootful", mustRunArgs(t, Podman{rootless: false}, spec)},
	} {
		t.Run(rt.name, func(t *testing.T) {
			require.NotEmpty(t, rt.args, "a container runtime must render a run argv")
			assert.Equal(t, "run", rt.args[0], "the argv is a `run` invocation")
			assert.Contains(t, rt.args, "--init",
				"%s must ask the runtime for an init at PID 1; without one the engine becomes PID 1 and never reaps orphans", rt.name)
		})
	}
}
