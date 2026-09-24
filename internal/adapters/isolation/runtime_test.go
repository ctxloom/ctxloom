package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// sampleSpec is a representative RunSpec used across the argv-rendering tests.
func sampleSpec() RunSpec {
	return RunSpec{
		Image:   "ctxloom-agent:latest",
		Name:    "ctxloom-iso-m-abc",
		WorkDir: "/home/u/proj",
		Home:    "/root",
		Command: []string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		Env:     []string{"CTXLOOM_PLUGIN=ai-backend-v1", "PLUGIN_PROTOCOL_VERSIONS=1"},
		Mounts: []Mount{
			{Host: "/home/u/proj", Container: "/home/u/proj"},
			{Host: "/tmp/sock", Container: "/run/ctxloom/plugin"},
		},
	}
}

// TestDockerRootless_RunsAsMappedRoot: rootless docker maps container-root to
// the host user — the ONLY uid that does — so the argv carries neither --user
// nor a PUID remap request (the run stays container-root). The identical-path
// project mount, socket mount, workdir, image, and in-container command must
// all render.
func TestDockerRootless_RunsAsMappedRoot(t *testing.T) {
	args := Docker{rootless: true}.RunArgs(sampleSpec())
	joined := strings.Join(args, " ")

	assert.Equal(t, []string{"run", "--rm", "--name", "ctxloom-iso-m-abc"}, args[:4], "run head")
	assert.NotContains(t, joined, "--user", "rootless docker maps root→host user; no --user")
	assert.NotContains(t, joined, "PUID", "no identity remap: container-root IS the launching user")
	assert.Contains(t, joined, "--mount type=bind,source=/home/u/proj,target=/home/u/proj", "identical-path project mount")
	assert.Contains(t, joined, "--mount type=bind,source=/tmp/sock,target=/run/ctxloom/plugin", "socket-dir mount")
	assert.Contains(t, joined, "-e HOME=/root", "fresh HOME")
	assert.Contains(t, joined, "-w /home/u/proj", "workdir")
	// The image precedes the in-container command as the final tokens.
	assert.Equal(t,
		[]string{"ctxloom-agent:latest", "/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		args[len(args)-5:], "image then in-container argv")
}

// TestRunArgs_AuthSecretValueNotInArgv is the regression for the world-readable
// cmdline leak: the resolved auth env crosses into the `docker run` argv
// NAME-ONLY (`-e ANTHROPIC_API_KEY`), never as `KEY=VAL`, so the secret value
// never appears in /proc/<pid>/cmdline for the container's whole lifetime. docker
// forwards the value from its own inherited environment (the docker CLI ctxloom
// execs inherits os.Environ, where the key was detected) instead.
func TestRunArgs_AuthSecretValueNotInArgv(t *testing.T) {
	const secret = "sk-ant-SUPER-SECRET-VALUE"
	t.Setenv("ANTHROPIC_API_KEY", secret)

	auth, ok := resolveDeclaredAuth(claudeAuth(t))
	require.True(t, ok, "an ANTHROPIC_API_KEY in the env resolves env passthrough")
	require.Equal(t, authEnv, auth.mode)

	spec := sampleSpec()
	spec.Env = append(spec.Env, auth.envPassthrough...)
	args := Docker{rootless: true}.RunArgs(spec)
	joined := strings.Join(args, " ")

	assert.Contains(t, joined, "-e ANTHROPIC_API_KEY", "the auth var crosses by NAME")
	assert.NotContains(t, joined, "ANTHROPIC_API_KEY="+secret, "no KEY=VAL form in the argv")
	for _, a := range args {
		assert.NotContains(t, a, secret, "the secret VALUE must never appear in the run argv")
	}
}

// TestDockerRootful_PassesIdentityEnv: under a rootful daemon the container
// starts as root and PUID/PGID tell the image entrypoint to remap its ctxloom
// user to the launching uid/gid and drop to it — so the plugin's bind-mounted
// socket and every project write land host-user-owned. No --user: the
// entrypoint needs root to usermod.
func TestDockerRootful_PassesIdentityEnv(t *testing.T) {
	joined := strings.Join(Docker{rootless: false}.RunArgs(sampleSpec()), " ")
	assert.Contains(t, joined, fmt.Sprintf("-e PUID=%d", os.Getuid()), "launching uid crosses for the remap")
	assert.Contains(t, joined, fmt.Sprintf("-e PGID=%d", os.Getgid()), "launching gid crosses for the remap")
	assert.NotContains(t, joined, "--user", "the entrypoint, not --user, sets identity")
}

// TestIdentityEnvArgs_NeverAllowsRoot: the image entrypoint refuses to run the
// engine as root when it cannot BECOME the PUID identity, and NOTHING ctxloom
// passes may downgrade that refusal — in either mode.
//
// INVERTED from TestIdentityEnvArgs_DegradedAllowsRootFallback by the
// degradation audit (obstinate-judiciary). That test asserted --degraded
// CARRIES `-e CTXLOOM_ALLOW_ROOT=1`, which made the bypass the specification:
// the flag told the entrypoint to run the engine as root with the user's
// project bind-mounted, so every file the run touched came back root-owned on
// the host. --degraded means "I accept a thinner run", never "I accept running
// as root", and there is no caller for whom root is the right answer — so the
// argv no longer carries the hatch under any mode.
//
// Kept here as well as in the guard test because this one exercises the REAL
// RunArgs of both runtimes, not identityEnvArgs in isolation: the hatch would
// come back just as easily via a runtime's own argv head.
func TestIdentityEnvArgs_NeverAllowsRoot(t *testing.T) {
	resetStrictness(t)
	assert.NotContains(t, strings.Join(Docker{}.RunArgs(sampleSpec()), " "),
		"CTXLOOM_ALLOW_ROOT", "docker run must never carry the root escape hatch")
	assert.NotContains(t, strings.Join(Podman{rootless: true}.RunArgs(sampleSpec()), " "),
		"CTXLOOM_ALLOW_ROOT", "podman run must never carry the root escape hatch")
}

// TestDockerIsRootless_ProbeErrorRoutesAFinding: the daemon's rootless-ness
// decides whether PUID is injected — i.e. who OWNS every file the run writes
// — so an undecidable probe must not pick a direction silently. It assumes
// rootful (the least damaging wrong guess: subuid-skewed ownership under a
// rootless daemon, vs running the engine as REAL root under a rootful one)
// and routes a ClassIsolation finding: strict collects it (the choke owner
// aborts), degraded streams the warning and proceeds on the assumption.
func TestDockerIsRootless_ProbeErrorRoutesAFinding(t *testing.T) {
	resetStrictness(t)
	prev := dockerSecurityOptions
	t.Cleanup(func() { dockerSecurityOptions = prev })

	dockerSecurityOptions = func() (string, error) { return "", errors.New("probe timed out") }
	assert.False(t, dockerIsRootless(), "least-damaging default: rootful")
	found := strictness.All()
	require.Len(t, found, 1)
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
	assert.Contains(t, found[0].Message, "rootless")
	assert.Contains(t, found[0].FixIt, "--degraded")

	// A successful probe never records: both answers are decisions, not faults.
	strictness.Reset()
	dockerSecurityOptions = func() (string, error) { return "[name=rootless name=seccomp]", nil }
	assert.True(t, dockerIsRootless())
	dockerSecurityOptions = func() (string, error) { return "[name=seccomp]", nil }
	assert.False(t, dockerIsRootless())
	assert.Empty(t, strictness.All())

	// Degraded: the assumption is warned about AND collected — degraded
	// suppresses fatality, not recording, so the run can still account for the
	// boundary it assumed rather than verified.
	strictness.Reset()
	dockerSecurityOptions = func() (string, error) { return "", errors.New("probe timed out") }
	assert.False(t, dockerIsRootless())
	assert.NotEmpty(t, strictness.All())
}

// TestNewDockerRuntime_ProbesOnlyReachableDaemons: with no reachable docker
// the rootless probe must not run at all — the runtime is never selected
// (Available gates selection), so probing would only manufacture a spurious
// identity finding on docker-less/daemon-down hosts (e.g. podman machines),
// aborting strict-mode container runs that docker plays no part in.
func TestNewDockerRuntime_ProbesOnlyReachableDaemons(t *testing.T) {
	resetStrictness(t)
	prev := dockerSecurityOptions
	t.Cleanup(func() { dockerSecurityOptions = prev })

	dockerSecurityOptions = func() (string, error) {
		t.Fatal("the identity probe must not run for an unreachable daemon")
		return "", nil
	}
	rt := newDockerRuntime(func(string) bool { return false })
	assert.Equal(t, Docker{}, rt)
	assert.Empty(t, strictness.All())

	dockerSecurityOptions = func() (string, error) { return "[name=rootless]", nil }
	rt = newDockerRuntime(func(string) bool { return true })
	assert.Equal(t, Docker{rootless: true}, rt, "a reachable daemon gets the real probe")
}

// TestPodmanRootful_DockerCompatibleArgv: rootful podman matches rootful
// docker — identity env for the entrypoint remap, no keep-id, no --user.
func TestPodmanRootful_DockerCompatibleArgv(t *testing.T) {
	args := Podman{}.RunArgs(sampleSpec())
	joined := strings.Join(args, " ")
	assert.Equal(t, []string{"run", "--rm", "--name", "ctxloom-iso-m-abc"}, args[:4])
	assert.NotContains(t, joined, "keep-id")
	assert.NotContains(t, joined, "--user")
	assert.Contains(t, joined, fmt.Sprintf("-e PUID=%d", os.Getuid()))
	assert.Contains(t, joined, "--mount type=bind,source=/home/u/proj,target=/home/u/proj")
	assert.Equal(t, []string{"rm", "-f", "-t", "0", "c1"}, Podman{}.RemoveArgs("c1"),
		"podman's rm -f waits its stop timeout before SIGKILL unless told -t 0")
}

// TestPodmanRootless_KeepIDAsRoot: rootless podman needs keep-id so the
// launching uid maps to ITSELF in-container, and must enter as namespaced root
// (keep-id's default user is the host uid, which could not usermod) so the
// entrypoint can remap ctxloom to PUID/PGID and drop to it.
func TestPodmanRootless_KeepIDAsRoot(t *testing.T) {
	joined := strings.Join(Podman{rootless: true}.RunArgs(sampleSpec()), " ")
	assert.Contains(t, joined, "--userns=keep-id", "launching uid maps to itself")
	assert.Contains(t, joined, "--user 0:0", "enter as namespaced root for the remap")
	assert.Contains(t, joined, fmt.Sprintf("-e PUID=%d", os.Getuid()))
}

// TestRemoveArgs force-removes by name for teardown.
func TestRemoveArgs(t *testing.T) {
	assert.Equal(t, []string{"rm", "-f", "c1"}, Docker{}.RemoveArgs("c1"))
}

// TestHost_IsNonContainer: Host is always available and launches nothing (the
// None/worktree path spawns a bare host subprocess instead).
func TestHost_IsNonContainer(t *testing.T) {
	h := Host{}
	assert.Equal(t, "host", h.Name())
	assert.Empty(t, h.Binary())
	assert.True(t, h.Available(), "the host can always run a subprocess")
	assert.Nil(t, h.RunArgs(sampleSpec()))
	assert.Nil(t, h.RemoveArgs("c1"))
	infos, err := h.Enumerate(context.Background(), containerNamePrefix)
	assert.NoError(t, err)
	assert.Nil(t, infos, "Host launches no containers, so there is never anything to list")
}

// TestDockerAndPodmanRunArgs_StampOwnerLabels pins the orphan-reap fix at its
// source: EVERY container this process starts — docker or podman, rootless or
// not — carries both labels ReapOrphanedContainers reads (labelOwnerPID,
// labelCreatedAt), stamped with THIS process's own pid. Without this,
// ReapOrphanedContainers has no signal to work from at all.
func TestDockerAndPodmanRunArgs_StampOwnerLabels(t *testing.T) {
	pidLabel := fmt.Sprintf("--label %s=%d", labelOwnerPID, os.Getpid())
	for _, rt := range []Runtime{Docker{rootless: true}, Docker{rootless: false}, Podman{rootless: true}, Podman{rootless: false}} {
		joined := strings.Join(rt.RunArgs(sampleSpec()), " ")
		assert.Contains(t, joined, pidLabel, "%s: owner-pid label", rt.Name())
		assert.Contains(t, joined, "--label "+labelCreatedAt+"=", "%s: created-at label", rt.Name())
	}
}

// TestOciRuntimeEnumerate_ParsesPsOutput pins ociRuntime.enumerate's parsing
// of `docker/podman ps --format {{.Names}}\t{{json .Labels}}` output: one
// ContainerInfo per non-blank line, labels decoded from the JSON tail, and a
// container with no labels at all (docker renders that as the literal `null`)
// coming back with a nil/empty map rather than an error — exactly the shape
// classifyContainer's map lookups already treat as "label absent".
func TestOciRuntimeEnumerate_ParsesPsOutput(t *testing.T) {
	orig := probeExec
	t.Cleanup(func() { probeExec = orig })

	var gotBin string
	var gotArgs []string
	probeExec = func(_ context.Context, bin string, args []string) (string, error) {
		gotBin = bin
		gotArgs = args
		return "ctxloom-iso-a-1\t{\"ctxloom.owner-pid\":\"123\",\"ctxloom.created-at\":\"2026-01-01T00:00:00Z\"}\n" +
			"ctxloom-iso-a-2\tnull\n", nil
	}

	infos, err := (ociRuntime{}).enumerate(context.Background(), "docker", containerNamePrefix)
	require.NoError(t, err)
	assert.Equal(t, "docker", gotBin)
	assert.Contains(t, gotArgs, "name="+containerNamePrefix)

	require.Len(t, infos, 2)
	assert.Equal(t, "ctxloom-iso-a-1", infos[0].Name)
	assert.Equal(t, "123", infos[0].Labels[labelOwnerPID])
	assert.Equal(t, "2026-01-01T00:00:00Z", infos[0].Labels[labelCreatedAt])
	assert.Equal(t, "ctxloom-iso-a-2", infos[1].Name)
	assert.Empty(t, infos[1].Labels, "a labelless container's `null` JSON must decode to an empty map, not error")
}

// TestOciRuntimeEnumerate_PropagatesRunFailure: a probeExec failure (daemon
// down, timeout) must surface as an error, not as an empty/successful list —
// ReapOrphanedContainers treats a nil result identically to "nothing to
// reap", so silently swallowing a real failure here would look exactly like
// an all-clear sweep.
func TestOciRuntimeEnumerate_PropagatesRunFailure(t *testing.T) {
	orig := probeExec
	t.Cleanup(func() { probeExec = orig })
	probeExec = func(context.Context, string, []string) (string, error) {
		return "", errors.New("daemon unreachable")
	}

	_, err := (ociRuntime{}).enumerate(context.Background(), "docker", containerNamePrefix)
	assert.Error(t, err)
}

// TestInContainer_EnvMarkers: the dev-container env markers trip detection (the
// filesystem markers are host-dependent and covered by the seam tests below).
func TestInContainer_EnvMarkers(t *testing.T) {
	t.Setenv("REMOTE_CONTAINERS", "true")
	assert.True(t, InContainer(), "REMOTE_CONTAINERS marks an in-container process")
}

// TestInContainerFrom_Markers drives the seam-injected detection core across
// every marker class WITHOUT touching the real /proc, sentinel files, or env
// (CI itself runs in containers; the hostile-env suite junks the env). Each
// hit is named for `container check`.
func TestInContainerFrom_Markers(t *testing.T) {
	none := func(string) error { return os.ErrNotExist }
	noFile := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	noEnv := func(string) string { return "" }

	tests := []struct {
		name     string
		stat     func(string) error
		readFile func(string) ([]byte, error)
		getenv   func(string) string
		want     []string
	}{
		{"no markers → outside", none, noFile, noEnv, nil},
		{"docker sentinel", func(p string) error {
			if p == "/.dockerenv" {
				return nil
			}
			return os.ErrNotExist
		}, noFile, noEnv, []string{"/.dockerenv"}},
		{"podman sentinel", func(p string) error {
			if p == "/run/.containerenv" {
				return nil
			}
			return os.ErrNotExist
		}, noFile, noEnv, []string{"/run/.containerenv"}},
		{"DEVCONTAINER env", none, noFile, func(e string) string {
			if e == "DEVCONTAINER" {
				return "true"
			}
			return ""
		}, []string{"$DEVCONTAINER"}},
		{"kubernetes env", none, noFile, func(e string) string {
			if e == "KUBERNETES_SERVICE_HOST" {
				return "10.0.0.1"
			}
			return ""
		}, []string{"$KUBERNETES_SERVICE_HOST"}},
		{"cgroup v1 docker", none, func(string) ([]byte, error) {
			return []byte("12:cpuset:/docker/abc123"), nil
		}, noEnv, []string{"cgroup:docker"}},
		{"cgroup v2 bare → no marker", none, func(string) ([]byte, error) {
			return []byte("0::/"), nil
		}, noEnv, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inContainerFrom(tt.stat, tt.readFile, tt.getenv))
		})
	}
}

