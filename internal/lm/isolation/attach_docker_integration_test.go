//go:build docker_integration

// ISO1's docker-gated proof of RunAttached + ExecSpec: the plain-stdio
// container primitive the ACP client driver's container transport
// (internal/acp) uses instead of SpawnClient's go-plugin-over-socket
// transport. Build-tagged so `just test` never compiles it (the gate stays
// green without docker); run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run RunAttached
package isolation

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// TestRunAttached_SamePathMountAndTeardown proves ISO1's invariant 1
// (same-path workspace mount) and RunAttached's teardown, independent of any
// engine: alpine:latest already exists on any host that ran the package's
// other docker-gated tests (or is trivially pullable), so this needs no
// custom image build. A marker file written into the HOST project dir
// (PrepareWorkspace's identical-path mount) is read back by `cat` running
// INSIDE the container at the exact same absolute path — proving the mount
// resolves, not just that the container started.
func TestRunAttached_SamePathMountAndTeardown(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container attach integration test")

	rt := SelectRuntime("docker")
	require.Equal(t, "docker", rt.Name())

	projectDir := t.TempDir()
	marker := filepath.Join(projectDir, "iso1-marker.txt")
	const markerContent = "iso1-same-path-proof"
	require.NoError(t, os.WriteFile(marker, []byte(markerContent), 0o644))

	pol := NewContainerFor(rt, "mock").WithImage("alpine:latest")
	// alpine:latest resolves NO engine, so ANY spec's auth requirement
	// would be a false gate here — this test proves the transport/mount
	// primitive only, so bypass PrepareWorkspace's auth gate entirely by
	// building the RunSpec directly instead of through the Container policy
	// (which is exactly what a caller with its own resolved auth, like the
	// real ACP container transport, layers on top of this primitive).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	spec := RunSpec{
		Image:   pol.image,
		Name:    containerName("attach-itest"),
		WorkDir: projectDir,
		Command: []string{"cat", marker},
		Mounts:  []Mount{rt.ExposeIdentical(projectDir, false)},
	}

	ac, err := RunAttached(ctx, rt, spec, nil)
	require.NoError(t, err, "RunAttached must start the container")
	t.Cleanup(func() { _ = ac.Close() })

	out, err := readAllWithDeadline(ac.Stdout, 10*time.Second)
	require.NoError(t, err, "read the container's stdout")
	assert.Equal(t, markerContent, strings.TrimSpace(string(out)),
		"cat inside the container must read the SAME bytes written on the host at the SAME absolute path — the same-path mount invariant")

	require.NoError(t, ac.Close())

	assert.Eventually(t, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-a",
			"--filter", "name="+spec.Name, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out)) == ""
	}, 15*time.Second, 250*time.Millisecond, "Close must force-remove the container")
}

// readAllWithDeadline reads r to EOF (the container's stdout closes when
// `cat` exits) or fails the read after timeout — avoids a hung test if the
// container never produces output.
func readAllWithDeadline(r interface{ Read([]byte) (int, error) }, timeout time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, err := r.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if err != nil {
				if errors.Is(err, io.EOF) {
					done <- result{data: buf}
					return
				}
				done <- result{data: buf, err: err}
				return
			}
		}
	}()
	select {
	case res := <-done:
		return res.data, res.err
	case <-time.After(timeout):
		return nil, context.DeadlineExceeded
	}
}
