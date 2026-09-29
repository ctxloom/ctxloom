//go:build unix

package isolation

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeOwnerRootEnv carries the injected root into the helper process; its
// absence is what keeps TestHelperFSProbeOwner inert in an ordinary run.
const probeOwnerRootEnv = "CTXLOOM_TEST_FSPROBE_OWNER_ROOT"

// probeOwnerReady is the line the helper prints once its probe dir exists and
// the probe is parked inside the container run — the moment the parent kills.
const probeOwnerReady = "fsprobe-owner-parked"

// TestHelperFSProbeOwner is not a test: it is the probe owner that
// TestProbeOneRoot_ReapsScratchOfKilledOwner SIGKILLs mid-probe. It parks
// inside probeExec forever, so the only way out is the kill.
func TestHelperFSProbeOwner(t *testing.T) {
	root := os.Getenv(probeOwnerRootEnv)
	if root == "" {
		t.Skip("helper process for TestProbeOneRoot_ReapsScratchOfKilledOwner")
	}
	probeExec = func(context.Context, string, []string) (string, error) {
		fmt.Println(probeOwnerReady)
		select {}
	}
	_ = probeOneRoot(context.Background(), probeRuntime{}, "img", root)
}

// TestProbeOneRoot_ReapsScratchOfKilledOwner: a probe owner killed mid-probe
// cannot run its deferred RemoveAll, so its scratch dir stays in the probed
// root — which is the user's live project among others. The NEXT probe of
// that root must leave no such dir behind, however recently the dead owner
// made it: a run's end releases every ephemeral resource it owns, and a
// crashed run's end is no exception.
//
// The death is real (SIGKILL on a re-exec'd owner, forced at the moment the
// owner reports it is parked mid-probe), so the dir is in exactly the state a
// crash leaves it. The root is a test temp dir, never a real project.
func TestProbeOneRoot_ReapsScratchOfKilledOwner(t *testing.T) {
	root := t.TempDir()

	owner := exec.Command(os.Args[0], "-test.run=^TestHelperFSProbeOwner$")
	owner.Env = append(os.Environ(), probeOwnerRootEnv+"="+root)
	stdout, err := owner.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, owner.Start())
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })

	lines := bufio.NewScanner(stdout)
	parked := false
	for lines.Scan() {
		if lines.Text() == probeOwnerReady {
			parked = true
			break
		}
	}
	require.True(t, parked, "helper never reached the probe: %v", lines.Err())
	require.NoError(t, owner.Process.Signal(syscall.SIGKILL))
	_ = owner.Wait()

	require.Len(t, scratchDirs(t, root, probeScratchPrefix), 1,
		"precondition: the killed owner left its probe dir behind")

	stubProbeExec(t, func(markerPath string) (string, error) {
		raw, err := os.ReadFile(markerPath)
		return string(raw), err
	})
	require.NoError(t, probeOneRoot(context.Background(), probeRuntime{}, "img", root))

	assert.Empty(t, scratchDirs(t, root, probeScratchPrefix),
		"the next probe reaps a dead owner's scratch and removes its own")
}

// TestProbeOneRoot_SparesLiveConcurrentProbe: probes run concurrently against
// the same roots (fan-out members, sibling ctxloom processes). A probe that
// reaps dead owners' scratch must never take a LIVE probe's dir: that probe
// would read no marker back and report a phantom sharing mismatch, which
// latches as a definitive verdict. The race is forced, not waited on: probe A
// is parked inside its container run while probe B runs start to finish.
func TestProbeOneRoot_SparesLiveConcurrentProbe(t *testing.T) {
	root := t.TempDir()

	aParked := make(chan string)
	aRelease := make(chan struct{})
	first := true
	stubProbeExec(t, func(markerPath string) (string, error) {
		// Only the stub goroutines touch first, and B's call is ordered after
		// A's by the aParked handoff.
		if first {
			first = false
			aParked <- markerPath
			<-aRelease
		}
		raw, err := os.ReadFile(markerPath)
		return string(raw), err
	})

	aDone := make(chan error, 1)
	go func() { aDone <- probeOneRoot(context.Background(), probeRuntime{}, "img", root) }()
	aMarker := <-aParked

	require.NoError(t, probeOneRoot(context.Background(), probeRuntime{}, "img", root),
		"probe B runs to completion while A is live")
	assert.FileExists(t, aMarker, "B must not reap live probe A's scratch")

	close(aRelease)
	require.NoError(t, <-aDone, "A reads its own marker back: no phantom mismatch")
	assert.Empty(t, scratchDirs(t, root, probeScratchPrefix))
}

