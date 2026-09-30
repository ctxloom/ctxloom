package isolation

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/platform"
)

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
func (d Docker) RunArgs(spec RunSpec) ([]string, error) {
	args := []string{"run", "--rm", "--name", spec.Name}
	args = append(args, initArgs()...)
	args = append(args, ownerLabelArgs()...)
	if d.passesPUID() {
		args = append(args, identityEnvArgs()...)
	}
	return d.runArgs(args, spec)
}

// passesPUID is false under a rootless daemon: container-ROOT is the one uid
// that maps to the launching host user, so the run stays root and the image
// must run as root. A rootful daemon has the entrypoint remap and drop.
func (d Docker) passesPUID() bool { return !d.rootless }

// removeOutcome adds docker's failure-shaped gone-ness to the shared reading:
// older daemons exit non-zero with "No such container" once --rm has already
// finished, and any daemon answers "removal of container ... is already in
// progress" while its OWN --rm cleanup is mid-flight (a long-lived container
// that exits when its stdin closes reproduces it). Both are teardown success:
// the container is gone, or docker's in-flight removal guarantees it shortly.
func (d Docker) removeOutcome(stdout []byte, err error) removeOutcome {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr := strings.ToLower(string(ee.Stderr))
		if strings.Contains(stderr, "no such container") ||
			(strings.Contains(stderr, "removal of container") && strings.Contains(stderr, "already in progress")) {
			return removeAlreadyGone
		}
	}
	return d.ociRuntime.removeOutcome(stdout, err)
}

// reachRoute: Docker Desktop's host.docker.internal lands on the host's
// loopback. Rootless docker offers no loopback route — dockerd-rootless.sh
// starts RootlessKit with --disable-host-loopback — and its bridge lives in
// RootlessKit's namespace, not on the host, so only the public fallback
// reaches it. Rootful docker's docker0 gateway is on the host.
//
// Those are the HOST routes; the self route precedes them (selfFirst), and a
// self with no joinable network is refused.
func (d Docker) reachRoute(ctx context.Context) (hostRoute, error) {
	return selfFirst(d.self, false, func() (hostRoute, error) { return d.hostReach(ctx) })
}

// hostReach is Docker's route to a coordinator on the daemon's host.
func (d Docker) hostReach(ctx context.Context) (hostRoute, error) {
	switch {
	case platform.ContainersInVM:
		return hostRoute{dial: "host.docker.internal"}, nil
	case d.rootless:
		return publicRoute("rootless docker's RootlessKit disables the containers' route to the host's loopback, and its bridge is not on the host")
	default:
		return bridgeRoute(ctx, d)
	}
}

// Enumerate lists RUNNING docker containers by name prefix, via the shared
// ociRuntime.enumerate.
func (d Docker) Enumerate(ctx context.Context, namePrefix string) ([]ContainerInfo, error) {
	return d.enumerate(ctx, d.Binary(), namePrefix)
}

// dockerOwnershipFormat is the `docker info` template whose answer names
// "rootless" among the daemon's security options when the daemon is rootless.
const dockerOwnershipFormat = "{{.SecurityOptions}}"

// dockerRootless reads dockerOwnershipFormat's answer. Every answer is
// readable: the list names rootless or it does not.
func dockerRootless(answer string) (bool, error) { return strings.Contains(answer, "rootless"), nil }

// newDockerRuntime probes docker for selection: reachability, then — only
// for a reachable daemon — its ownership through the shared probeOwnership.
// An unreachable docker is never selected, so asking it more would only
// manufacture a spurious ownership finding on docker-less or daemon-down
// hosts where podman serves the run. It returns the CONCRETE Docker so the
// ownership its argv is built with (rootless) and the ownership selection
// filters on are the same probe's answer, never re-derived downstream.
func newDockerRuntime(reachable func(string) bool) (Docker, RuntimeAxis) {
	if !reachable("docker") {
		return Docker{}, ownershipUndecided
	}
	owns, _ := probeOwnership("docker", dockerOwnershipFormat, dockerRootless)
	d := Docker{rootless: owns == RuntimeContainerRootless}
	d.reachable = true
	d.self = resolveSelf(d)
	return d, owns
}
