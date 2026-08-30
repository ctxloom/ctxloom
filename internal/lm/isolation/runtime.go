package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/containerprobe"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Runtime is the pluggable container launcher — proper polymorphism, NOT
// an if/else over runtime names. Each implementation (Docker | Podman | Host)
// knows how to build the `run` argv (image, --rm, --name, -v mounts, -e env, -w
// workdir), how to tear a container down (rm -f), and whether it can actually
// launch right now. It is the Phase-0 SpawnClient seam made runtime-swappable: a
// new runtime is one more implementation, selected by detection/config.
type Runtime interface {
	// Name identifies the runtime ("docker" | "podman" | "host") for diagnostics.
	Name() string
	// Binary is the runtime CLI resolved for exec (e.g. "docker"). Empty for Host.
	Binary() string
	// Available reports whether this runtime can launch a container NOW: the CLI
	// is on PATH and its daemon is reachable. Drives runtime selection: when
	// nothing is available an EXPLICITLY-requested container is a fatal finding
	// (ClassIsolation, exit 3) unless --degraded, while an ambient default
	// degrades silently to the host.
	Available() bool
	// RunArgs builds the full argv (after Binary) that starts the container in the
	// FOREGROUND with stdout/stderr attached — go-plugin reads the plugin's
	// handshake line from the container's stdout, so no -d and no -t (a pty would
	// mangle the handshake).
	RunArgs(spec RunSpec) []string
	// RemoveArgs builds the argv that force-removes the named container (teardown).
	RemoveArgs(name string) []string
	// ExecArgs builds the argv (after Binary) that runs command inside an
	// ALREADY-RUNNING container by name — `exec -i [-t] [-e NAME…] <name>
	// <command…>`. tty adds `-t` (the interactive pty the docker-exec
	// vpio.Launcher drives an interactive turn over). env entries cross as bare
	// `-e NAME` name-only forwards (the value rides the exec PROCESS env, never
	// this world-readable argv) — the identical discipline renderRunSpec uses
	// for `run`. No docker SDK: plain CLI, identical on docker and podman. Noop
	// (nil) for Host/Chroot, which launch no container to exec into.
	ExecArgs(name string, tty bool, env []string, command []string) []string
	// Spawn launches the plugin client for a prepared run per this runtime's
	// launch convention: Host self-invokes a bare `ctxloom llm serve` subprocess;
	// the OCI runtimes go-plugin-run the plugin INSIDE a container (the moved
	// spawnInContainer body). It is the SpawnClient seam pushed onto the runtime,
	// so a Policy's SpawnClient is just workspace→LaunchSpec adaptation plus
	// runtime.Spawn — the runtime, not the policy, owns HOW the process starts.
	Spawn(launch LaunchSpec) (pb.Client, error)
	// Expose renders one host↔target exposure into a Mount for this runtime. For
	// the OCI runtimes (and Host) it is the identity bind Mount{host, target,
	// readOnly}; it exists as a seam so a future daemonless/imageless runtime
	// (Chroot) can map an exposure differently (e.g. a path under its root) at
	// the one delivery-layer primitive instead of at every Mount literal.
	Expose(host, target string, readOnly bool) Mount
	// ExposeMapped renders the bind Mount for hostPath — project dir, gitdir
	// mirror — with its Container side passed through this runtime's
	// pathMapper (§ lanky-pod): Mount{Host: hostPath, Container:
	// mapper().toContainer(hostPath)}. Under identityMapper (the default;
	// every Docker/Podman/Host construction path today) that is byte-for-byte
	// Expose(hostPath, hostPath, readOnly), so introducing this seam changes
	// NOTHING on Linux/host-native/true-DinD. It is NOT guaranteed identical
	// in general — a non-identity mapper (Windows drive-letter→POSIX; DooD
	// mountinfo-derived) plugs in at ONE place — mapper() below — rather than
	// at every call site that needs the SAME translation.
	ExposeMapped(hostPath string, readOnly bool) Mount
	// mapper returns this runtime's host↔container path translation
	// (unexported: an internal wiring seam, not part of the public contract
	// external packages implement). buildRunSpec reads it to translate the
	// project mount + WorkDir the SAME way ExposeMapped does, so the two
	// never disagree about where a host path lands in-container.
	mapper() pathMapper
}