// TestRenderRunSpec_FreshHomeIsCarriedByEveryProductionSpec pins that a
// review row claimed renderRunSpec's `if spec.Home != ""` guard
// silently loses the "fresh HOME isolates engine global state" property for a
// spec built without a home. The guard is real, but the empty case is
// unreachable for any spec that launches an ENGINE: home is not a per-call
// parameter, it is Container.home, assigned defaultContainerHome by
// NewContainerFor — the sole constructor every path (containerFor,
// NewContainerWorktreeFor) routes through — and threaded verbatim
// into both engine-launching builders (buildRunnerSpec, ExecSpec). The one production RunSpec that carries no home
// is the shared-fs marker probe, which runs `cat /probe/marker` in a scratch
// container and holds no engine state at all, so it has no HOME property to
// lose. This pins both halves: every Container carries a home, and a spec that
// carries one renders the -e HOME= flag.
func TestRenderRunSpec_FreshHomeIsCarriedByEveryProductionSpec(t *testing.T) {
	require.NotEmpty(t, defaultContainerHome, "the container home constant is the fresh-HOME property's single source")

	rt := fakeRuntime{name: "docker", available: true}
	for _, backend := range append(composableEngines(), "", "no-such-engine") {
		assert.Equal(t, defaultContainerHome, NewContainerFor(rt, backend).home,
			"backend %q: every Container must carry the fresh in-container HOME", backend)
	}
	assert.Equal(t, defaultContainerHome, NewContainerFor(rt, "mock").WithImage("img").home)
	assert.Equal(t, defaultContainerHome, containerFor(rt, "claude-code", ImageConfig{}).home)

	spec := runnerSpecFor(Docker{}, "claude-code", "/proj", nil, nil)
	require.Equal(t, defaultContainerHome, spec.Home)
	assert.Contains(t, strings.Join(renderRunSpec(spec), " "), "-e HOME="+defaultContainerHome,
		"a spec carrying a home must render the fresh-HOME env flag")
}

// TestPassesPUID: only rootless docker keeps the run container-root with no
// identity env; the RunArgs head and the run-as-is identity check read the
// same answer.
func TestPassesPUID(t *testing.T) {
	assert.False(t, Docker{rootless: true}.passesPUID())
	assert.NotContains(t, strings.Join(Docker{rootless: true}.RunArgs(sampleSpec()), " "), "PUID=")
	assert.True(t, Docker{}.passesPUID())
	assert.True(t, Podman{}.passesPUID())
	assert.True(t, Podman{rootless: true}.passesPUID())
}
