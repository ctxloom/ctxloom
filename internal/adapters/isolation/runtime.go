package isolation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/containerprobe"
	"github.com/ctxloom/ctxloom/internal/shared/hostnet"
)

// Runtime is the pluggable container launcher — proper polymorphism, NOT
// an if/else over runtime names. Each implementation (Docker | Podman | Host)
// knows how to build the `run` argv (image, --rm, --name, -v mounts, -e env, -w
// workdir), how to tear a container down (rm -f), and whether it can actually
// launch right now. A new runtime is one more implementation, selected by
// detection/config.
type Runtime interface {
	// Name identifies the runtime ("docker" | "podman" | "host") for diagnostics.
	Name() string
	// Binary is the runtime CLI resolved for exec (e.g. "docker"). Empty for Host.
	Binary() string
	// Available reports whether this runtime can launch a container NOW: the CLI
	// is on PATH and its daemon is reachable. Drives runtime selection: when
	// nothing is available a requested container is a non-degradable fatal
	// finding (ClassIsolation, exit 3): the run is refused, never moved to the
	// host.
	Available() bool
	// RunArgs builds the full argv (after Binary) that starts the container in the
	// FOREGROUND with stdout/stderr attached — no -d, and -t only when the spec
	// says so (RunSpec.TTY: an interactive launch's runner on the originator's
	// pty).
	RunArgs(spec RunSpec) []string
	// RemoveArgs builds the argv that force-removes the named container (teardown).
	RemoveArgs(name string) []string
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
	// external packages implement). buildRunnerSpec reads it to translate the
	// project mount + WorkDir the SAME way ExposeMapped does, so the two
	// never disagree about where a host path lands in-container.
	mapper() pathMapper
	// Enumerate lists containers whose name starts with namePrefix that are
	// RUNNING right now (no -a: --rm already means an EXITED container is
	// already gone by itself, so there is nothing among exited containers for
	// a caller to find — see container_reap.go's doc). Each result's Labels
	// carries exactly what ownerLabelArgs stamped at `run` time, verbatim; a
	// caller decides what an absent or malformed one means. This is what
	// ReapOrphanedContainers uses instead of composing `ps` argv itself: Host
	// launches no containers and always returns nil, nil.
	Enumerate(ctx context.Context, namePrefix string) ([]ContainerInfo, error)

	// The methods below are the CLI grammar a runtime's own tooling differs
	// in. Unexported like mapper(): internal wiring, each with one default on
	// ociRuntime and overridden only by the runtime that really differs.

	// inspectRunningArgs builds the argv that prints "true" while name runs.
	inspectRunningArgs(name string) []string
	// imageInspectArgs builds the argv that inspects image, rendering format
	// when it is non-empty (a bare inspect answers only "does it exist").
	imageInspectArgs(image, format string) []string
	// daemonNameTemplate is the `info` Go template naming the daemon's host.
	daemonNameTemplate() string
	// removeOutcome reads what this runtime's CLI said about a RemoveArgs run.
	removeOutcome(stdout []byte, err error) removeOutcome
	// passesPUID reports whether a run passes the PUID/PGID identity env, i.e.
	// relies on the image entrypoint to remap and drop to the launching user.
	passesPUID() bool
	// reachRoute is how a container of this runtime reaches a coordinator on
	// the host, in preference order: the host's LOOPBACK through the
	// runtime's own translator, where its network offers one; the private
	// host-side bridge gateway (rootful); else the host's primary outbound
	// address, flagged Public. Only the last one's listener is reachable
	// beyond this host, and the coordinator's endpoint is token-protected
	// there — allowed, never preferred.
	reachRoute(ctx context.Context) (hostRoute, error)
	// gatewayInspectArgs builds the argv printing the default bridge
	// network's host-side gateway address.
	gatewayInspectArgs() []string
}