// pathMapper translates a host filesystem path to the path the SAME resource
// appears at inside the container's mount namespace. It is the
// convergence point runtime.go's doc on Expose already anticipated
// ("Centralizing every delivery-layer Mount construction here is what lets a
// future daemonless runtime remap an exposure without touching each call
// site"): identical-path (Host==Container) is ONE configuration of this seam
// — identityMapper, the default — not a hardcoded assumption. An inverse
// toHost method used to sit on this interface for a future consumer that
// never arrived (zero production callers, only a test); deleted
// rather than kept as a speculative hook — add it back with the first real
// need for the reverse direction.
//
// Two non-identity mappers are anticipated (both DEFERRED past this task,
// which builds only the seam + identity default — see the container-runtime-
// bugs plan §4/§5):
//   - Windows: a drive-letter host path (C:\Users\foo\proj) needs a POSIX
//     in-container target (e.g. /workspace); a real implementation must also
//     handle the Docker-Desktop `/host_mnt/c/...` SOURCE form and rewrite the
//     socket-dir prefix swap (containerAddrTranslator) through the same
//     mapping — see swapPrefix's doc. The linked-worktree case additionally
//     needs the worktree's `gitdir:` FILE content rewritten (a Windows
//     absolute path is unresolvable as a mounted POSIX path unchanged) — a
//     known hard edge, explicitly deferred to sudsy-sip Tier C.
//   - DooD (docker-outside-of-docker): ctxloom itself runs inside a
//     container that shares the HOST daemon, so this process's paths are not
//     the daemon's paths; a mapper derived from /proc/self/mountinfo or
//     `docker inspect <self>` would translate them (snug-dawn, optional/
//     deferred — the shared-fs probe already detects and degrades this case
//     loudly rather than mounting the wrong path).
type pathMapper interface {
	// toContainer maps a host path to the in-container path the SAME resource
	// should be mounted/reached at.
	toContainer(hostPath string) string
}

// identityMapper is the default pathMapper: Host==Container, unconditionally.
// Every Docker/Podman/Host construction path uses it today (directly or via
// the nil-mapper fallback in withMapper/runtimeMapper), so the seam changes
// NOTHING for the supported topology — every identical-path Mount this
// package builds is byte-for-byte what a hardcoded Mount{Host: p, Container:
// p} literal produced before this seam existed.
type identityMapper struct{}

func (identityMapper) toContainer(hostPath string) string { return hostPath }

// runtimeMapper returns m if non-nil, else identityMapper{} — the nil-safe
// getter every Runtime's mapper() implements, so a runtime constructed
// without an explicit mapper (every call site today) is identity, not a nil-
// pointer hazard.
func runtimeMapper(m pathMapper) pathMapper {
	if m == nil {
		return identityMapper{}
	}
	return m
}

// funcMapper adapts a plain function to pathMapper — the backing type for
// NewDockerWithMapperForTest.
type funcMapper func(hostPath string) string

func (f funcMapper) toContainer(hostPath string) string { return f(hostPath) }

// NewDockerWithMapperForTest builds a Docker runtime carrying a custom
// host→container path-mapping function. mapper() is deliberately UNEXPORTED
// (this interface's own doc: "not part of the public contract external
// packages implement"), so a package outside internal/lm/isolation — notably
// internal/acp's container reach-back test — has no other way to construct a
// NON-IDENTITY Runtime and prove its call site actually routes a mount
// through the mapper seam rather than hardcoding Host==Container. No
// production caller uses this; every real construction path still passes a
// nil pathMap (identity).
func NewDockerWithMapperForTest(toContainer func(hostPath string) string) Docker {
	return Docker{ociRuntime: ociRuntime{pathMap: funcMapper(toContainer)}}
}

// LaunchSpec carries the launch conventions Spawn needs to start one plugin run,
// independent of which runtime realizes it (SD7). It is the union of what
// Container.spawnInContainer took as arguments (the per-run BackendName/Label/
// Verbosity/AgentID, the WorkDir mounted as cwd, the go-plugin HostSocketDir, the
// scoped auth ExtraEnv + credential/overlay ExtraMounts, and the diagnostic
// AuthMode) and the container conventions it read off the Container (Image,
// BinaryPath, Home, ContainerSocketDir). Host ignores every container field and
// uses only BackendName/Label/Verbosity.
type LaunchSpec struct {
	BackendName string // engine backend the plugin serves (`llm serve <backend>`)
	Label       string // resolved config label (--label; may be empty)
	Verbosity   int    // plugin log verbosity + auth/transport diagnostics gate
	AgentID     string // scopes the teardown-targetable container name

	Image              string // OCI: image reference to run
	BinaryPath         string // OCI: the container's ctxloom path (runs `llm serve`)
	Home               string // OCI: fresh $HOME inside the container
	ContainerSocketDir string // OCI: fixed in-container unix-socket bind target
	WorkDir            string // OCI: cwd + identical-path project/worktree mount
	HostSocketDir      string // OCI: host scratch dir go-plugin created the socket under

	ExtraEnv    []string          // scoped auth env passthrough threaded into the run
	ExtraMounts []Mount           // auth credential mounts + config overlays + gitdir mirror
	AuthMode    containerAuthMode // how auth resolved (diagnostics; no secrets)

	// SpawnEnv is the per-spawn env stamped onto the runner PROCESS (the
	// coordinator reach-back trio + session identity). Host: appended to the
	// subprocess cmd.Env. OCI: each key crosses as a bare-name `-e NAME`
	// (value read from the `run` process env, which gets the KEY=VAL — the
	// credential never lands in the world-readable argv).
	SpawnEnv map[string]string
}

