package isolation

import (
	"context"
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
// host (ContainerHostAlias).
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
func (Podman) daemonNameTemplate() string { return "{{.Host.Hostname}}" }

// ContainerHostAlias is podman's host.containers.internal wherever an address
// of the host's own does not reach it. Inside a VM (Podman Machine) it lands on
// the host's loopback. Under rootless pasta the container carries a copy of the
// host's primary address, so dialing that address stays inside the container,
// while host.containers.internal is mapped by pasta to the host's primary
// address itself. Rootful (bridge) and slirp4netns containers reach the host at
// its own addresses, and under slirp4netns podman resolves the alias to an
// arbitrary host interface that nothing need be listening on — so there is no
// alias there.
func (p Podman) ContainerHostAlias() string {
	if platform.ContainersInVM || (p.rootless && p.rootlessNet == "pasta") {
		return "host.containers.internal"
	}
	return ""
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
