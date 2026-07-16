package isolation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin/runner"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// containerRemoveTimeout bounds our OWN teardown: the go-plugin fork calls Kill
// with context.Background() (no deadline), so a `docker/podman rm -f` against a
// wedged daemon would hang session shutdown forever. We cap it here regardless of
// the ctx passed in.
const containerRemoveTimeout = 15 * time.Second

// containerRunner implements go-plugin's runner.Runner by launching the plugin
// server INSIDE a container via a Runtime. It mirrors go-plugin's own
// cmdrunner.CmdRunner shape — a thin lifecycle wrapper over an *exec.Cmd — except
// the process it manages is `docker/podman run …` and Kill force-removes the
// container. Stdout() is the CONTAINER's stdout, from which go-plugin reads the
// plugin handshake line; the embedded containerAddrTranslator maps the plugin's
// announced unix-socket path from the container namespace to the host bind mount.
type containerRunner struct {
	runtime Runtime
	name    string
	cmd     *exec.Cmd

	stdout io.ReadCloser
	stderr io.ReadCloser

	containerAddrTranslator
}

// Ensure containerRunner satisfies the go-plugin runner interface.
var _ runner.Runner = (*containerRunner)(nil)

// newContainerRunner builds the runner for one plugin container. hostSocketDir is
// the host directory go-plugin created for the unix socket (passed to the
// RunnerFunc); containerSocketDir is the fixed in-container path it is bind-mounted
// to — the two seed the AddrTranslator's container↔host path swap.
func newContainerRunner(rt Runtime, spec RunSpec, hostSocketDir, containerSocketDir string, spawnEnv map[string]string) (*containerRunner, error) {
	cmd := exec.Command(rt.Binary(), rt.RunArgs(spec)...)
	// Per-spawn env values ride the `run` PROCESS env (owner-readable only);
	// the spec carries matching bare-name `-e NAME` forms so the runtime
	// forwards them into the container without touching the argv.
	if len(spawnEnv) > 0 {
		kv := make([]string, 0, len(spawnEnv))
		for k, v := range spawnEnv {
			kv = append(kv, k+"="+v)
		}
		sort.Strings(kv)
		cmd.Env = append(os.Environ(), kv...)
	}
	// No Stdin: go-plugin (this version) does not watch stdin for parent-death, so
	// the plugin runs until the container is removed by Kill. Leaving Stdin nil
	// keeps `docker run` from holding the host's terminal.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("container stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("container stderr pipe: %w", err)
	}
	return &containerRunner{
		runtime: rt,
		name:    spec.Name,
		cmd:     cmd,
		stdout:  stdout,
		stderr:  stderr,
		containerAddrTranslator: containerAddrTranslator{
			hostSocketDir:      hostSocketDir,
			containerSocketDir: containerSocketDir,
		},
	}, nil
}

// Start launches the container process (the plugin comes up inside it).
func (r *containerRunner) Start(_ context.Context) error {
	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("start %s container: %w", r.runtime.Name(), err)
	}
	return nil
}

// Wait blocks until the container process exits.
func (r *containerRunner) Wait(_ context.Context) error { return r.cmd.Wait() }

// Kill tears the container down: a name-targeted force-remove (stops + removes the
// container, which also ends the --rm `run` process), under OUR own timeout so a
// wedged daemon cannot hang shutdown. The remove targets the CONTAINER by name —
// the real stop; the trailing cmd.Process kill only reaps our own `run` CLI (it
// would NOT stop the container). If the remove does not confirm the container is
// gone (timeout on a wedged daemon, or a real rm error) we surface the leak
// LOUDLY: the live container still holds the workspace Cleanup is about to remove.
// Teardown runs outside the startup choke, so this is a streamed warn-and-continue
// (not a strictness finding) — never panic a run, but never hide a leak either. A
// racing --rm makes the remove report an already-gone container, which is success.
func (r *containerRunner) Kill(ctx context.Context) error {
	if r.name != "" && r.runtime.Binary() != "" {
		cctx, cancel := context.WithTimeout(ctx, containerRemoveTimeout)
		// probeExec is the package's exec seam (`.Output()` captures stderr onto
		// the ExitError); reuse it so teardown is testable without a real runtime.
		if _, err := probeExec(cctx, r.runtime.Binary(), r.runtime.RemoveArgs(r.name)); err != nil && !removeReportsGone(err) {
			clidiag.Warn("ctxloom",
				"container %q may still be running after teardown (%v) — the %s daemon did not confirm removal; it holds this run's workspace, remove it manually with `%s %s`",
				r.name, err, r.runtime.Name(), r.runtime.Binary(), strings.Join(r.runtime.RemoveArgs(r.name), " "))
		}
		cancel()
	}
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	return nil
}