// RunSpec is the runtime-agnostic description of one plugin container: which image
// to run, the in-container argv, the identical-path project mount + the
// host↔container socket-dir mount, a fresh HOME, and the curated go-plugin
// handshake env. A Runtime renders it into its own `run` argv.
type RunSpec struct {
	Image   string   // image reference to run
	Name    string   // --name, so teardown can target this exact container
	WorkDir string   // -w and the identical-path project bind-mount target
	Home    string   // fresh $HOME inside the container (engine global state isolated)
	Command []string // in-container argv (the container's ctxloom + "llm serve …")
	Env     []string // -e KEY=VAL, the curated go-plugin handshake env
	Mounts  []Mount  // --mount type=bind bind mounts

	// Trace, when non-nil, marks a PROBE-ONLY run: renderRunSpec then grants
	// --cap-add=SYS_PTRACE, bind-mounts the trace dir out, and wraps Command in
	// strace to observe the engine's file READS. NIL on every production run —
	// the structural gate that keeps SYS_PTRACE unreachable from a normal
	// `ctxloom run`. Set solely by traceProbeFromEnv. See TraceProbe.
	Trace *TraceProbe
}

// Mount is one bind mount rendered as `--mount type=bind,source=,target=[,readonly]`.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

// ociRuntime is the shared OCI (Docker/Podman) base: the two engines have a
// docker-CLI-compatible `run`/`rm` grammar, so everything that does not depend
// on the rootless identity head lives here once. It owns RemoveArgs (identical
// `rm -f <name>` teardown) and runArgs — the shared RunArgs TAIL that appends
// the runtime-agnostic renderRunSpec render onto a runtime-specific HEAD. Docker
// and Podman embed it and keep ONLY their Name/Binary/Available and their
// rootless-specific run-arg head (identityEnvArgs stays shared, called from each
// head). The rootless flag itself stays on the concrete types: it is consulted
// solely by that per-type head, so the base never needs it.
// pathMap, when set, is this runtime's non-identity pathMapper (the pathMapper
// seam) — nil (the zero value, every construction path today) is identity via
// runtimeMapper. No constructor in this package sets it yet; it is the
// injection point a future Windows/DooD-aware SelectRuntime would populate.
type ociRuntime struct{ pathMap pathMapper }

// RemoveArgs force-removes the container (SIGKILL + rm; idempotent enough that a
// racing --rm auto-remove just reports "no such container", which callers ignore).
// Shared by Docker and Podman — the `rm -f <name>` argv is identical on both.
func (ociRuntime) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

// ExecArgs renders `exec -i [-t] [-e NAME…] <name> <command…>` — the shared
// OCI grammar (docker and podman render identically). `-i` is unconditional
// (the exec's stdin is always wired to the host pty); `-t` is added only for an
// interactive turn (the docker CLI's own "-t requires a tty" check is satisfied
// by the host pty pair the Launcher runs this argv under). Each env entry is a
// BARE NAME forwarded via `-e NAME`, so a credential value never lands on the
// argv — matching renderRunSpec's bare-`-e` discipline for `run`.
func (ociRuntime) ExecArgs(name string, tty bool, env []string, command []string) []string {
	args := []string{"exec", "-i"}
	if tty {
		args = append(args, "-t")
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, name)
	return append(args, command...)
}

// runArgs assembles the full `run` argv from a runtime-specific HEAD (the
// --rm/--name/--user/identity prefix each concrete runtime builds) and the
// shared, runtime-agnostic TAIL (env, mounts, workdir, image, in-container
// command) rendered by renderRunSpec. The single append site both Docker and
// Podman funnel through.
func (ociRuntime) runArgs(head []string, spec RunSpec) []string {
	return append(head, renderRunSpec(spec)...)
}

// Expose renders one exposure as an identical-path bind mount — the OCI
// primitive, shared by Docker and Podman (and matching Host). Centralizing every
// delivery-layer Mount construction here is what lets a future daemonless runtime
// remap an exposure without touching each call site.
func (ociRuntime) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeMapped renders the Mount for hostPath with its Container side routed
// through this runtime's pathMapper — identityMapper by default, so this is
// byte-for-byte Expose(hostPath, hostPath, readOnly) until a non-identity
// mapper is wired; a non-identity mapper changes ONLY the Container side.
func (rt ociRuntime) ExposeMapped(hostPath string, readOnly bool) Mount {
	return Mount{Host: hostPath, Container: rt.mapper().toContainer(hostPath), ReadOnly: readOnly}
}

// mapper returns this runtime's pathMapper (identity when unset).
func (rt ociRuntime) mapper() pathMapper { return runtimeMapper(rt.pathMap) }

