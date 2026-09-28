package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// describedEnvironment stands in for the preview's environment: only its
// Describe is read by the plan.
type describedEnvironment struct {
	isolation.Environment
	desc isolation.Description
}

func (e describedEnvironment) Describe() isolation.Description { return e.desc }

// TestProbedEnvironment_NoRuntimeShowsUnavailableAndUnknown: a preview whose
// requested runtime is unreachable names the refusal in both output forms —
// runtime unavailable, reach unknown — never the host it would fall back to.
func TestProbedEnvironment_NoRuntimeShowsUnavailableAndUnknown(t *testing.T) {
	cell := launch.Cell{Handle: describedEnvironment{desc: isolation.Description{
		Workspace: "shared", Runtime: isolation.RuntimeUnavailable, Reach: isolation.ReachUnknown,
	}}}
	env := probedEnvironment(cell)
	require.Equal(t, &environmentJSON{Runtime: "unavailable", Reach: "unknown"}, env)

	wire, err := json.Marshal(dryRunJSON{Environment: env})
	require.NoError(t, err)
	assert.Contains(t, string(wire), `"environment":{"runtime":"unavailable","reach":"unknown"}`)

	var buf bytes.Buffer
	printEnvironment(&buf, env)
	assert.Equal(t, "=== Environment ===\nruntime: unavailable, reach: unknown\n", buf.String())

	assert.Nil(t, probedEnvironment(launch.Cell{}), "a cell the adapter did not make describes nothing")
}

// TestDryRun_HostPreviewShowsTheHostEnvironment: a plain dry run previews the
// host environment and says so, in JSON and in text.
func TestDryRun_HostPreviewShowsTheHostEnvironment(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got), "payload: %s", res.out)
	require.NotNil(t, got.Environment, "the plan carries the probed environment")
	assert.Equal(t, environmentJSON{Runtime: "host", Reach: "loopback"}, *got.Environment)

	res = runCLI(t, "run", "--dry-run", "--format", "text", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	assert.Contains(t, res.stdout, "=== Environment ===\nruntime: host, reach: loopback\n")
}
