package isolation

import (
	"errors"
	"fmt"
	"os"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/selfexec"
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

	auth, ok := resolveClaudeContainerAuth("/root", t.TempDir())
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

// TestIdentityEnvArgs_DegradedAllowsRootFallback: the entrypoint refuses to
// run the engine as root when it cannot BECOME the PUID identity; degraded
// mode — the one warn-and-continue home — must let that container launch
// anyway, so the runtime passes the entrypoint's escape hatch there and ONLY
// there (in strict mode the refusal stands and fails the launch loudly).
func TestIdentityEnvArgs_DegradedAllowsRootFallback(t *testing.T) {
	resetStrictness(t)
	joined := strings.Join(Docker{}.RunArgs(sampleSpec()), " ")
	assert.NotContains(t, joined, "CTXLOOM_ALLOW_ROOT", "strict: the root-refusal must stand")

	strictness.SetDegraded(true)
	assert.Contains(t, strings.Join(Docker{}.RunArgs(sampleSpec()), " "),
		"-e CTXLOOM_ALLOW_ROOT=1", "degraded docker run carries the escape hatch")
	assert.Contains(t, strings.Join(Podman{rootless: true}.RunArgs(sampleSpec()), " "),
		"-e CTXLOOM_ALLOW_ROOT=1", "degraded podman run carries the escape hatch")
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
	strictness.SetDegraded(true)
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
	assert.Equal(t, []string{"rm", "-f", "c1"}, Podman{}.RemoveArgs("c1"))
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
}

// TestContainerHandshakeEnv_Curates keeps ONLY the go-plugin handshake vars
// (magic cookie + PLUGIN_*), overrides the socket dir to the container path, and
// drops everything else (host paths/secrets never cross the boundary).
func TestContainerHandshakeEnv_Curates(t *testing.T) {
	in := []string{
		pb.HandshakeConfig.MagicCookieKey + "=ai-backend-v1",
		"PLUGIN_PROTOCOL_VERSIONS=1",
		"PLUGIN_MIN_PORT=0",
		"PLUGIN_UNIX_SOCKET_DIR=/tmp/host-sock", // must be overridden
		"HOME=/home/babbitt",                    // host env — must be dropped
		"ANTHROPIC_API_KEY=secret",              // secret — must be dropped
		"malformed-no-equals",                   // skipped
	}
	out := containerHandshakeEnv(in, "/run/ctxloom/plugin")

	assert.Contains(t, out, pb.HandshakeConfig.MagicCookieKey+"=ai-backend-v1")
	assert.Contains(t, out, "PLUGIN_PROTOCOL_VERSIONS=1")
	assert.Contains(t, out, "PLUGIN_MIN_PORT=0")
	assert.Contains(t, out, "PLUGIN_UNIX_SOCKET_DIR=/run/ctxloom/plugin", "socket dir overridden to the container path")
	assert.NotContains(t, out, "PLUGIN_UNIX_SOCKET_DIR=/tmp/host-sock", "host socket path must not leak")
	for _, kv := range out {
		assert.NotContains(t, kv, "LISTEN_TCP", "the forked TCP-listener gate is gone — never force TCP")
		assert.False(t, strings.HasPrefix(kv, "HOME="), "host HOME must not cross")
		assert.NotContains(t, kv, "ANTHROPIC_API_KEY", "secrets must not cross")
	}
}

// TestContainerSpawnUnsupportedErr_GOOSGate: the go-plugin container SpawnClient
// path is Linux-only since 0.7 (the forked TCP transport that bridged the Docker
// Desktop VM boundary on macOS/Windows was deleted). The gate proceeds on Linux
// (nil) and fails loud on every other platform with a message naming the residual
// paths and pointing at Linux / the top-level run modes.
func TestContainerSpawnUnsupportedErr_GOOSGate(t *testing.T) {
	assert.NoError(t, containerSpawnUnsupportedErr("linux"),
		"Linux uses the stock unix-socket-over-bind-mount transport — the gate must proceed")

	for _, goos := range []string{"darwin", "windows"} {
		err := containerSpawnUnsupportedErr(goos)
		require.Error(t, err, "%s must fail loud: the container TCP transport is gone", goos)
		msg := err.Error()
		assert.Contains(t, msg, "Linux-only", "the message must state the path is Linux-only")
		assert.Contains(t, msg, "top-level run modes", "the message must point at the supported top-level modes")
		assert.Contains(t, msg, goos, "the message must name the offending platform")
	}
}

// TestAddrTranslator_PrefixSwap maps the plugin's announced container socket path
// to the host bind mount and back, and leaves paths outside the mount untouched.
func TestAddrTranslator_PrefixSwap(t *testing.T) {
	tr := containerAddrTranslator{hostSocketDir: "/tmp/host-sock", containerSocketDir: "/run/ctxloom/plugin"}

	net, host, err := tr.PluginToHost("unix", "/run/ctxloom/plugin/plugin123")
	assert.NoError(t, err)
	assert.Equal(t, "unix", net)
	assert.Equal(t, "/tmp/host-sock/plugin123", host, "container→host socket path")

	_, plug, err := tr.HostToPlugin("unix", "/tmp/host-sock/broker456")
	assert.NoError(t, err)
	assert.Equal(t, "/run/ctxloom/plugin/broker456", plug, "host→container socket path")

	_, outside, err := tr.PluginToHost("unix", "/var/run/other.sock")
	assert.NoError(t, err)
	assert.Equal(t, "/var/run/other.sock", outside, "paths outside the mount are unchanged")
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
// into all three engine-launching builders (buildRunSpec's home argument,
// buildRunnerSpec, ExecSpec). The one production RunSpec that carries no home
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

	spec := buildRunSpec("img", "name", "/proj", defaultContainerHome,
		[]string{"/usr/local/bin/ctxloom", "llm", "serve", "claude-code"},
		"/run/ctxloom/plugin", "/tmp/host-sock/plugin123", nil, nil, nil, nil)
	require.Equal(t, defaultContainerHome, spec.Home)
	assert.Contains(t, strings.Join(renderRunSpec(spec), " "), "-e HOME="+defaultContainerHome,
		"a spec carrying a home must render the fresh-HOME env flag")
}

// TestContainerHandshakeEnv_PluginPrefixIsTheCallersGuarantee states the
// boundary explicitly, because the doc comment's "never the host's full
// environment" reads as a promise this function alone keeps and it is not one.
// The PLUGIN_ arm is a prefix match over whatever it is handed, so an ambient
// host PLUGIN_* var WOULD cross if the caller seeded cmdEnv from os.Environ().
// The prefix stays (go-plugin owns that namespace; a version that adds a
// handshake var must not silently lose it). What changed is the caller:
// pb.ContainerClientConfig sets SkipHostEnv, pinned by
// TestContainerClientConfig_SkipsHostEnv. This test pins the half that lives
// here — every non-PLUGIN_ host key is dropped regardless — and documents the
// half that does not, so nobody reads the prefix match as a host-env filter.
func TestContainerHandshakeEnv_PluginPrefixIsTheCallersGuarantee(t *testing.T) {
	out := containerHandshakeEnv([]string{
		pb.HandshakeConfig.MagicCookieKey + "=ai-backend-v1",
		"PLUGIN_PROTOCOL_VERSIONS=1",
		"PLUGIN_LEAKED_SECRET=from-the-host",
		"AWS_SECRET_ACCESS_KEY=from-the-host",
	}, "/run/ctxloom/plugin")

	keys := map[string]bool{}
	for _, kv := range out {
		key, _, _ := strings.Cut(kv, "=")
		keys[key] = true
	}
	assert.True(t, keys[pb.HandshakeConfig.MagicCookieKey], "the magic cookie crosses")
	assert.True(t, keys["PLUGIN_PROTOCOL_VERSIONS"], "go-plugin's handshake vars cross")
	assert.False(t, keys["AWS_SECRET_ACCESS_KEY"], "a non-PLUGIN_ host key never crosses, whatever the caller hands in")
	assert.True(t, keys["PLUGIN_LEAKED_SECRET"],
		"the prefix match forwards ANY PLUGIN_ key: keeping the host environment out of cmdEnv is the caller's job (pb.ContainerClientConfig's SkipHostEnv), not this function's")
}

// TestHostSpawn_FailedDialReturnsPlainNilInterface pins the typed-nil-in-
// interface fix at the exact boundary cli/run.go's teardownTransport
// depends on: Host.Spawn declares its return as the pb.Client INTERFACE, and
// on a failed dial must hand back a genuinely nil interface value, not a
// non-nil interface boxing a nil *pb.LLMRunner.
//
// Driven through the REAL production call (selfexec.SetPathForTesting points
// the self-invoke at /bin/false, so pb.NewSelfInvokingClientForLabelEnv
// actually forks it) rather than a fake: /bin/false exits immediately with
// no handshake output, so go-plugin's dial fails fast — a real but
// sub-millisecond subprocess, not a long-lived one. This is the same failure
// SHAPE as the original incident (a spawn that cannot start), reproduced
// without needing real credentials or a broken engine binary.
//
// The assertion below is a PLAIN `!= nil`, matching what
// cli/run.go's teardownTransport actually does in production — testify's
// require.NotNil/assert.Nil use reflection and see straight through a boxed
// typed-nil pointer, so they would pass whether or not this boundary was
// fixed and cannot stand in for this check.
func TestHostSpawn_FailedDialReturnsPlainNilInterface(t *testing.T) {
	restore := selfexec.SetPathForTesting("/bin/false")
	t.Cleanup(restore)

	client, err := Host{}.Spawn(LaunchSpec{BackendName: "does-not-matter", Verbosity: 0})
	require.Error(t, err, "a self-invoke of a binary that never speaks the plugin handshake must fail")

	if client != nil {
		t.Fatal("Host.Spawn must return a plain nil pb.Client interface on a failed dial; got a non-nil interface (typed-nil-in-interface pitfall) — the exact shape that let cli/run.go's teardownTransport call Kill on a nil receiver")
	}
}

// TestOciRuntimeSpawn_FailedDialReturnsPlainNilInterface is
// TestHostSpawn_FailedDialReturnsPlainNilInterface's container-transport
// twin: ociRuntime.spawn is the OTHER boundary in the spawn chain where a
// concrete *pb.LLMRunner return value gets converted into the pb.Client
// interface (via pb.NewContainerClient), so it needs the same explicit-nil
// treatment on the error path.
//
// fakeRuntime{binary: "/bin/false"} substitutes for a real docker/podman
// binary: newContainerRunner execs rt.Binary() with rt.RunArgs(spec), so
// pointing that at /bin/false makes the "container" exit immediately with no
// handshake output — a real, sub-millisecond subprocess exercising the
// actual production dial path without a docker daemon.
func TestOciRuntimeSpawn_FailedDialReturnsPlainNilInterface(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("the go-plugin container spawn transport is Linux-only")
	}

	rt := fakeRuntime{name: "fake", binary: "/bin/false"}
	launch := LaunchSpec{
		BackendName:   "does-not-matter",
		HostSocketDir: t.TempDir(),
	}

	client, err := ociRuntime{}.spawn(rt, launch)
	require.Error(t, err, "a container runtime binary that exits before the handshake must fail the dial")

	// Same plain comparison as production's teardownTransport (see
	// TestHostSpawn_FailedDialReturnsPlainNilInterface's doc) — testify's
	// reflection-based nil checks would pass either way and cannot stand in
	// for this.
	if client != nil {
		t.Fatal("ociRuntime.spawn must return a plain nil pb.Client interface on a failed dial; got a non-nil interface (typed-nil-in-interface pitfall)")
	}
}