// containerSpawnUnsupportedErr is the platform gate for the go-plugin container
// SpawnClient path: it returns a fail-loud error on any non-Linux host and nil on
// Linux. The transport is unix-socket-over-bind-mount — the plugin creates its
// socket in a dir bind-mounted from the host, which is only a live endpoint when
// host and container share ONE kernel (Linux). The forked TCP-over-loopback
// transport that bridged the Docker Desktop VM boundary on macOS/Windows was
// deleted in 0.7 (it opened a routable in-container plugin listener on the shared
// bridge), so this path is now Linux-only. Its only residual callers
// are NON-top-level: the legacy degraded/fallback delegation and the oneshot
// fallback a delegated child takes on a backend without structured-chat support.
// Top-level container runs (docker-exec / Transport 2) and delegated agents
// (docker-direct) never reach here and are unaffected on every platform. goos is
// a parameter (not read from runtime.GOOS inline) so the gate is unit-testable,
// advertiseHostFor style.
func containerSpawnUnsupportedErr(goos string) error {
	if goos == "linux" {
		return nil
	}
	return fmt.Errorf("this legacy container spawn path (degraded/fallback delegation, oneshot-fallback delegated children) is Linux-only since 0.7: the container TCP transport was removed to close a security hole. Run these on Linux, or use the fully-supported top-level run modes; top-level runs and delegated agents are unaffected on this platform (%s)", goos)
}

// spawn is the moved body of the old Container.spawnInContainer: it launches the
// plugin INSIDE a container via the go-plugin RunnerFunc + AddrTranslator
// transport and returns its client (Kill force-removes the container). It is
// shared by Docker and Podman, which pass themselves as rt so the run argv is
// built by the CONCRETE runtime's RunArgs (rootless head and all) —
// containerRunnerFunc → buildRunSpec → rt.RunArgs, exactly as before. Every
// container convention arrives on the LaunchSpec, so the body threads image/
// binaryPath/home/socketDir/workdir/mounts/env identically to spawnInContainer.
func (ociRuntime) spawn(rt Runtime, launch LaunchSpec) (pb.Client, error) {
	// goos-gated fail-loud: this go-plugin SpawnClient path is Linux-only since
	// 0.7 (the container TCP transport was deleted). runtime.GOOS is passed to a
	// parameterized helper so the gate is unit-testable (advertiseHostFor style).
	if err := containerSpawnUnsupportedErr(runtime.GOOS); err != nil {
		return nil, err
	}

	command := []string{launch.BinaryPath, "llm", "serve", launch.BackendName}
	if launch.Label != "" {
		command = append(command, "--label", launch.Label)
	}

	// Diagnostic only (never secrets): how the in-container engine is authenticated.
	if launch.Verbosity > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: container auth via %s\n", launch.AuthMode)
	}

	name := containerName(launch.AgentID)
	// The container name is the ONLY handle on a running agent when tmux is
	// unavailable — `docker logs -f <name>` / `docker attach <name>` are the
	// documented fallback. It carries a random suffix so no human can derive
	// it from the agent id, and the container is force-removed on teardown
	// (AttachedContainer.Close), so its logs are LIVE-ONLY. Spawn is therefore
	// the one moment this name is both knowable and useful: print it
	// unconditionally rather than behind -v.
	fmt.Fprintf(os.Stderr, "ctxloom: container %s (watch: docker logs -f %s)\n", name, name)
	runnerFunc := containerRunnerFunc(rt, launch.Image, name, launch.WorkDir, launch.Home, command, launch.ContainerSocketDir, launch.ExtraEnv, launch.ExtraMounts, launch.SpawnEnv)
	// Assigned to the concrete *pb.LLMRunner, not returned directly: a bare
	// `return pb.NewContainerClient(...)` would let Go auto-convert a failed
	// call's typed-nil *LLMRunner into a non-nil pb.Client interface value
	// (Go's typed-nil-in-interface pitfall) at this exact return-statement
	// boundary — the point where a concrete type meets an interface return
	// type. An explicit nil interface on error is what every caller up the
	// chain (Container/Worktree/None.SpawnClient, and cli/run.go's
	// `if st.client != nil` teardown check) needs to see.
	client, err := pb.NewContainerClient(launch.Verbosity, runnerFunc, launch.HostSocketDir)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Docker launches containers via the docker CLI. rootless records whether the
// daemon is rootless — the axis that decides the run's identity mapping:
// under rootless docker the container's ROOT user maps to the invoking host
// user (the only uid that does — a non-root container user would map to a
// subuid and wreck bind-mount ownership), so the run stays container-root and
// no PUID is passed. Under a rootful daemon the container starts as root and
// PUID/PGID tell the image entrypoint to remap its `ctxloom` user to the
// launching uid/gid and drop to it — named non-root identity with correct
// host-side ownership (socket and project files land launching-user-owned).
type Docker struct {
	ociRuntime
	rootless bool
}

