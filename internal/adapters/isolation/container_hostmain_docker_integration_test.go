//go:build docker_integration

// The host base over a MAIN checkout, against a real daemon and real git. Run
// with:
//
//	just test-pkg ./internal/adapters/isolation/ -tags docker_integration -count=1 -run HostBaseMainCheckout
//
// A main checkout's .git rides the read-write project mount, so its hooks/
// would be writable from the container and then run ON THE HOST.
// gitdirMirrorMounts shadows hooks/ read-only (unit-tested in
// container_test.go); this proves the daemon honours the shadow while the
// checkout's own index and HEAD stay writable enough to commit.
package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContainerPolicy_HostBaseMainCheckout_HooksReadOnlyCommitsWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		dockergate.SkipCapability(t, "git not on PATH, and the host-base main-checkout test needs a real repo")
	}
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the host-base main-checkout integration test")
	primary := daemonFixtures(t, "docker")
	// Rootless only: the container writes the host's .git as the launching
	// user there, so the host-user teardown can remove what it wrote.
	rt := ProbeRuntime("docker")
	if d, ok := rt.(Docker); !ok || !d.rootless {
		dockergate.SkipCapability(t, "rootful docker root-owns files the host-user teardown cannot remove; needs rootless docker")
	}
	buildGitIntegrationImage(t)
	registerVendorlessFixture(t, "mock", engine.DistributionTestOnly) // see the out-of-repo worktree test

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	repo := initRealRepo(t) // a MAIN checkout: .git is a directory inside it
	gitDir := filepath.Join(repo, ".git")
	hooksDir := filepath.Join(gitDir, "hooks")
	hook := filepath.Join(hooksDir, "post-commit")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ntouch hook-ran\n"), 0o755))

	testsupport.Isolate(t) // the session scratch resolves under a fake $HOME, never the real ~/.ctxloom
	pol := NewContainerFor(rt, "mock").WithImage(worktreeIntegrationImage).WithSessionState(SessionState{Harp: "brisk-teal-otter"})
	ws, err := pol.prepareWorkspace(ctx, repo, "hostmain-itest")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok, "the host-base workspace is a container workspace")
	require.Contains(t, cw.extraMounts, bind(hooksDir, hooksDir, true), "the policy shadows the main checkout's hooks read-only")

	// The project mount first, as the runner renders it (roots, then the plan).
	policyMounts := append([]mount{{Host: repo, Container: repo}}, cw.extraMounts...)

	for name, cmd := range map[string]string{
		"new hook":     "echo '#!/bin/sh' > " + filepath.Join(hooksDir, "pre-commit"),
		"replace hook": "echo '#!/bin/sh' > " + hook,
		"remove hook":  "rm " + hook,
	} {
		out, err := dockerRun(ctx, primary, worktreeIntegrationImage, repo, policyMounts, "sh", "-c", cmd)
		assert.Error(t, err, "%s: the write must fail in-container:\n%s", name, out)
	}
	assert.NoFileExists(t, filepath.Join(hooksDir, "pre-commit"), "no hook was installed on the host")
	hookAfter, err := os.ReadFile(hook)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\ntouch hook-ran\n", string(hookAfter), "the host's hook is unchanged")

	// The checkout's own index and HEAD are writable: a commit, a branch
	// switch and back, all lock-and-rename onto them, and the hook still runs.
	script := `set -e
git config --global safe.directory '*'
git config user.name itest && git config user.email itest@example.com
echo change > change.txt && git add change.txt && git commit -qm in-container
git switch -qc side && git switch -q main`
	out, err := dockerRun(ctx, primary, worktreeIntegrationImage, repo, policyMounts, "sh", "-c", script)
	require.NoError(t, err, "committing in a main checkout must work through the policy's mounts:\n%s", out)
	assert.Equal(t, "in-container", gitRun(t, repo, "log", "-1", "--format=%s"), "the commit landed on the host")
	assert.Equal(t, "main", gitRun(t, repo, "symbolic-ref", "--short", "HEAD"), "the HEAD writes landed on the host")
	assert.Empty(t, gitRun(t, repo, "status", "--porcelain", "--untracked-files=no"), "the host's index records the commit")
	assert.FileExists(t, filepath.Join(repo, "hook-ran"), "the read-only hook resolved and ran in-container")
	for _, lock := range []string{"index.lock", "HEAD.lock", filepath.Join("refs", "heads", "main.lock")} {
		assert.NoFileExists(t, filepath.Join(gitDir, lock), "git leaves no %s behind", lock)
	}

	require.NoError(t, ws.Cleanup())
	assert.DirExists(t, repo, "the host base's Cleanup never removes the live project dir")
}
