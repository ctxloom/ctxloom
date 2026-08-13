//go:build docker_integration

// Package isolation's docker-gated worktree-in-container proof. Build-tagged so the
// normal `just test` never compiles it (the Go gate stays green without docker);
// run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run ContainerWorktree
//
// It composes {Workspace=worktree} × {Runtime=container} against a REAL temp git
// repo: PrepareWorkspace creates a per-member worktree + container scratch; the
// plugin comes up IN a container over the mounted worktree (Info()); a standalone
// container with the SAME two mounts (worktree identical-path + the .git common-dir
// mirror) resolves git — the gitdir-when-mounted (§4a T1.1) proof, contrasted with
// the mirror-absent failure; a config write into the mounted worktree lands in the
// worktree (ephemeral) not the host project; and Kill + the WIP-safe teardown leave
// NO leftover container and NO leftover worktree.
package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const worktreeIntegrationImage = "ctxloom-iso-wt-itest:latest"

// TestContainerWorktreePolicy_WorktreeInContainer is the Phase-C gate: a fan-out
// member's worktree mounted into a container, with git resolving inside.
func TestContainerWorktreePolicy_WorktreeInContainer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		dockergate.SkipCapability(t, "git not on PATH, and the worktree-in-container test needs a real repo")
	}
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the worktree-in-container integration test")
	// This test writes into the mounted worktree FROM INSIDE the container and
	// then tears the worktree down FROM THE HOST. Only ROOTLESS docker maps
	// container-root to the launching user, so those in-container writes are
	// host-user-owned and the WIP-safe teardown (and t.TempDir cleanup) can remove
	// them. Under a ROOTFUL daemon the writes are ROOT-owned and both teardowns
	// fail, LEAKING root-owned files — so gate on rootless, not merely Available().
	rt := SelectRuntime("docker")
	if d, ok := rt.(Docker); !ok || !d.rootless {
		dockergate.SkipCapability(t, "rootful docker root-owns worktree files the host-user teardown cannot remove; needs rootless docker")
	}
	buildGitIntegrationImage(t)

	// Env-passthrough auth satisfies the container gate without host creds; the mock
	// backend ignores it (no real engine runs here — Info precedes Execute).
	t.Setenv("ANTHROPIC_API_KEY", "itest-mock-key")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	repo := initRealRepo(t) // helper from worktree_integration_test.go (same package)

	// The in-container ctxloom (the plugin the SpawnClient step below brings up)
	// runs with cwd = the mounted worktree, and a linked worktree with NO
	// .ctxloom of its own is a fatal config finding — it aborts startup with
	// exit 3 before the go-plugin handshake, which surfaces here as an
	// unreadable plugin stdout rather than as the config error it is. A real
	// project's worktree checkout carries the project's committed .ctxloom, so
	// the fixture commits one too. (Only reachable since container auth stopped
	// failing closed on this test's constructor: the run never got past the auth
	// gate to discover it.)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".ctxloom", "config.yaml"), []byte("version: 5\n"), 0o644))
	gitRun(t, repo, "add", filepath.Join(".ctxloom", "config.yaml"))
	gitRun(t, repo, "commit", "-m", "seed ctxloom config")

	pol := NewContainerWorktreeFor(rt, "mock", ImageConfig{Image: worktreeIntegrationImage}, git.NewExec())
	ws, err := pol.PrepareWorkspace(ctx, repo, "wt-itest")
	require.NoError(t, err, "PrepareWorkspace must create a worktree + container scratch")

	// Workspace kind: a WORKTREE (not the live project) — under the OS temp dir, a
	// real checkout, distinct from the host repo.
	assert.NotEqual(t, repo, ws.Dir(), "the member workspace is a worktree, not the live project")
	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "the worktree lives under the OS temp dir")
	assert.FileExists(t, filepath.Join(ws.Dir(), "README.md"), "the worktree is a checkout of the repo")

	// The composed workspace carries the identical-path .git gitdir mirror mount.
	cw, ok := ws.(*containerWorkspace)
	require.True(t, ok, "the workspace is a worktree-in-container workspace")
	common, err := git.NewExec().CommonDir(ctx, ws.Dir())
	require.NoError(t, err)
	assert.Contains(t, cw.extraMounts, Mount{Host: common, Container: common},
		"the .git common-dir is mirrored identical-path")

	// (1) TRANSPORT: Info() crosses the worktree-in-container gRPC boundary.
	client, err := pol.SpawnClient("mock", "", 0, ws, nil)
	require.NoError(t, err, "SpawnClient brings the plugin up in a container over the mounted worktree")
	// Kill on cleanup so a later require failure can't leak a running container
	// (Kill is idempotent: the explicit Kill below + this one both just `rm -f`).
	t.Cleanup(func() { client.Kill() })
	info, err := client.Info(ctx)
	require.NoError(t, err, "Info() round-trips over the plugin-in-container transport")
	assert.Equal(t, "mock", info.GetName(), "backend name from inside the container")
	t.Logf("Info() over worktree-in-container transport: name=%q version=%q", info.GetName(), info.GetVersion())

	// (2) GIT RESOLVES INSIDE: a standalone container with the SAME two mounts
	// (worktree identical-path + the .git mirror) resolves git — the T1.1 proof.
	revParse, err := dockerRun(ctx, worktreeIntegrationImage, ws.Dir(),
		[]Mount{{Host: ws.Dir(), Container: ws.Dir()}, {Host: common, Container: common}},
		"git", "-c", "safe.directory=*", "rev-parse", "HEAD")
	require.NoError(t, err, "git must resolve inside the container via the mounted common-dir:\n%s", revParse)
	t.Logf("in-container `git rev-parse HEAD`: %s", strings.TrimSpace(revParse))
	assert.Len(t, strings.TrimSpace(revParse), 40, "a full commit sha resolved inside the container")

	// Contrast: mounting ONLY the worktree (no .git mirror) FAILS — the mirror is
	// load-bearing, not incidental.
	noMirror, err := dockerRun(ctx, worktreeIntegrationImage, ws.Dir(),
		[]Mount{{Host: ws.Dir(), Container: ws.Dir()}},
		"git", "-c", "safe.directory=*", "rev-parse", "HEAD")
	require.Error(t, err, "without the .git mirror, git must NOT resolve inside the container")
	assert.Contains(t, noMirror, "not a git repository", "the gitdir pointer is unresolvable without the mirror")
	t.Logf("in-container git WITHOUT the .git mirror (expected failure): %s", strings.TrimSpace(noMirror))

	// (3) CONFIG-IN-WORKTREE: a write into the mounted worktree lands in the
	// worktree (ephemeral) NOT the host project, and is §3.1-excluded so it stays
	// out of the merge and out of the teardown's dirty check.
	writeOut, err := dockerRun(ctx, worktreeIntegrationImage, ws.Dir(),
		[]Mount{{Host: ws.Dir(), Container: ws.Dir()}},
		"sh", "-c", "mkdir -p .claude && printf '{}' > .claude/settings.json && printf '{}' > .mcp.json")
	require.NoError(t, err, "config write into the mounted worktree must succeed:\n%s", writeOut)
	assert.FileExists(t, filepath.Join(ws.Dir(), ".claude", "settings.json"), "config write lands in the worktree")
	assert.NoFileExists(t, filepath.Join(repo, ".claude", "settings.json"), "the host project stays clean")
	assert.NoFileExists(t, filepath.Join(repo, ".mcp.json"), "the host project stays clean")

	// (4) CLEAN TEARDOWN of BOTH the container and the worktree.
	wtDir := ws.Dir()
	client.Kill()
	require.NoError(t, ws.Cleanup(), "Kill-then-Cleanup: WIP-safe teardown after the container is gone")

	assert.Eventually(t, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-a",
			"--filter", "name=ctxloom-iso-wt-itest-", "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out)) == ""
	}, 20*time.Second, 250*time.Millisecond, "the container is force-removed on Kill")

	// The worktree is removed despite the excluded config writes (§3.1 keeps
	// `git status` clean, so the WIP-safe remove proceeds).
	assert.NoDirExists(t, wtDir, "the worktree dir is removed by the WIP-safe teardown")
	list := gitOut(t, repo, "worktree", "list", "--porcelain")
	assert.NotContains(t, list, wtDir, "no leftover worktree after the WIP-safe teardown")
}

// dockerRun runs `docker run --rm` for the given image with the given identical-path
// mounts and workdir, returning the combined output and error. Used to prove the
// git/config behavior of the SAME mount composition the policy builds, independent
// of the plugin. (Rootless docker maps container-root to the host user, so files it
// creates in the mounted worktree are host-user-owned and the teardown can remove
// them.)
func dockerRun(ctx context.Context, image, workDir string, mounts []Mount, args ...string) (string, error) {
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
// on alpine + git) used by the worktree-in-container test: ctxloom serves the mock
// plugin, and git proves the gitdir mount composition. Rebuilt each run so the test
// exercises the current tree.
func buildGitIntegrationImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	// Target the HOST arch: `FROM alpine:latest` resolves the host's arch, so a
	// hardcoded GOARCH=amd64 binary would `exec format error` on an arm64 host.
	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
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