// Name identifies the runtime.
func (Docker) Name() string { return "docker" }

// Binary is the docker CLI.
func (Docker) Binary() string { return "docker" }

// Available reports docker CLI on PATH + a reachable daemon.
func (Docker) Available() bool { return runtimeReachable("docker") }

// RunArgs renders the spec into a `docker run` argv: the rootless-specific
// identity HEAD plus the shared renderRunSpec tail (via ociRuntime.runArgs).
func (d Docker) RunArgs(spec RunSpec) []string {
	args := []string{"run", "--rm", "--name", spec.Name}
	if !d.rootless {
		// Rootful daemon: the entrypoint remaps ctxloom to the launching
		// uid/gid and drops privileges. Rootless already maps root→host user.
		args = append(args, identityEnvArgs()...)
	}
	return d.runArgs(args, spec)
}

// Spawn launches the plugin in a docker container. It forwards to the shared
// ociRuntime.spawn, passing d so the run argv is built by Docker.RunArgs (the
// rootless-aware head) — embedding alone could not recover the concrete type.
func (d Docker) Spawn(launch LaunchSpec) (pb.Client, error) { return d.spawn(d, launch) }

// identityEnvArgs renders the PUID/PGID env that tells the agent image's
// entrypoint to remap its generic ctxloom user to the launching uid/gid and
// drop privileges to it.
func identityEnvArgs() []string {
	args := []string{
		"-e", fmt.Sprintf("PUID=%d", os.Getuid()),
		"-e", fmt.Sprintf("PGID=%d", os.Getgid()),
	}
	if strictness.Degraded() {
		// The entrypoint REFUSES to run the engine as root when it cannot
		// become the PUID identity (no usable gosu/setpriv) — in strict mode
		// that refusal fails the launch loudly. Degraded mode is the one
		// warn-and-continue home (CLAUDE.md), so it alone passes the escape
		// hatch that downgrades the refusal to warn-and-run-as-root.
		args = append(args, "-e", "CTXLOOM_ALLOW_ROOT=1")
	}
	return args
}

// Podman launches containers via the podman CLI. podman's run/rm argv is
// docker-CLI-compatible. rootless records whether the engine is rootless:
// rootless podman needs --userns=keep-id so the launching uid maps to ITSELF
// in-container instead of a subuid — then the entrypoint's PUID/PGID remap
// yields a run that is genuinely non-root in-container AND correctly owned on
// the host, something rootless docker cannot express. Rootful podman behaves
// like rootful docker (identity mapping; entrypoint remap only). (Built but
// not daemon-tested on this host — no podman installed.)
type Podman struct {
	ociRuntime
	rootless bool
}

// Name identifies the runtime.
func (Podman) Name() string { return "podman" }

// Binary is the podman CLI.
func (Podman) Binary() string { return "podman" }

// Available reports podman CLI on PATH + a reachable engine.
func (Podman) Available() bool { return runtimeReachable("podman") }

// RunArgs renders the spec into a `podman run` argv (docker-compatible): the
// rootless-specific keep-id/identity HEAD plus the shared renderRunSpec tail
// (via ociRuntime.runArgs). Both modes start as container-root and let the
// image entrypoint remap ctxloom to the launching uid/gid (PUID/PGID) and drop
// to it; rootless additionally needs keep-id so that uid maps to itself on the
// host instead of a subuid.
func (p Podman) RunArgs(spec RunSpec) []string {
	args := []string{"run", "--rm", "--name", spec.Name}
	if p.rootless {
		// keep-id's DEFAULT user is the host uid (not root), which couldn't
		// remap; enter as namespaced root so the entrypoint can usermod+drop.
		args = append(args, "--userns=keep-id", "--user", "0:0")
	}
	args = append(args, identityEnvArgs()...)
	return p.runArgs(args, spec)
}

// Spawn launches the plugin in a podman container, forwarding to the shared
// ociRuntime.spawn with p as rt so the run argv is built by Podman.RunArgs (the
// keep-id/identity head).
func (p Podman) Spawn(launch LaunchSpec) (pb.Client, error) { return p.spawn(p, launch) }

// podmanIsRootless reports whether the podman engine is rootless (`podman info`
// security flag). Best-effort: on any error it returns true — podman is
// rootless by default, and keep-id under a rootful engine errors loudly at
// launch (degrading the run) while a missing keep-id under rootless silently
// wrecks bind-mount ownership, the worse failure.
func podmanIsRootless() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.Security.Rootless}}").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != "false"
}

// Host is the non-container runtime: the plugin runs as a bare host subprocess
// (the None and — Phase 2 — worktree policies). It satisfies Runtime so
// runtime selection is uniform, but it launches nothing itself: the host path
// spawns via pb.NewSelfInvokingClientForLabel, so RunArgs/RemoveArgs are unused.
type Host struct{}

