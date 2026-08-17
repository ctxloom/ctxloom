//go:build docker_integration

// Docker-gated end-to-end proof that a dead container's stderr is recoverable
// AFTER the container is force-removed — the whole point of streaming the tail
// instead of reaching for `docker logs` on a --rm container that no longer
// exists. Build-tagged so `just test` never compiles it; run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run StderrTailSurvivesTeardown
package isolation

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunAttached_StderrTailSurvivesTeardown proves the core claim against a
// REAL container: a process that writes a distinctive line
// to stderr and dies must have that line recoverable from the attached
// handle's StderrTail EVEN AFTER Close force-removes the container — because
// the tail was streamed into a host-side ring as the bytes arrived, not read
// back from a container that no longer exists. This guards against exactly
// the shape of failure where decisive evidence
// (e.g. "SyntaxError: Unexpected token 'with'") was reachable only via `docker
// logs` on a still-live container and vanished on teardown.
func TestRunAttached_StderrTailSurvivesTeardown(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the stderr-tail survival integration test")
	rt := SelectRuntime("docker")
	require.Equal(t, "docker", rt.Name())

	const dyingWords = "SyntaxError: Unexpected token WITH (node module loader died)"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	spec := RunSpec{
		Image:   "alpine:latest",
		Name:    containerName("stderrtail-itest"),
		WorkDir: "/",
		// Write the distinctive line to STDERR (>&2) and exit non-zero — a
		// container that dies in its "module loader" before ever speaking a
		// protocol, its only account of why on stderr.
		Command: []string{"sh", "-c", "echo '" + dyingWords + "' >&2; exit 1"},
	}

	// Belt-and-suspenders: force-remove by name on any exit path so a failed
	// assertion never leaves container debris behind.
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", spec.Name).Run()
	})

	ac, err := RunAttached(ctx, rt, spec, nil)
	require.NoError(t, err, "RunAttached must start the container")

	// The stderr is STREAMED into the host-side ring as the container writes
	// it — so it accrues while the container is still (briefly) alive.
	require.Eventually(t, func() bool {
		return strings.Contains(ac.StderrTail(), dyingWords)
	}, 15*time.Second, 100*time.Millisecond,
		"the container's dying words must stream into the tail — got %q", ac.StderrTail())

	// Force-remove the container (models teardown). `docker logs` against this
	// name now finds nothing — the exact trap this streaming design avoids.
	// Close's returned error is cmd.Wait()'s; a force-killed `docker run` CLI
	// reports "signal: killed", expected and not under test.
	_ = ac.Close()
	assert.Eventually(t, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-a",
			"--filter", "name="+spec.Name, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out)) == ""
	}, 15*time.Second, 250*time.Millisecond, "Close must force-remove the container")

	// The decisive assertion: AFTER the container is gone, its dying words are
	// STILL recoverable from the streamed tail — the property `docker logs`
	// could never give once the container was force-removed.
	assert.Contains(t, ac.StderrTail(), dyingWords,
		"the container's dying words must SURVIVE force-removal via the streamed tail")

	assert.Less(t, len(ac.StderrTail()), 2*stderrtailDefaultForTest(),
		"the tail must stay bounded")
}

// stderrtailDefaultForTest mirrors stderrtail.DefaultBytes without importing
// it into a test whose only need is a rough upper bound.
func stderrtailDefaultForTest() int { return 8192 }
