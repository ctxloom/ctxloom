package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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
	// PublishPort, when > 0, publishes that TCP port on host loopback
	// (-p 127.0.0.1:PORT:PORT). Non-zero only for the loopback plugin transport
	// (non-Linux hosts): the in-container plugin listens on TCP at this port and
	// the host go-plugin client dials it over loopback, crossing the Docker
	// Desktop VM boundary that a bind-mounted unix socket cannot. Zero on Linux,
	// where the unix-socket transport is used and no port is published.
	PublishPort int
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
type ociRuntime struct{}

// RemoveArgs force-removes the container (SIGKILL + rm; idempotent enough that a
// racing --rm auto-remove just reports "no such container", which callers ignore).
// Shared by Docker and Podman — the `rm -f <name>` argv is identical on both.
func (ociRuntime) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

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

// spawn is the moved body of the old Container.spawnInContainer: it launches the
// plugin INSIDE a container via the go-plugin RunnerFunc + AddrTranslator
// transport and returns its client (Kill force-removes the container). It is
// shared by Docker and Podman, which pass themselves as rt so the run argv is
// built by the CONCRETE runtime's RunArgs (rootless head and all) —
// containerRunnerFunc → buildRunSpec → rt.RunArgs, exactly as before. Every
// container convention arrives on the LaunchSpec, so the body threads image/
// binaryPath/home/socketDir/workdir/mounts/env identically to spawnInContainer.
func (ociRuntime) spawn(rt Runtime, launch LaunchSpec) (pb.Client, error) {
	command := []string{launch.BinaryPath, "llm", "serve", launch.BackendName}
	if launch.Label != "" {
		command = append(command, "--label", launch.Label)
	}

	// Diagnostic only (never secrets): how the in-container engine is authenticated.
	if launch.Verbosity > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: container auth via %s\n", launch.AuthMode)
	}

	// Pick the plugin transport: unix socket on Linux (fast, shared kernel), TCP
	// over host loopback off Linux where a bind-mounted unix socket cannot cross
	// the Docker Desktop VM boundary. A reservation failure is fatal to this run
	// (the caller degrades to None) — better than launching a container the host
	// could never reach.
	loopbackPort, err := loopbackPluginPort()
	if err != nil {
		return nil, err
	}
	if launch.Verbosity > 0 && loopbackPort > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: container plugin transport: TCP loopback 127.0.0.1:%d\n", loopbackPort)
	}

	name := containerName(launch.AgentID)
	runnerFunc := containerRunnerFunc(rt, launch.Image, name, launch.WorkDir, launch.Home, command, launch.ContainerSocketDir, launch.ExtraEnv, launch.ExtraMounts, loopbackPort)
	return pb.NewContainerClient(launch.BackendName, launch.Label, launch.Verbosity, runnerFunc, launch.HostSocketDir)
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

// Spawn launches the bare self-invoked plugin subprocess — the exact body of
// pb.DefaultClientFactory that the None and Worktree policies spawn. The
// workspace is expressed purely via the caller's RunOptions.WorkDir, so only
// BackendName/Label/Verbosity are consulted; the container fields are ignored.
func (Host) Spawn(launch LaunchSpec) (pb.Client, error) {
	return pb.NewSelfInvokingClientForLabel(launch.BackendName, launch.Label, launch.Verbosity)
}

// Expose renders the identity bind mount, same as the OCI runtimes — the host
// path IS the exposed path (no container namespace to remap into).
func (Host) Expose(host, target string, readOnly bool) Mount {
	return Mount{Host: host, Container: target, ReadOnly: readOnly}
}

// Chroot is the SHAPED-BUT-UNIMPLEMENTED daemonless/imageless runtime (a
// fast-follow): the interface accommodates a runtime that isolates the engine in
// a chroot/namespace WITHOUT a container image or a daemon, proving the Spawn +
// Expose seam needs no further shape change for it. It is deliberately never
// selected — Available() is false and SelectRuntime does NOT list it in its
// byName map — so nothing here runs today. Spawn errors, Expose/RunArgs/
// RemoveArgs are zero-valued. The real path-under-root exposure and the
// namespace launch are the fast-follow's work.
type Chroot struct{}

// Ensure the stub satisfies the runtime interface (the whole point: no further
// shape change is needed to add a daemonless/imageless runtime).
var _ Runtime = Chroot{}

// Name identifies the runtime.
func (Chroot) Name() string { return "chroot" }

// Binary is empty — a chroot runtime execs no container CLI.
func (Chroot) Binary() string { return "" }

