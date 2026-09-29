package isolation

import (
	"context"
	"fmt"
	"os/exec"
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
func (p Podman) RunArgs(spec RunSpec) []string {
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
func (p Podman) reachRoute(ctx context.Context) (hostRoute, error) {
	switch {
	case platform.ContainersInVM && machineVMIsWSL:
		// Under WSL host.containers.internal lands in the machine VM, and a
		// connection to a listener on this host times out (podman issues
		// #14933, #25152). This host's own primary address is the route those
		// reports show working, taken explicitly — public, and warned.
		return publicRoute(podmanWSLReach)
	case platform.ContainersInVM:
		return hostRoute{dial: "host.containers.internal"}, nil
	case p.rootless && p.rootlessNet == "pasta":
		return hostRoute{dial: pastaHostLoopback, network: "pasta:--map-host-loopback," + pastaHostLoopback}, nil
	case p.rootless && p.rootlessNet == "slirp4netns":
		return hostRoute{dial: slirpHostLoopback, network: "slirp4netns:allow_host_loopback=true"}, nil
	case p.rootless:
		return publicRoute(fmt.Sprintf("rootless podman's network %q has no known route to the host's loopback", p.rootlessNet))
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

// newPodmanRuntime probes the engine once (`podman info`) for whether it is
// rootless and which rootless network it uses. Best-effort: on any error it
// assumes rootless with an unknown network — podman is rootless by default,
// and keep-id under a rootful engine errors loudly at launch while a missing
// keep-id under rootless silently wrecks bind-mount ownership, the worse
// failure.
func newPodmanRuntime() Podman {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.Security.Rootless}} {{.Host.RootlessNetworkCmd}}").Output()
	if err != nil {
		return Podman{rootless: true}
	}
	rootless, network, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return Podman{rootless: rootless != "false", rootlessNet: network}
}