// TestProbeOneRoot_ReapLeavesUserContent: only directories carrying the
// probe's own prefix are ever candidates; user content that merely sorts
// nearby is never touched.
func TestProbeOneRoot_ReapLeavesUserContent(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "ctxloom-fsprobe.md")
	require.NoError(t, os.WriteFile(file, []byte("notes"), 0o644))
	dir := filepath.Join(root, "ctxloom-fsprobe")
	require.NoError(t, os.Mkdir(dir, 0o755))

	stubProbeExec(t, func(markerPath string) (string, error) {
		raw, err := os.ReadFile(markerPath)
		return string(raw), err
	})
	require.NoError(t, probeOneRoot(context.Background(), probeRuntime{}, "img", root))
	assert.FileExists(t, file)
	assert.DirExists(t, dir, "the bare prefix stem is not a probe scratch name")
}

// TestImageBuild_ReapsScratchOfDeadBuilds: an image build killed mid-flight
// leaves its build-context scratch (ctxloom-imgbase-* / ctxloom-imgbuild-*)
// in the OS temp dir. The next build must reap it. Tested at the build
// functions against a fake runtime script under an injected TMPDIR — never
// the real OS temp dir, and no container image is built. A dead owner's
// on-disk state is simulated as a scratch dir no live process holds; the
// real-kill form of that state is TestProbeOneRoot_ReapsScratchOfKilledOwner,
// which exercises the same owned-scratch mechanism.
func TestImageBuild_ReapsScratchOfDeadBuilds(t *testing.T) {
	selfExe := withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir())
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	for _, prefix := range []string{imageBaseScratchPrefix, imageBuildScratchPrefix} {
		require.NoError(t, os.Mkdir(filepath.Join(tmp, prefix+"dead"), 0o700))
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, filepath.Join(dir, "builds.log"), dir, "{}")
	rt := fakeRuntime{name: "docker", binary: script, available: true}

	_, err := buildBaseImage(context.Background(), rt, &baseStage{desc: "test base", containerfile: []byte("FROM scratch\n")}, false, nil)
	require.NoError(t, err)
	require.NoError(t, buildImage(context.Background(), rt, "ctxloom-reap-test:latest", []byte("FROM scratch\n"), selfExe, buildFlags{}, nil))

	assert.Empty(t, scratchDirs(t, tmp, imageBaseScratchPrefix), "base build reaps dead base scratch and removes its own")
	assert.Empty(t, scratchDirs(t, tmp, imageBuildScratchPrefix), "agent build reaps dead build scratch and removes its own")
}

// TestImageBuild_SparesLiveConcurrentBuild: two builds overlap (sibling
// processes, fan-out ensure). The second must not reap the first's live build
// context out from under its runtime. Forced with FIFOs: build A's fake
// runtime reports its context dir and blocks until released; build B runs to
// completion meanwhile; A's fake then fails unless its Containerfile is still
// there.
func TestImageBuild_SparesLiveConcurrentBuild(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	require.NoError(t, syscall.Mkfifo(started, 0o600))
	require.NoError(t, syscall.Mkfifo(release, 0o600))
	blocking := filepath.Join(dir, "fake-docker-blocking")
	require.NoError(t, os.WriteFile(blocking, []byte(fmt.Sprintf(`#!/bin/sh
PATH=/usr/bin:/bin
for a in "$@"; do ctx="$a"; done
echo "$ctx" > %q
read _ < %q
[ -f "$ctx/Containerfile" ]
`, started, release)), 0o755))

	base := &baseStage{desc: "test base", containerfile: []byte("FROM scratch\n")}
	aDone := make(chan error, 1)
	go func() {
		_, err := buildBaseImage(context.Background(), fakeRuntime{name: "docker", binary: blocking, available: true}, base, false, nil)
		aDone <- err
	}()

	raw, err := os.ReadFile(started)
	require.NoError(t, err)
	aCtx := strings.TrimSpace(string(raw))

	_, err = buildBaseImage(context.Background(), fakeRuntime{name: "docker", binary: "true", available: true}, base, false, nil)
	require.NoError(t, err, "build B runs to completion while A is live")
	assert.DirExists(t, aCtx, "B must not reap live build A's context")

	require.NoError(t, os.WriteFile(release, []byte("go\n"), 0o600))
	require.NoError(t, <-aDone, "A's runtime still finds its build context")
	assert.Empty(t, scratchDirs(t, tmp, imageBaseScratchPrefix))
}
