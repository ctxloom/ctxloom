//go:build docker_integration

// The live-container proof of the container lock-path fix (RULED,
// human — see statemounts.go's sessionStateMounts doc, the ~/.ctxloom/locks
// paragraph). A statemounts_test.go unit test already pins the pure path
// arithmetic without a docker daemon; this file is the actual cross-boundary
// proof alongside this package's other docker-gated tests (one of which
// covers the same shape one level down — same-path project mount instead of
// the locks-dir mount). Build-tagged so
// `just test` never compiles it; run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run TestContainerLockMount_
package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// TestContainerLockMount_HostAndContainerReadSameLockFile proves the fix end
// to end: a lock sidecar the HOST writes via filelock.HomePathFor for an
// identical-path engine-settings file is readable INSIDE the container at
// the path the CONTAINER's own filelock.HomePathFor resolves to (its $HOME
// is defaultContainerHome, the same -e HOME=... every real container run
// sets — runtime.go's renderRunSpec). Before the fix that path was under a
// locks dir nothing mounted; after it, the two names collide on the SAME
// bind-mounted directory, so `cat` inside the container reads back exactly
// what the host wrote — proof the boundary is closed, not just that the
// path arithmetic on paper agrees.
func TestContainerLockMount_HostAndContainerReadSameLockFile(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container lock-path mount integration test")

	rt := SelectRuntime("docker")
	require.Equal(t, "docker", rt.Name())

	testsupport.Isolate(t) // fake $HOME: filelock/paths resolve here, never the real user's ~/.ctxloom
	projectDir := t.TempDir()
	// An engine-settings file at the SAME absolute path a container's
	// identical-path project bind mount would give it — the scenario the
	// hole lived in (containerConfigOverlay's targets).
	protected := filepath.Join(projectDir, ".claude", "settings.json")

	c := NewContainerFor(rt, "mock").WithImage("alpine:latest")
	mounts, err := c.sessionStateMounts()
	require.NoError(t, err)

	var lockMount Mount
	var found bool
	for _, m := range mounts {
		if strings.HasSuffix(m.Container, filepath.Join(".ctxloom", "locks")) {
			lockMount = m
			found = true
		}
	}
	require.True(t, found, "sessionStateMounts must carry the locks-dir mount")

	// The host side: filelock's real resolver, the same one every
	// ctxloom-family binary calls before read-modify-writing a foreign
	// engine-settings file.
	hostLockPath, err := filelock.HomePathFor(protected)
	require.NoError(t, err)
	unlock, err := filelock.Lock(hostLockPath)
	require.NoError(t, err)
	const proof = "host-side-lock-proof"
	require.NoError(t, os.WriteFile(hostLockPath, []byte(proof), 0o644))
	unlock()

	// The container side: same basename (flattening depends only on the
	// protected path, never on $HOME — see flattenLockName's doc), under
	// the mount's CONTAINER target.
	containerLockPath := filepath.Join(lockMount.Container, filepath.Base(hostLockPath))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	spec := RunSpec{
		Image:   c.image,
		Name:    containerName("lockmount-itest"),
		WorkDir: "/",
		Env:     []string{"HOME=" + defaultContainerHome},
		Command: []string{"cat", containerLockPath},
		Mounts:  []Mount{lockMount},
	}

	ac, err := RunAttached(ctx, rt, spec, nil)
	require.NoError(t, err, "RunAttached must start the container")
	t.Cleanup(func() { _ = ac.Close() })

	out, err := readAllWithDeadline(ac.Stdout, 10*time.Second)
	require.NoError(t, err, "read the container's stdout")
	assert.Equal(t, proof, strings.TrimSpace(string(out)),
		"the container, reading at the path its OWN filelock.HomePathFor resolves to, must see the exact bytes the host wrote under ITS OWN HomePathFor — same protected path, same lock file, both sides of the boundary")
}