// removeReportsGone reports whether a force-remove error is the benign
// already-gone race (the --rm `run` beat us to removing the container):
// docker/podman exit non-zero with "No such container" — the container had
// ALREADY finished self-removing before our `rm -f` ran — or "removal of
// container ... is already in progress" — docker's OWN async --rm cleanup
// is in flight AT THE SAME MOMENT ours lands, a race live-verified (ISO1's
// docker-gated ACP container-transport test, TestACPContainerTransport_
// RealTurn: a long-lived container that only exits when its stdin closes
// reliably reproduces this exact message on this docker version, 100% of
// runs — the SamePathMount test's short-lived `cat` never does, because
// by the time Close() runs there its --rm has always already finished).
// Both are teardown SUCCESS — the container is gone (or is guaranteed to be,
// momentarily, by docker's own in-flight removal) — not a leak, so neither
// is surfaced.
func removeReportsGone(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	stderr := strings.ToLower(string(ee.Stderr))
	return strings.Contains(stderr, "no such container") ||
		(strings.Contains(stderr, "removal of container") && strings.Contains(stderr, "already in progress"))
}

// Stdout is the container's stdout — go-plugin negotiates the handshake here.
func (r *containerRunner) Stdout() io.ReadCloser { return r.stdout }

// Stderr forwards container/plugin logs to the host logger.
func (r *containerRunner) Stderr() io.ReadCloser { return r.stderr }

// Name is a human-friendly identifier (the container name).
func (r *containerRunner) Name() string { return r.name }

// ID is the unique running-plugin identifier (the container name; teardown targets
// it via --name).
func (r *containerRunner) ID() string { return r.name }

// Diagnose returns best-effort help when the plugin failed to negotiate — almost
// always a missing image, an unrunnable in-container binary, or a socket-mount
// permission problem.
func (r *containerRunner) Diagnose(_ context.Context) string {
	return fmt.Sprintf("plugin container %q (%s) did not negotiate the go-plugin handshake: "+
		"check the image exists, its ctxloom is executable, and the socket dir is bind-mounted",
		r.name, r.runtime.Name())
}

// containerAddrTranslator maps unix-socket paths between the container namespace
// (where the plugin creates and announces its socket, under containerSocketDir)
// and the host namespace (the bind-mounted hostSocketDir the host process dials).
// This is the go-plugin AddrTranslator that solves the socket-across-container
// problem WITHOUT identical-path socket mounts: the two dirs are the same bind
// mount, so translation is a prefix swap and the socket file is visible on both.
type containerAddrTranslator struct {
	hostSocketDir      string
	containerSocketDir string
}

// PluginToHost maps an address the plugin announced (container namespace) to the
// host namespace before the host connects to it. Unix sockets get the socket-dir
// prefix swap; a TCP announce (the loopback transport) gets its host rewritten to
// 127.0.0.1: the plugin binds and advertises 0.0.0.0:PORT inside the container,
// but the host reaches it on the loopback port published by -p 127.0.0.1:PORT:PORT
// — so only the port carries over, dialed on host loopback.
func (t containerAddrTranslator) PluginToHost(network, addr string) (string, string, error) {
	if network == "tcp" {
		if _, port, err := net.SplitHostPort(addr); err == nil {
			return network, net.JoinHostPort("127.0.0.1", port), nil
		}
		return network, addr, nil
	}
	return network, swapPrefix(addr, t.containerSocketDir, t.hostSocketDir), nil
}

// HostToPlugin maps a host address to the container namespace before it is sent to
// the plugin (e.g. go-plugin broker sockets the plugin dials back).
func (t containerAddrTranslator) HostToPlugin(network, addr string) (string, string, error) {
	return network, swapPrefix(addr, t.hostSocketDir, t.containerSocketDir), nil
}

