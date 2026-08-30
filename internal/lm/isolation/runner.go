package isolation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin/runner"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

	// waited is CLOSED by Wait once the container process has been reaped, and
	// closing it is what publishes exitCode to any other goroutine. The
	// synchronisation is not defensive: go-plugin calls Wait on a goroutine of
	// its own and Diagnose on the caller's, so cmd.ProcessState is written by
	// one and would be read by the other. Reading it directly is a DATA RACE
	// (confirmed by the race detector, not reasoned about) — exec.Cmd populates
	// ProcessState inside Wait with no synchronisation of its own.
	waited   chan struct{}
	waitOnce sync.Once
	exitCode int

	// exitWait bounds how long Diagnose will wait for that reap. It has to wait
	// at all because of go-plugin's ORDERING: the stdout scanner closes its line
	// channel BEFORE releasing the wait-group that gates the Wait goroutine, and
	// Diagnose is called from the receive on that closed channel. So Diagnose
	// reliably runs BEFORE the process is reaped, and an opportunistic read
	// would report "no status" precisely when a status exists. The bound is what
	// keeps that from becoming a hang when the handshake failed with the
	// container still ALIVE (a garbage line, a truncated one), where nothing
	// will ever reap it. Zero in a bare literal, so a test that never starts a
	// process does not pay it.
	exitWait time.Duration

	containerAddrTranslator
}

// diagnoseExitWait bounds Diagnose's wait for the container's exit status.
// Generous relative to what it measures — the process has already written EOF
// to stdout, so its reap is imminent, not slow — and paid only on a path that
// has already failed to start a plugin.
const diagnoseExitWait = 3 * time.Second

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
		runtime:  rt,
		name:     spec.Name,
		cmd:      cmd,
		stdout:   stdout,
		stderr:   stderr,
		waited:   make(chan struct{}),
		exitWait: diagnoseExitWait,
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

// Wait blocks until the container process exits, then publishes its exit status
// for Diagnose. This is the ONLY place the status is read: cmd.Wait() is what
// writes ProcessState, so reading it here — before the close that hands it to
// other goroutines — is the one ordering in which that read is not a race.
func (r *containerRunner) Wait(_ context.Context) error {
	err := r.cmd.Wait()
	r.waitOnce.Do(func() {
		if r.cmd.ProcessState != nil {
			r.exitCode = r.cmd.ProcessState.ExitCode()
		}
		close(r.waited)
	})
	return err
}

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
	// The name-targeted force-remove (under our own timeout + already-gone
	// tolerance) is shared with the docker-direct RunnerHandle.Kill.
	removeContainer(ctx, r.runtime, r.name)
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

// Diagnose returns best-effort help when the plugin failed to negotiate.
//
// The container's EXIT STATUS is consulted first, because the most misleading
// failure this type can report is one that is not a transport fault at all: the
// ctxloom INSIDE the container reaching its own startup gate and refusing over a
// fatal config finding. That refusal crosses the boundary as nothing but a
// process status (its rendered finding goes to the container's stderr, which
// go-plugin forwards to the logger, not to the caller's error), so a fixed
// "check the image exists / the socket dir is bind-mounted" string sent every
// reader hunting a transport bug for a configuration problem. Naming the
// refusal — and the flag that overrides it — is the whole point.
//
// Only the go-plugin-handshake wording is kept for the case that genuinely is
// one: no status available, or an exit that carries no meaning of ours.
func (r *containerRunner) Diagnose(_ context.Context) string {
	lead := fmt.Sprintf("plugin container %q (%s)", r.name, r.runtime.Name())
	code, ok := r.exitStatus()
	switch {
	case ok && code == strictness.ExitCodeFatalFindings:
		return lead + fmt.Sprintf(" exited %d before the handshake: the ctxloom INSIDE the container"+
			" REFUSED TO START over a fatal startup finding (broken config, an unresolvable profile"+
			" or bundle) — a configuration refusal, not a transport fault."+
			" The finding is on the container's stderr above and names its own remedy;"+
			" re-run with --degraded to downgrade ordinary config findings to warnings and launch anyway.", code)
	case ok && code != 0:
		return lead + fmt.Sprintf(" exited %d before the handshake: the in-container ctxloom died"+
			" rather than serving the plugin. Check the container's stderr above, then that the image"+
			" exists and its ctxloom is executable.", code)
	}
	return lead + " did not negotiate the go-plugin handshake: " +
		"check the image exists, its ctxloom is executable, and the socket dir is bind-mounted"
}

