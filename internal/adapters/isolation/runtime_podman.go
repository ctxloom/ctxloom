package isolation

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/platform"
)

// Podman launches containers via the podman CLI. podman's run/rm argv is
// docker-CLI-compatible. rootless records whether the engine is rootless:
// rootless podman needs --userns=keep-id so the launching uid maps to ITSELF
// in-container instead of a subuid — then the entrypoint's PUID/PGID remap
// yields a run that is genuinely non-root in-container AND correctly owned on
// the host, something rootless docker cannot express. Rootful podman behaves
// like rootful docker (identity mapping; entrypoint remap only).
//
// rootlessNet is the rootless network command podman reports ("pasta",
// "slirp4netns"; "" when unprobed), which decides how a container reaches the
// host and the network its runner sits on (reachRoute).
type Podman struct {
	ociRuntime
	rootless    bool
	rootlessNet string
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
func (p Podman) RunArgs(spec RunSpec) ([]string, error) {
	args := []string{"run", "--rm", "--name", spec.Name}
	args = append(args, initArgs()...)
	args = append(args, ownerLabelArgs()...)
	if p.rootless {
		// keep-id's DEFAULT user is the host uid (not root), which couldn't
		// remap; enter as namespaced root so the entrypoint can usermod+drop.
		args = append(args, "--userns=keep-id", "--user", "0:0")
	}
	args = append(args, identityEnvArgs()...)
	return p.runArgs(args, spec)
}

// RemoveArgs adds `-t 0`: podman's `rm -f` otherwise sends the stop signal and
// waits its 10s stop timeout before the SIGKILL, so every teardown of a live
// runner would stall that long.
func (Podman) RemoveArgs(name string) []string { return []string{"rm", "-f", "-t", "0", name} }

// daemonNameTemplate: `podman info` has no top-level Name (that template is an
// execution error) and carries the host name under Host.
// canonicalRef strips the localhost/ registry podman files an unqualified
// local build under, so `localhost/ctxloom-agent-x:t` reads as the tag built.
func (Podman) canonicalRef(ref string) string { return strings.TrimPrefix(ref, "localhost/") }

func (Podman) daemonNameTemplate() string { return "{{.Host.Hostname}}" }

// podmanWSLReach is why a WSL podman machine's run takes the public route,
// and what to do if that route is blocked.
const podmanWSLReach = "podman machine on WSL routes host.containers.internal to the machine VM, not this host; the coordinator listens on this host's primary address instead — allow it through Windows Firewall if the runner cannot connect, or use Docker Desktop"

// pastaHostLoopback is the in-container address pasta maps to the host's
// loopback for our runners. Beside podman's own pasta addresses (169.254.1.1
// DNS, 169.254.1.2 host.containers.internal), which do not reach loopback.
const pastaHostLoopback = "169.254.1.3"

// slirpHostLoopback is slirp4netns's gateway, which reaches the host's
// loopback once allow_host_loopback is set.
const slirpHostLoopback = "10.0.2.2"

// reachRoute: host.containers.internal in a podman machine, except under WSL
// (machineVMIsWSL); a rootless translator's loopback route; rootful podman's
// netavark bridge gateway; else the public fallback.
//
// A translator route keeps the user's rootless translator and opens its route
// to the host's loopback, so the coordinator needs no listener beyond
// loopback. Measured on podman 5.4.2: without these options neither
// translator reaches the host's 127.0.0.1, and pasta's
// host.containers.internal lands on a LAN address.
//
// Those are the HOST routes; the self route precedes them (selfFirst). A
// rootless podman self with no network to join (its container on
// pasta/slirp4netns) shares its network namespace instead — the owner's
// ruling, warned; every other runtime refuses that case.
func (p Podman) reachRoute(ctx context.Context) (hostRoute, error) {
	return selfFirst(p.self, p.rootless, func() (hostRoute, error) { return p.hostReach(ctx) })
}

// hostReach is Podman's route to a coordinator on the daemon's host.
//
// Both public routes here leave their listener open to every peer: under WSL
// the connection arrives from the machine VM's NAT address, and for a rootless
// network with no known translator there is no measured source to hold the
// filter to.
func (p Podman) hostReach(ctx context.Context) (hostRoute, error) {
	switch {
	case platform.ContainersInVM && machineVMIsWSL:
		// Under WSL host.containers.internal lands in the machine VM, and a
		// connection to a listener on this host times out (podman issues
		// #14933, #25152). This host's own primary address is the route those
		// reports show working, taken explicitly — public, and warned.
		return publicRoute(podmanWSLReach, false)
	case platform.ContainersInVM:
		return hostRoute{dial: "host.containers.internal"}, nil
	case p.rootless && p.rootlessNet == "pasta":
		return hostRoute{dial: pastaHostLoopback, network: "pasta:--map-host-loopback," + pastaHostLoopback}, nil
	case p.rootless && p.rootlessNet == "slirp4netns":
		return hostRoute{dial: slirpHostLoopback, network: "slirp4netns:allow_host_loopback=true"}, nil
	case p.rootless:
		return publicRoute(fmt.Sprintf("rootless podman's network %q has no known route to the host's loopback", p.rootlessNet), false)
	default:
		return bridgeRoute(ctx, p)
	}
}

// gatewayInspectArgs reads the default `podman` network's subnet gateway.
func (Podman) gatewayInspectArgs() []string {
	return []string{"network", "inspect", "podman", "--format", "{{range .Subnets}}{{.Gateway}}{{end}}"}
}

// Enumerate lists RUNNING podman containers by name prefix, via the shared
// ociRuntime.enumerate.
func (p Podman) Enumerate(ctx context.Context, namePrefix string) ([]ContainerInfo, error) {
	return p.enumerate(ctx, p.Binary(), namePrefix)
}

// podmanOwnershipFormat is the `podman info` template answering whether the
// engine is rootless and, in the same call, its rootless network command and
// what decides the resource caps it can enforce (podmanLimitSupport). Every
// field renders as one word, save the controller list, which is last.
const podmanOwnershipFormat = "{{.Host.Security.Rootless}} {{.Host.RootlessNetworkCmd}} {{.Host.MemTotal}} {{.Host.CgroupsVersion}} {{.Host.CgroupManager}} {{.Host.CgroupControllers}}"

// podmanRootless reads podmanOwnershipFormat's first field: "true" or
// "false", and anything else is unreadable.
func podmanRootless(answer string) (bool, error) {
	field, _, _ := strings.Cut(strings.TrimSpace(answer), " ")
	r, err := strconv.ParseBool(field)
	if err != nil {
		return false, fmt.Errorf("unreadable rootless answer %q", field)
	}
	return r, nil
}

// podmanRootlessNet is the answer's second field, the rootless network
// command ("" when podman reports none).
func podmanRootlessNet(answer string) string {
	f := strings.SplitN(strings.TrimSpace(answer), " ", 3)
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

// oomEventArgs is nil: podman logs no OOM event (its log has the container's
// died/remove, never an oom), so an OOM kill cannot be confirmed after --rm.
func (Podman) oomEventArgs(string, time.Time, time.Time) []string { return nil }

// newPodmanRuntime probes podman for selection exactly as newDockerRuntime
// probes docker: reachability, then — only for a reachable engine — its
// ownership through the shared probeOwnership, whose one call also names the
// rootless network (reachRoute).
func newPodmanRuntime(reachable func(string) bool) (Podman, RuntimeAxis) {
	if !reachable("podman") {
		return Podman{}, ownershipUndecided
	}
	owns, answer := probeOwnership("podman", podmanOwnershipFormat, podmanRootless)
	rootless := owns == RuntimeContainerRootless
	p := Podman{rootless: rootless, rootlessNet: podmanRootlessNet(answer)}
	p.reachable = true
	p.limits = podmanLimitSupport(answer, rootless)
	p.self, p.selfErr = resolveSelf(p)
	return p, owns
}

const (
	// podmanRootlessV1 is why rootless podman on cgroup v1 enforces no cap:
	// the user cannot be delegated a v1 hierarchy, and podman refuses a run
	// given one ("cannot specify resource limits without cgroups v2").
	podmanRootlessV1 = "rootless podman cannot enforce resource limits on cgroup v1"
	// podmanRootlessNoSystemd is the same refusal under cgroup v2 when podman
	// manages cgroups itself (cgroupfs) rather than through systemd, which is
	// what delegates the user a controllable subtree.
	podmanRootlessNoSystemd = "rootless podman enforces resource limits only with the systemd cgroup manager"
)

// podmanLimitSupport reads the fields podmanOwnershipFormat appends after
// rootless and the network command: MemTotal, CgroupsVersion, CgroupManager,
// then CgroupControllers (the controllers delegated to this user, last
// because it renders with spaces). Rootful podman enforces both caps on
// either cgroup version; rootless needs v2 under systemd, and each cap its
// controller delegated.
func podmanLimitSupport(answer string, rootless bool) limitSupport {
	f := strings.SplitN(strings.TrimSpace(answer), " ", 6)
	if len(f) < 5 {
		return limitSupport{}
	}
	mem, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return limitSupport{}
	}
	sup := limitSupport{hostMemory: mem}
	switch {
	case !rootless:
		sup.memory, sup.pids = true, true
	case f[3] != "v2":
		sup.unenforced = podmanRootlessV1
	case f[4] != "systemd":
		sup.unenforced = podmanRootlessNoSystemd
	default:
		sup.memory, sup.pids = delegatedControllers(f)
		if !sup.memory && !sup.pids {
			sup.unenforced = "rootless podman has neither the memory nor the pids cgroup controller delegated"
		}
	}
	sup.swap = sup.memory
	return sup
}

// delegatedControllers reports whether the memory and pids controllers are
// among the CgroupControllers podmanLimitSupport's split answer ends with
// (f[5], rendered as "[a b c]"; absent when none are delegated).
func delegatedControllers(f []string) (memory, pids bool) {
	if len(f) < 6 {
		return false, false
	}
	for _, c := range strings.Fields(strings.Trim(f[5], "[]")) {
		switch c {
		case "memory":
			memory = true
		case "pids":
			pids = true
		}
	}
	return memory, pids
}
