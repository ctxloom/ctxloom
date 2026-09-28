//go:build docker_integration

package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// The git-enabled image and the raw `docker run` the worktree-in-container
// proofs (container_hostworktree_integration_test.go) drive: they prove the
// git/config behavior of the SAME mount composition the policy builds,
// independent of the runner.

const worktreeIntegrationImage = "ctxloom-iso-wt-itest:latest"

// dockerRun runs `docker run --rm` for the given image with the given identical-path
// mounts and workdir, returning the combined output and error. (Rootless docker
// maps container-root to the host user, so files it creates in the mounted
// worktree are host-user-owned and the teardown can remove them.)
func dockerRun(ctx context.Context, image, workDir string, mounts []mount, args ...string) (string, error) {
	full := []string{"run", "--rm"}
	for _, m := range mounts {
		// Mirror the policy's render (renderRunSpec): --mount type=bind, not -v.
		opt := "type=bind,source=" + m.Host + ",target=" + m.Container
		if m.ReadOnly {
			opt += ",readonly"
		}
		full = append(full, "--mount", opt)
	}
	full = append(full, "-w", workDir, image)
	full = append(full, args...)
	out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput()
	return string(out), err
}

// buildGitIntegrationImage builds a git-enabled minimal image (static linux ctxloom
// on alpine + git): git proves the gitdir mount composition. Rebuilt each run so
// the test exercises the current tree.
func buildGitIntegrationImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	// Target the HOST arch: `FROM alpine:latest` resolves the host's arch, so a
	// hardcoded GOARCH=amd64 binary would `exec format error` on an arm64 host.
	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-buildvcs=false", "-ldflags", testsupport.TestBinaryLDFlags, "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	// TestMain sandboxes the cwd away from the module, so the nested build
	// runs from the repo root where go.mod is.
	root, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static ctxloom: %v\n%s", err, out)
	}

	dockerfile := "FROM alpine:latest\nRUN apk add --no-cache git\nCOPY ctxloom /usr/local/bin/ctxloom\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))

	img := exec.Command("docker", "build", "-t", worktreeIntegrationImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build git-enabled image: %v\n%s", err, out)
	}
}