// hostRoute is a runtime's answer to reachRoute: the host part a container
// dials, and what the coordinator must listen on for that dial to land.
type hostRoute struct {
	dial   string
	listen present.Listen
}

// ErrNoHostReach refuses a container whose runtime offers no route to the
// host at all: no loopback translator, no bridge gateway, and no default
// route to take the primary address from. Its runner could never dial home.
var ErrNoHostReach = errors.New("isolation: a container of this runtime has no address to reach the coordinator on")

// primaryOutboundIP is the fallback route's address source; a package var so
// tests decide it without the host's routing table.
var primaryOutboundIP = hostnet.PrimaryOutboundIP

// publicRoute is the last preference: the host's primary outbound address,
// listened on and reachable beyond this host. why names the runtime mode's
// missing private route for the one-time warning.
func publicRoute(why string) (hostRoute, error) {
	ip := primaryOutboundIP()
	if ip == "" {
		return hostRoute{}, fmt.Errorf("%w: %s, and the host has no default route to take its primary address from", ErrNoHostReach, why)
	}
	return hostRoute{dial: ip, listen: present.Listen{Addr: ip, Public: true, Why: why}}, nil
}

// bridgeRoute is the rootful preference: the default bridge network's
// host-side gateway, a private address only the host and its containers
// share. Falls back to publicRoute when the runtime reports none that parses.
func bridgeRoute(ctx context.Context, rt Runtime) (hostRoute, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := probeExec(cctx, rt.Binary(), rt.gatewayInspectArgs())
	if ip := strings.TrimSpace(out); err == nil && net.ParseIP(ip) != nil {
		return hostRoute{dial: ip, listen: present.Listen{Addr: ip}}, nil
	}
	return publicRoute(fmt.Sprintf("%s reports no bridge gateway on the host to listen on (%v)", rt.Name(), err))
}

// removeOutcome is what a force-remove established about its container.
type removeOutcome int

const (
	// removeFailed: the runtime did not confirm the container is gone — a
	// possible leak, surfaced by the caller.
	removeFailed removeOutcome = iota
	// removeRemoved: this remove took a container down.
	removeRemoved
	// removeAlreadyGone: there was nothing under the name. Final only once
	// nothing can still create it (see removeLaunched).
	removeAlreadyGone
)