// Available is always false — Chroot is never selected (not implemented).
func (Chroot) Available() bool { return false }

// RunArgs is a noop — there is no container `run` argv.
func (Chroot) RunArgs(RunSpec) []string { return nil }

// RemoveArgs is a noop — there is nothing to force-remove.
func (Chroot) RemoveArgs(string) []string { return nil }

// Spawn is not implemented — the daemonless launch is the fast-follow's work.
func (Chroot) Spawn(LaunchSpec) (pb.Client, error) {
	return nil, fmt.Errorf("chroot runtime: Spawn not implemented")
}

// Expose is a zero-valued stub — the real path-under-root mapping is the
// fast-follow's work.
func (Chroot) Expose(string, string, bool) Mount { return Mount{} }

// renderRunSpec renders the runtime-agnostic tail of a run argv (env, mounts,
// workdir, image, in-container command) shared by Docker and Podman. The
// runtime-specific head (--rm/--name/--user) is prepended by each RunArgs.
func renderRunSpec(spec RunSpec) []string {
	var args []string
	if spec.PublishPort > 0 {
		// Loopback plugin transport: bind the container's TCP listener to the
		// SAME host loopback port so the host go-plugin client can dial it. Only
		// 127.0.0.1 is published (never 0.0.0.0) — the plugin RPC is host-local.
		// --network host would be simpler but is unavailable on Docker Desktop
		// Mac/Windows (the container is in a VM), which is exactly where this path
		// runs, so an explicit loopback port-publish it is.
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", spec.PublishPort, spec.PublishPort))
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
	for _, m := range spec.Mounts {
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
	args = append(args, spec.Command...)
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
// hosts where podman serves the run.
func newDockerRuntime(reachable func(string) bool) Runtime {
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
func InContainer() bool { return len(containerMarkers()) > 0 }

// containerMarkers returns the matched in-container markers (empty = none),
// for InContainer and the `container check` diagnosis.
func containerMarkers() []string {
	return inContainerFrom(
		func(p string) error { _, err := os.Stat(p); return err },
		os.ReadFile,
		os.Getenv,
	)
}

// inContainerFrom is the seam-injected core of the in-container detection:
// stat/readFile/getenv arrive as functions so tests never touch the real
// /proc, sentinel files, or process env (CI itself runs inside containers,
// and the hostile-env suite junks the environment). Returns every marker that
// matched, named for diagnostics.
func inContainerFrom(stat func(string) error, readFile func(string) ([]byte, error), getenv func(string) string) []string {
	var markers []string
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if stat(f) == nil {
			markers = append(markers, f)
		}
	}
	for _, e := range []string{"REMOTE_CONTAINERS", "CODESPACES", "DEVCONTAINER", "KUBERNETES_SERVICE_HOST"} {
		if getenv(e) != "" {
			markers = append(markers, "$"+e)
		}
	}
	// cgroup v1 runtime signatures — BEST-EFFORT ONLY: cgroup v2 exposes a
	// bare "0::/" with no runtime marker, which is why the sentinel-file and
	// env probes lead and this never stands alone as a negative signal.
	if b, err := readFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "containerd", "kubepods", "/lxc/"} {
			if strings.Contains(s, marker) {
				markers = append(markers, "cgroup:"+marker)
			}
		}
	}
	return markers
}

// SelectRuntime picks the container runtime by config preference then detection.
// prefer is an explicit runtime name ("docker" | "podman"); empty means auto. It
// returns the first launchable runtime (prefer, else docker, else podman) with
// rootless detected for docker, or Host{} when none can launch. SelectRuntime
// itself never errors — a runtime that cannot launch is simply not selected;
// the caller decides the consequence: an EXPLICITLY-requested container that
// lands on Host is a fatal finding (ClassIsolation) it aborts on unless
// --degraded, while an ambient default degrades silently to the host.
func SelectRuntime(prefer string) Runtime {
	newDocker := func() Runtime { return newDockerRuntime(runtimeReachable) }
	byName := map[string]func() Runtime{
		"docker": newDocker,
		"podman": func() Runtime { return Podman{rootless: podmanIsRootless()} },
	}
	if prefer != "" {
		if mk, ok := byName[prefer]; ok {
			if rt := mk(); rt.Available() {
				return rt
			}
		}
		// An unknown or unavailable preference falls through to auto-detection.
	}
	for _, mk := range []func() Runtime{newDocker, byName["podman"]} {
		if rt := mk(); rt.Available() {
			return rt
		}
	}
	return Host{}
}