// Name identifies the runtime.
func (Host) Name() string { return "host" }

// Binary is empty — Host execs the plugin directly, not via a container CLI.
func (Host) Binary() string { return "" }

// Available is always true — the host can always run a subprocess (None never
// fails; it is the fault-tolerant floor).
func (Host) Available() bool { return true }

// RunArgs is a noop — Host does not launch a container.
func (Host) RunArgs(RunSpec) []string { return nil }

// RemoveArgs is a noop — Host has nothing to tear down.
func (Host) RemoveArgs(string) []string { return nil }

// ExecArgs is a noop — Host launches no container to exec into.
func (Host) ExecArgs(string, bool, []string, []string) []string { return nil }

// Spawn launches the bare self-invoked plugin subprocess — the exact body of
// pb.DefaultClientFactory that the None and Worktree policies spawn. The
// workspace is expressed purely via the caller's RunOptions.WorkDir, so only
// BackendName/Label/Verbosity are consulted; the container fields are ignored.
func (Host) Spawn(launch LaunchSpec) (pb.Client, error) {
	// Assigned to the concrete *pb.LLMRunner, not returned directly — see
	// ociRuntime.spawn's matching comment for why a bare `return pb.New...(...)`
	// here would box a failed spawn's typed-nil *LLMRunner into a non-nil
	// pb.Client interface value instead of the honest nil callers check for.
	client, err := pb.NewSelfInvokingClientForLabelEnv(launch.BackendName, launch.Label, launch.Verbosity, launch.SpawnEnv)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// Expose renders the identity bind mount, same as the OCI runtimes — the host
// path IS the exposed path (no container namespace to remap into).
func (Host) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// ExposeMapped is the identity mount for Host specifically — Host has no
// container namespace to remap into, so this is unconditionally
// Expose(hostPath, hostPath, readOnly) (matching Host.mapper() below, always
// identityMapper).
func (Host) ExposeMapped(hostPath string, readOnly bool) Mount {
	return Mount{Host: hostPath, Container: hostPath, ReadOnly: readOnly}
}

// mapper is always identity — Host launches no container, so there is no
// host↔container path translation to perform.
func (Host) mapper() pathMapper { return identityMapper{} }

// renderRunSpec renders the runtime-agnostic tail of a run argv (env, mounts,
// workdir, image, in-container command) shared by Docker and Podman. The
// runtime-specific head (--rm/--name/--user) is prepended by each RunArgs.
func renderRunSpec(spec RunSpec) []string {
	var args []string
	// PROBE-ONLY: a non-nil Trace overrides Docker's default seccomp profile
	// with the probe profile (default policy + the ptrace family allowed), which
	// is what lets strace trace its own children in-container. NO capability is
	// granted — strace parents the tracee, so the ptrace permission model needs
	// none; only the seccomp syscall filter had to be loosened, and only for the
	// ptrace family. This is the SOLE site that can apply the override, and it
	// fires only when the spec explicitly carries a Trace — nil on every
	// production run, which therefore keeps Docker's default profile. A
	// `--security-opt` is a `run` flag and must precede the image, which the
	// append order guarantees. SeccompProfile is empty only if the probe could
	// not materialize the profile file; then we skip the override (default
	// profile stands) rather than run with a broken path.
	if spec.Trace != nil && spec.Trace.SeccompProfile != "" {
		args = append(args, "--security-opt", "seccomp="+spec.Trace.SeccompProfile)
	}
	if spec.Home != "" {
		args = append(args, "-e", "HOME="+spec.Home)
	}
	// Each Env entry renders as `-e <entry>`. Two forms cross here, both native to
	// the docker/podman `-e` grammar: "KEY=VAL" sets an explicit value (the
	// go-plugin handshake vars, IS_SANDBOX, TERM), while a BARE "KEY" (no '=') is a
	// name-only passthrough — the runtime forwards the VALUE from its own inherited
	// environment. Auth SECRETS use the bare-name form (containerAuth.envPassthrough)
	// so the value never lands in this long-lived `run` argv, which is world-readable
	// via /proc/<pid>/cmdline; it stays in the launcher's env only.
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	mounts := spec.Mounts
	if spec.Trace != nil {
		// PROBE-ONLY: bind-mount the trace dir OUT so the strace output written
		// from inside survives the container's `--rm` teardown — no docker cp
		// race. A separate slice so the spec's own Mounts are never mutated.
		mounts = append(append([]Mount(nil), mounts...),
			Mount{Host: spec.Trace.HostDir, Container: spec.Trace.ContainerDir})
	}
	for _, m := range mounts {
		// --mount (not -v host:container[:ro]): the colon-delimited -v grammar is
		// ambiguous on Windows, where a host path carries a drive-letter colon
		// (C:\...) that mis-splits. --mount type=bind,source=,target=[,readonly]
		// is colon-free and renders identically on docker + podman + Linux. Every
		// mount in this package funnels through here, so this is the single site.
		// (--mount requires the source to already exist; every Mount.Host in this
		// package is a path we created or verified before the run, so that holds.)
		opt := "type=bind,source=" + m.Host + ",target=" + m.Container
		if m.ReadOnly {
			opt += ",readonly"
		}
		args = append(args, "--mount", opt)
	}
	if spec.WorkDir != "" {
		args = append(args, "-w", spec.WorkDir)
	}
	args = append(args, spec.Image)
	command := spec.Command
	if spec.Trace != nil {
		// PROBE-ONLY: wrap the in-container engine exec in strace so the vendor
		// CLI's file READS (incl. ENOENT probes) are captured. The image
		// ENTRYPOINT (ctxloom-entrypoint) execs "$@", so strace becomes the
		// direct child and `-f` follows the fork into ctxloom and the engine.
		command = append(straceWrapPrefix(spec.Trace), spec.Command...)
	}
	args = append(args, command...)
	return args
}

// runtimeReachable reports whether a container runtime CLI is on PATH and its
// daemon answers `<bin> info`. Any failure (missing binary, daemon down) →
// false → the caller degrades down the chain to None: a fatal finding
// (ClassIsolation) the choke owner aborts on when a container was EXPLICITLY
// requested, unless --degraded; an ambient default degrades silently.
func runtimeReachable(bin string) bool {
	if _, err := exec.LookPath(bin); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// `info` succeeds only when the daemon/engine is reachable; discard its output.
	cmd := exec.CommandContext(ctx, bin, "info")
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// dockerSecurityOptions probes the docker daemon's security options; a package
// var so tests drive the undecidable-probe path hermetically (mirrors the
// resolveSelfExe / sharedFSCheck seams).
var dockerSecurityOptions = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.SecurityOptions}}").Output()
	return string(out), err
}