// ContainerInfo is one running container as Enumerate reports it: enough for
// a caller deciding whether to reap it, never more.
type ContainerInfo struct {
	Name   string
	Labels map[string]string
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
//     handle the Docker-Desktop `/host_mnt/c/...` SOURCE form. The
//     linked-worktree case additionally
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

// RunSpec is the runtime-agnostic description of one runner container: which
// image to run, the in-container argv, the identical-path project mount and
// the workspace's mounts, a fresh HOME, and the run's env. A Runtime renders
// it into its own `run` argv.
type RunSpec struct {
	Image   string   // image reference to run
	Name    string   // --name, so teardown can target this exact container
	WorkDir string   // -w and the identical-path project bind-mount target
	Home    string   // fresh $HOME inside the container (engine global state isolated)
	Command []string // in-container argv (the container's ctxloom + its subcommand)
	Env     []string // -e KEY=VAL, or a bare -e NAME forwarded from the run process's env
	Mounts  []Mount  // --mount type=bind bind mounts
	// TTY attaches the run to a terminal (-i -t): the runner's stdio is the
	// tty the originator holds — an INTERACTIVE launch's foreground runner.
	TTY bool

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

// RemoveArgs force-removes the container: SIGKILL, then rm. A racing --rm
// auto-remove leaves nothing under the name, which removeOutcome reads as
// removeAlreadyGone. A runtime whose `rm -f` would first wait out a stop
// timeout overrides this to skip that wait, so the SIGKILL claim holds for
// every runtime.
func (ociRuntime) RemoveArgs(name string) []string { return []string{"rm", "-f", name} }

// removeOutcome reads a remove's result: success with the name echoed on
// stdout removed a container; success with EMPTY stdout found none (current
// CLIs exit 0 on a missing name and say so, if at all, only on stderr); any
// failure is unconfirmed. A runtime whose CLI reports gone-ness through a
// failure overrides this.
func (ociRuntime) removeOutcome(stdout []byte, err error) removeOutcome {
	switch {
	case err != nil:
		return removeFailed
	case len(bytes.TrimSpace(stdout)) == 0:
		return removeAlreadyGone
	default:
		return removeRemoved
	}
}

// inspectRunningArgs is the docker-CLI-compatible running-state inspect.
func (ociRuntime) inspectRunningArgs(name string) []string {
	return []string{"container", "inspect", "-f", "{{.State.Running}}", name}
}

// imageInspectArgs is the docker-CLI-compatible image inspect.
func (ociRuntime) imageInspectArgs(image, format string) []string {
	args := []string{"image", "inspect", image}
	if format != "" {
		args = append(args, "--format", format)
	}
	return args
}

// daemonNameTemplate is the top-level {{.Name}} field.
func (ociRuntime) daemonNameTemplate() string { return "{{.Name}}" }

// gatewayInspectArgs reads the default `bridge` network's IPAM gateway.
func (ociRuntime) gatewayInspectArgs() []string {
	return []string{"network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"}
}

// passesPUID is true: every mode relies on the image entrypoint to remap, save
// the one that overrides it. An unrecognised mode stays on the conservative
// side, where a run-as-is image must carry the entrypoint.
func (ociRuntime) passesPUID() bool { return true }

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

// enumerate is the shared Docker/Podman Enumerate body: `<binary> ps --filter
// name=<namePrefix> --format {{.Names}}\t{{json .Labels}}`, one line per
// RUNNING container whose name contains namePrefix (docker/podman's `name`
// filter is substring, not anchored — ReapOrphanedContainers re-checks the
// prefix itself rather than trusting this as an exact filter). Routed through
// probeExec (package var) so it is testable without a runtime present, same
// seam sharedFSProbe and diagnoseAdvisory already use.
func (ociRuntime) enumerate(ctx context.Context, binary, namePrefix string) ([]ContainerInfo, error) {
	out, err := probeExec(ctx, binary, []string{
		"ps",
		"--filter", "name=" + namePrefix,
		"--format", "{{.Names}}\t{{json .Labels}}",
	})
	if err != nil {
		return nil, fmt.Errorf("%s ps: %w", binary, err)
	}
	var infos []ContainerInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		name, labelsJSON, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var labels map[string]string
		// A container with no labels renders `{{json .Labels}}` as "null" or
		// "{}" depending on engine/version; either unmarshals cleanly into a
		// nil/empty map, which is exactly the "no label" state a caller
		// checking Labels[key] already handles correctly.
		_ = json.Unmarshal([]byte(labelsJSON), &labels)
		infos = append(infos, ContainerInfo{Name: name, Labels: labels})
	}
	return infos, nil
}

// identityEnvArgs renders the PUID/PGID env that tells the agent image's
// entrypoint to remap its generic ctxloom user to the launching uid/gid and
// drop privileges to it.
// CTXLOOM_ALLOW_ROOT IS DELIBERATELY NEVER PASSED. It used to be appended here
// under strictness.Degraded(), and that was a security bypass wearing a
// convenience flag: the agent image's entrypoint REFUSES to run the engine as
// root when it cannot become the PUID identity (no usable gosu/setpriv), and
// this handed it the escape hatch that downgrades its own refusal to
// warn-and-run-as-root — inside a container with the user's project bind-
// mounted, so everything the run touched came back root-owned on the host.
//
// --degraded means "I accept a thinner run", never "I accept running as root".
// There is no caller for whom the right answer is root, so there is no flag:
// an image that cannot drop privileges is a broken image, and the fix is to
// repair or replace it (see runAsIs/overrideIdentityRemedy). Do not reintroduce
// this; TestDegradedNeverBypassesIsolation asserts the argv never carries it.
func identityEnvArgs() []string {
	return []string{
		"-e", fmt.Sprintf("PUID=%d", os.Getuid()),
		"-e", fmt.Sprintf("PGID=%d", os.Getgid()),
	}
}

// renderRunSpec renders the runtime-agnostic tail of a run argv (env, mounts,
// workdir, image, in-container command) shared by Docker and Podman. The
// runtime-specific head (--rm/--name/--user) is prepended by each RunArgs.
func renderRunSpec(spec RunSpec) []string {
	var args []string
	if spec.TTY {
		args = append(args, "-i", "-t")
	}
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
	// the docker/podman `-e` grammar: "KEY=VAL" sets an explicit value
	// (IS_SANDBOX, TERM), while a BARE "KEY" (no '=') is a
	// name-only passthrough — the runtime forwards the VALUE from its own inherited
	// environment. Auth SECRETS use the bare-name form (containerAuth.envPassthrough)
	// so the value never lands in this long-lived `run` argv, which is world-readable
	// via /proc/<pid>/cmdline; it stays in the launcher's env only.
	for _, e := range spec.Env {
		args = append(args, "-e", e)
	}
	args = append(args, mountArgs(runMounts(spec))...)
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

// runMounts is the spec's mounts, plus — PROBE-ONLY — the trace dir bound
// OUT so the strace output written from inside survives the container's
// `--rm` teardown (no docker cp race). A separate slice so the spec's own
// Mounts are never mutated.
func runMounts(spec RunSpec) []Mount {
	if spec.Trace == nil {
		return spec.Mounts
	}
	return append(append([]Mount(nil), spec.Mounts...),
		Mount{Host: spec.Trace.HostDir, Container: spec.Trace.ContainerDir})
}

// mountArgs renders each mount as a --mount flag.
//
// --mount (not -v host:container[:ro]): the colon-delimited -v grammar is
// ambiguous on Windows, where a host path carries a drive-letter colon
// (C:\...) that mis-splits. --mount type=bind,source=,target=[,readonly] is
// colon-free and renders identically on docker + podman + Linux. Every mount
// in this package funnels through here, so this is the single site. (--mount
// requires the source to already exist; every Mount.Host in this package is
// a path we created or verified before the run, so that holds.)
func mountArgs(mounts []Mount) []string {
	var args []string
	for _, m := range mounts {
		opt := "type=bind,source=" + m.Host + ",target=" + m.Container
		if m.ReadOnly {
			opt += ",readonly"
		}
		args = append(args, "--mount", opt)
	}
	return args
}

// runtimeReachable reports whether a container runtime CLI is on PATH and its
// daemon answers `<bin> info`. Any failure (missing binary, daemon down) →
// false → the runtime is not selected, and a requested container becomes a
// non-degradable fatal finding (ClassIsolation) the choke owner aborts on.
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
// the mock backend records the same answer as evidence of where an
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
		{Docker{}.Name(), func() (Runtime, RuntimeAxis) {
			d := newDockerRuntime(runtimeReachable)
			return d, ownershipAxis(d.rootless)
		}},
		{Podman{}.Name(), func() (Runtime, RuntimeAxis) {
			p := newPodmanRuntime()
			return p, ownershipAxis(p.rootless)
		}},
	}
}

// selectRuntimeWhere walks the candidates — config preference first, then
// detection order — and returns the first that is launchable AND accepted by
// ok, or Host{} when none is. It never errors: a runtime that cannot serve is
// simply not selected, and the caller decides the consequence (chainFor makes
// an EXPLICITLY-requested container that lands on Host a non-degradable fatal
// ClassIsolation finding). SelectRuntime and ProbeRuntime differ ONLY in ok.
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
// values exist to prevent, and it stays forbidden under --degraded: chainFor
// refuses the run instead, naming the other mode as an explicit selection.
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