// exitStatus reports the container process's exit code, waiting up to r.exitWait
// for the reap, and whether one became available. The ok is never collapsed into
// a bare -1: that reads as a real status and would be reported as one.
//
// A container still RUNNING is the case the bound exists for — nothing will
// reap it, so the wait must expire rather than block go-plugin's startup.
func (r *containerRunner) exitStatus() (int, bool) {
	if r.waited == nil {
		return 0, false
	}
	select {
	case <-r.waited:
		return r.exitCode, true
	default:
	}
	timer := time.NewTimer(r.exitWait)
	defer timer.Stop()
	select {
	case <-r.waited:
		return r.exitCode, true
	case <-timer.C:
		return 0, false
	}
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
// host namespace before the host connects to it: the socket the plugin created
// under containerSocketDir is reached by the host under the bind-mounted
// hostSocketDir, a plain prefix swap on the same file. Unix-socket-over-bind-mount
// is the only container plugin transport since 0.7 (the forked TCP-over-loopback
// transport was deleted with the fork — Linux only, where host and container
// share one kernel).
func (t containerAddrTranslator) PluginToHost(network, addr string) (string, string, error) {
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
// Windows correctness gap (DOCUMENTED not fixed here — see the
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
// project-mount translation (the pathMapper seam, runtime.go) this
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
// not hand-roll docker-exec + socket plumbing. The transport is unix-socket-over-
// bind-mount only (Linux, shared kernel) since 0.7 — the forked TCP-over-loopback
// path was deleted with the fork.
func containerRunnerFunc(rt Runtime, image, name, projectDir, home string, command []string, containerSocketDir string, extraEnv []string, extraMounts []Mount, spawnEnv map[string]string) pb.ContainerRunnerFunc {
	return func(_ hclog.Logger, cmd *exec.Cmd, hostSocketDir string) (runner.Runner, error) {
		spec := buildRunSpec(image, name, projectDir, home, command, containerSocketDir, hostSocketDir, cmd.Env, extraEnv, extraMounts, rt.mapper())
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
// with root/sudo privileges"). This is the runtime-side half of "the container is
// the boundary" — the policy-level Approvals axis that used to name the other
// half was deleted as dead: the run's actual approval posture resolves
// from config/CLI/agent, never from the isolation policy. Harmless to engines
// that ignore it.
var containerBaseEnv = []string{"IS_SANDBOX=1"}

// buildRunSpec assembles the RunSpec for one plugin container from the launch
// parameters. Env = the fixed container base env + the curated go-plugin handshake
// vars (from hostEnv, socket dir overridden to the container path) + the scoped
// auth extraEnv. Mounts = the project mount (cwd + .git resolve unchanged on the
// identity mapper) + the socket-dir mount + extraMounts (auth credential mounts +
// config overlays). Pure and deterministic so the mount/env wiring is unit-testable
// without a container.
//
// mapper (the pathMapper seam, container-runtime-bugs.plan.md §5) translates
// projectDir into the WorkDir + project-mount CONTAINER path: identityMapper
// (every caller today — see containerRunnerFunc, which reads it off rt.mapper())
// makes this byte-for-byte the identical-path mount buildRunSpec always built,
// so the seam changes NOTHING for the supported (Linux host-native / true DinD)
// topology. A future Windows mapper (drive-letter host path → POSIX in-
// container target) or DooD mapper (mountinfo-derived) plugs in HERE without
// touching this function's callers.
func buildRunSpec(image, name, projectDir, home string, command []string, containerSocketDir, hostSocketDir string, hostEnv, extraEnv []string, extraMounts []Mount, mapper pathMapper) RunSpec {
	mapper = runtimeMapper(mapper)
	containerWorkDir := mapper.toContainer(projectDir)
	env := append(append([]string(nil), containerBaseEnv...), containerHandshakeEnv(hostEnv, containerSocketDir)...)
	env = append(env, extraEnv...)
	mounts := append([]Mount{
		// Project mount: cwd + .git gitdir resolve unchanged UNDER THE IDENTITY
		// MAPPER; a non-identity mapper renders the container target through
		// the SAME translation WorkDir below uses, so the two never disagree.
		{Host: projectDir, Container: containerWorkDir},
		// The unix-socket dir go-plugin created, mounted to the container path.
		// containerSocketDir is already a FIXED in-container convention (not a
		// host-derived path), so it does not route through the mapper.
		{Host: hostSocketDir, Container: containerSocketDir},
	}, extraMounts...)
	return RunSpec{
		Image:   image,
		Name:    name,
		WorkDir: containerWorkDir,
		Home:    home,
		Command: command,
		Env:     env,
		Mounts:  mounts,
		// nil on every production run; set only under the isolation probe's
		// dedicated env var (traceProbeFromEnv) — the sole gate for the
		// renderRunSpec SYS_PTRACE grant. See TraceProbe.
		Trace: traceProbeFromEnv(),
	}
}

// containerHandshakeEnv curates the environment that crosses into the container:
// ONLY the go-plugin handshake vars (the magic cookie + PLUGIN_* — protocol
// versions, ports, mTLS cert, mux flag), never the host's full environment. It
// OVERRIDES PLUGIN_UNIX_SOCKET_DIR to the container path so the plugin creates its
// socket in the bind-mounted dir. Anything else (host paths, secrets) is dropped;
// a real engine's auth is a separate, deliberate follow-up (see the container
// policy docs). The transport is unix-socket-over-bind-mount only (Linux, shared
// kernel) since 0.7 — the forked TCP-over-loopback listener gate was deleted with
// the fork.
//
// The PLUGIN_ arm is a PREFIX match, deliberately: go-plugin owns that namespace
// and a version that adds a handshake var must not silently lose it here. That
// makes the "never the host's environment" half a property of the CALLER, not of
// this function — cmdEnv must carry go-plugin's own vars and nothing else, which
// pb.ContainerClientConfig guarantees with SkipHostEnv (without it go-plugin
// seeds the Cmd from os.Environ() and an ambient host PLUGIN_* would cross the
// boundary, onto a `run` argv that is world-readable via /proc/<pid>/cmdline).
// Non-PLUGIN_ host keys are dropped here regardless, as defence in depth.
func containerHandshakeEnv(cmdEnv []string, containerSocketDir string) []string {
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
		case strings.HasPrefix(key, "PLUGIN_"):
			out = append(out, kv)
		}
	}
	return out
}