// swapPrefix replaces a leading directory prefix, preserving the socket basename.
// A path outside the mounted dir is returned unchanged (defensive: only the
// plugin's own socket paths live under the mount).
//
// Windows correctness gap (lanky-pod, DOCUMENTED not fixed here — see the
// container-runtime-bugs plan §4/§5; UNVERIFIABLE without Windows hardware):
// this function uses path/filepath (OS-native separators) on BOTH sides, which
// is correct today because both directions swap between two paths that are
// EITHER both host-native (this process's GOOS) — never the case here — or
// (the real case) hostSocketDir (OS-native, a go-plugin-created host temp
// dir) against containerSocketDir (a FIXED POSIX string,
// defaultContainerSocketDir). On Linux/macOS filepath.Join's separator matches
// POSIX, so containerSocketDir renders correctly by coincidence. On a WINDOWS
// BUILD, filepath.Join(containerSocketDir, rel) would render `\`-separated
// segments onto a POSIX prefix — WRONG (the container never sees a `\`). A
// correct fix needs to know WHICH side of the swap is host-native vs.
// container-POSIX (not just "from" vs "to") and join accordingly — pure
// path/filepath is insufficient. Deferred to sudsy-sip Tier C alongside the
// project-mount translation (lanky-pod's pathMapper seam, runtime.go) this
// same swap must eventually route through.
func swapPrefix(path, from, to string) string {
	if from == "" || to == "" {
		return path
	}
	rel, err := filepath.Rel(from, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.Join(to, rel)
}

// containerRunnerFunc builds the go-plugin RunnerFunc that launches the plugin in
// a container. go-plugin calls it with the host socket dir it created (hostTmpDir);
// we bind-mount that to containerSocketDir, override PLUGIN_UNIX_SOCKET_DIR to the
// container path (go-plugin set it to the host path on the `run` process env,
// which the plugin inside the container never sees), and hand back a runner whose
// AddrTranslator maps the socket path back. extraEnv (scoped auth passthrough) and
// extraMounts (auth credential mounts + config overlays) are threaded into the run
// spec on top of the handshake env + project/socket mounts.
//
// go-plugin RunnerFunc+AddrTranslator: canonical plugin-in-container transport; do
// not hand-roll docker-exec + socket plumbing.
// loopbackPort > 0 selects the TCP-over-host-loopback transport (non-Linux
// hosts): the in-container plugin is told to listen on TCP at that pinned port
// (PLUGIN_LISTEN_TCP + PLUGIN_MIN_PORT/PLUGIN_MAX_PORT) and the run publishes it
// to host loopback. 0 keeps the default unix-socket transport (Linux).
func containerRunnerFunc(rt Runtime, image, name, projectDir, home string, command []string, containerSocketDir string, extraEnv []string, extraMounts []Mount, spawnEnv map[string]string, loopbackPort int) pb.ContainerRunnerFunc {
	return func(_ hclog.Logger, cmd *exec.Cmd, hostSocketDir string) (runner.Runner, error) {
		spec := buildRunSpec(image, name, projectDir, home, command, containerSocketDir, hostSocketDir, cmd.Env, extraEnv, extraMounts, loopbackPort, rt.mapper())
		// The per-spawn runner env crosses as bare names (renderRunSpec's
		// `-e <name>` form); the values ride the run-process env
		// (newContainerRunner), never this argv.
		names := make([]string, 0, len(spawnEnv))
		for k := range spawnEnv {
			names = append(names, k)
		}
		sort.Strings(names)
		spec.Env = append(spec.Env, names...)
		return newContainerRunner(rt, spec, hostSocketDir, containerSocketDir, spawnEnv)
	}
}

// containerBaseEnv is the fixed env every plugin container gets, independent of
// auth. IS_SANDBOX=1 tells the engine it runs inside a real sandbox so it permits
// approval-bypass as root — the container runs as (mapped) root, and claude
// otherwise REFUSES `--dangerously-skip-permissions` under uid 0 ("cannot be used
// with root/sudo privileges"). This is the runtime-side complement of the policy's
// ApprovalsBypass: the same "the container is the boundary" decision. Harmless to
// engines that ignore it.
var containerBaseEnv = []string{"IS_SANDBOX=1"}

// buildRunSpec assembles the RunSpec for one plugin container from the launch
// parameters. Env = the fixed container base env + the curated go-plugin handshake
// vars (from hostEnv, socket dir overridden to the container path) + the scoped
// auth extraEnv. Mounts = the project mount (cwd + .git resolve unchanged on the
// identity mapper) + the socket-dir mount + extraMounts (auth credential mounts +
// config overlays). Pure and deterministic so the mount/env wiring is unit-testable
// without a container.
//
// mapper (lanky-pod's seam, container-runtime-bugs.plan.md §5) translates
// projectDir into the WorkDir + project-mount CONTAINER path: identityMapper
// (every caller today — see containerRunnerFunc, which reads it off rt.mapper())
// makes this byte-for-byte the identical-path mount buildRunSpec always built,
// so the seam changes NOTHING for the supported (Linux host-native / true DinD)
// topology. A future Windows mapper (drive-letter host path → POSIX in-
// container target) or DooD mapper (mountinfo-derived) plugs in HERE without
// touching this function's callers.
func buildRunSpec(image, name, projectDir, home string, command []string, containerSocketDir, hostSocketDir string, hostEnv, extraEnv []string, extraMounts []Mount, loopbackPort int, mapper pathMapper) RunSpec {
	mapper = runtimeMapper(mapper)
	containerWorkDir := mapper.toContainer(projectDir)
	env := append(append([]string(nil), containerBaseEnv...), containerHandshakeEnv(hostEnv, containerSocketDir, loopbackPort)...)
	env = append(env, extraEnv...)
	mounts := append([]Mount{
		// Project mount: cwd + .git gitdir resolve unchanged UNDER THE IDENTITY
		// MAPPER; a non-identity mapper renders the container target through
		// the SAME translation WorkDir below uses, so the two never disagree.
		{Host: projectDir, Container: containerWorkDir},
		// The unix-socket dir go-plugin created, mounted to the container path.
		// Inert under the loopback (TCP) transport — the plugin listens on TCP
		// and creates no socket here — but harmless, so the mount is unconditional.
		// containerSocketDir is already a FIXED in-container convention (not a
		// host-derived path), so it does not route through the mapper.
		{Host: hostSocketDir, Container: containerSocketDir},
	}, extraMounts...)
	return RunSpec{
		Image:       image,
		Name:        name,
		WorkDir:     containerWorkDir,
		Home:        home,
		Command:     command,
		Env:         env,
		Mounts:      mounts,
		PublishPort: loopbackPort,
	}
}

// containerHandshakeEnv curates the environment that crosses into the container:
// ONLY the go-plugin handshake vars (the magic cookie + PLUGIN_* — protocol
// versions, ports, mTLS cert, mux flag), never the host's full environment. It
// OVERRIDES PLUGIN_UNIX_SOCKET_DIR to the container path so the plugin creates its
// socket in the bind-mounted dir. Anything else (host paths, secrets) is dropped;
// a real engine's auth is a separate, deliberate follow-up (see the container
// policy docs).
//
// loopbackPort > 0 selects the TCP-over-loopback transport (the ctxloom fork's
// PLUGIN_LISTEN_TCP gate). It then PINS PLUGIN_MIN_PORT/PLUGIN_MAX_PORT to that
// single port (overriding go-plugin's client default of 10000-25000) so the
// in-container listener binds exactly the port the run publishes to host
// loopback, and appends PLUGIN_LISTEN_TCP=1 so the forked serverListener chooses
// TCP even though the plugin's own GOOS is linux. On Linux (loopbackPort == 0)
// none of this fires and the forwarded port vars are inert (the unix listener
// ignores them), so the default transport is byte-for-byte unchanged.
func containerHandshakeEnv(cmdEnv []string, containerSocketDir string, loopbackPort int) []string {
	var out []string
	for _, kv := range cmdEnv {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case key == pb.HandshakeConfig.MagicCookieKey:
			out = append(out, kv)
		case key == "PLUGIN_UNIX_SOCKET_DIR":
			// Override: the host path go-plugin set is meaningless in the container.
			out = append(out, "PLUGIN_UNIX_SOCKET_DIR="+containerSocketDir)
		case loopbackPort > 0 && (key == "PLUGIN_MIN_PORT" || key == "PLUGIN_MAX_PORT"):
			// Pin the listener to the single published loopback port.
			out = append(out, fmt.Sprintf("%s=%d", key, loopbackPort))
		case strings.HasPrefix(key, "PLUGIN_"):
			out = append(out, kv)
		}
	}
	if loopbackPort > 0 {
		out = append(out, "PLUGIN_LISTEN_TCP=1")
	}
	return out
}