// dockerIsRootless reports whether the docker daemon is rootless (its `info`
// SecurityOptions list "rootless"). Called only for a REACHABLE daemon
// (newDockerRuntime gates on reachability), so a probe failure here is a
// genuinely undecidable identity direction — not a missing CLI or a down
// daemon — and it must not pick one silently: the answer decides whether PUID
// is injected, i.e. who OWNS every file the run writes. INVARIANT on error:
// assume ROOTFUL (inject PUID) and route a finding. Wrongly assuming rootful
// under a rootless daemon skews project-file ownership to a subordinate uid —
// wrong, but confined to the launching user's privileges; wrongly assuming
// rootless under a rootful daemon would run the engine as REAL root and
// root-own project files — strictly worse. Strict mode collects the finding
// (the choke owner aborts pre-launch); --degraded proceeds on the assumption
// with the streamed warning.
func dockerIsRootless() bool {
	out, err := dockerSecurityOptions()
	if err != nil {
		strictness.Fail(strictness.ClassIsolation,
			"check `docker info --format '{{.SecurityOptions}}'` against the daemon and retry, or pass --degraded to proceed assuming a rootful daemon",
			"cannot determine whether the docker daemon is rootless (%v); assuming rootful — if it is actually rootless, files the container writes will land owned by a subordinate uid", err)
		return false
	}
	return strings.Contains(out, "rootless")
}

// newDockerRuntime constructs the Docker runtime for selection. The rootless
// identity probe runs only when the daemon is REACHABLE: an unreachable
// docker is never selected (Available gates selection), so probing it would
// only manufacture a spurious identity finding on docker-less or daemon-down
// hosts where podman serves the run. It returns the CONCRETE Docker so the
// candidate table can read the probed rootless-ness straight off it rather
// than re-deriving ownership downstream, where it could drift from the flag
// the run argv is actually built with.
func newDockerRuntime(reachable func(string) bool) Docker {
	if !reachable("docker") {
		return Docker{}
	}
	return Docker{rootless: dockerIsRootless()}
}

// InContainer reports whether THIS process is already running inside a
// container (dev container, CI job, pod). Markers: the docker/podman sentinel
// files, the well-known dev-container/k8s env vars, and container-runtime
// signatures in the init cgroup. It gates the devcontainer-specific wording on
// degrade warnings and feeds `container check`; the actual can-containers-
// launch decision is behavior-based (runtime reachability + the shared-fs
// probe), never this heuristic alone.
func InContainer() bool { return containerprobe.InContainer() }

// containerMarkers returns the matched in-container markers (empty = none),
// for InContainer and the `container check` diagnosis.
//
// The probe itself lives in internal/shared/containerprobe because
// internal/lm/backends' mock records the same answer as evidence of where an
// engine ran, and backends cannot import this package (isolation -> backends
// -> acp -> isolation). Two copies of the marker list would drift the first
// time a runtime changed a sentinel.
func containerMarkers() []string { return containerprobe.Markers() }

// inContainerFrom is retained as this package's name for the injected seam so
// the detection stays testable from here — the behavior is one definition, in
// containerprobe.
func inContainerFrom(stat func(string) error, readFile func(string) ([]byte, error), getenv func(string) string) []string {
	return containerprobe.MarkersFrom(stat, readFile, getenv)
}

// ownershipAxis maps a PROBED rootless-ness onto the runtime-axis value that
// runtime satisfies. The single mapping site: ownership is recorded where it
// is probed (dockerIsRootless / podmanIsRootless), never re-derived later from
// a runtime's type or name, where it could drift from the very flag its run
// argv is built with (Docker.RunArgs / Podman.RunArgs both branch on it).
func ownershipAxis(rootless bool) RuntimeAxis {
	if rootless {
		return RuntimeContainerRootless
	}
	return RuntimeContainerRootful
}

// runtimeCandidate is one selectable container runtime: the name a config
// preference names it by, and a probe that constructs it AND reports the
// container ownership it provides. probe shells out to the runtime CLI, so it
// is called lazily — only until a candidate is accepted.
type runtimeCandidate struct {
	name  string
	probe func() (Runtime, RuntimeAxis)
}

// runtimeCandidates is the ORDERED auto-detection list (docker, then podman).
// A package var, and a func rather than a slice literal, so tests drive
// selection — including the ownership filter — hermetically: the real probes
// exec `docker info` / `podman info` against live daemons, which a unit test
// must never depend on. Mirrors the dockerSecurityOptions / sharedFSCheck /
// selectRuntimeProbe seams.
var runtimeCandidates = func() []runtimeCandidate {
	return []runtimeCandidate{
		{"docker", func() (Runtime, RuntimeAxis) {
			d := newDockerRuntime(runtimeReachable)
			return d, ownershipAxis(d.rootless)
		}},
		{"podman", func() (Runtime, RuntimeAxis) {
			p := Podman{rootless: podmanIsRootless()}
			return p, ownershipAxis(p.rootless)
		}},
	}
}

// selectRuntimeWhere walks the candidates — config preference first, then
// detection order — and returns the first that is launchable AND accepted by
// ok, or Host{} when none is. It never errors: a runtime that cannot serve is
// simply not selected, and the caller decides the consequence (chainFor makes
// an EXPLICITLY-requested container that lands on Host a fatal ClassIsolation
// finding unless --degraded). SelectRuntime and ProbeRuntime differ ONLY in ok.
func selectRuntimeWhere(prefer string, ok func(owns RuntimeAxis) bool) Runtime {
	candidates := runtimeCandidates()
	pick := func(c runtimeCandidate) Runtime {
		rt, owns := c.probe()
		if rt.Available() && ok(owns) {
			return rt
		}
		return nil
	}
	if prefer != "" {
		for _, c := range candidates {
			if c.name == prefer {
				if rt := pick(c); rt != nil {
					return rt
				}
			}
		}
		// An unknown, unavailable, or ownership-mismatched preference falls
		// through to auto-detection.
	}
	for _, c := range candidates {
		if rt := pick(c); rt != nil {
			return rt
		}
	}
	return Host{}
}

// SelectRuntime picks the container runtime that can serve a run DEMANDING the
// want ownership. prefer is an explicit runtime name ("docker" | "podman");
// empty means auto-detect (docker, then podman).
//
// A candidate is selectable only when it is launchable AND its probed
// ownership IS want. A rootful request on a host offering only a rootless
// runtime returns Host{}, not the rootless runtime — handing back the other
// ownership mode is the exact silent substitution the two container axis
// values exist to prevent, and it stays forbidden under --degraded, which
// falls back to the HOST (chainFor) rather than to the other mode.
//
// want that is not a container axis value ("", "host", a typo) demands no
// container at all, so none is selected: Host{}. That totality is deliberate —
// a caller that forgot to thread the demand can never accidentally receive a
// container. The genuinely unconstrained question ("what runtime is reachable
// on this host?") is ProbeRuntime, which has to be asked for by name.
func SelectRuntime(prefer string, want RuntimeAxis) Runtime {
	if !IsContainerRuntimeAxis(want) {
		return Host{}
	}
	return selectRuntimeWhere(prefer, func(owns RuntimeAxis) bool { return owns == want })
}

// ProbeRuntime picks the first launchable container runtime with NO ownership
// demand — the "what container runtime is reachable here?" question asked by
// diagnostics (`container check`) and by an image BUILD, neither of which
// commits a run to a boundary. A RUN must never use this: it would take
// whichever ownership the host happens to offer, which is what SelectRuntime's
// filter exists to stop. Returns Host{} when nothing can launch.
func ProbeRuntime(prefer string) Runtime {
	return selectRuntimeWhere(prefer, func(RuntimeAxis) bool { return true })
}
