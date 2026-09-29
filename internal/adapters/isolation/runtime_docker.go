package isolation

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/platform"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
// Self first: when the daemon has confirmed this process is one of its
// containers, the runner joins that container's network (selfNetworkRoute),
// ahead of every host route — none of which this process's namespace holds.
func (d Docker) reachRoute(ctx context.Context) (hostRoute, error) {
	if d.self != nil {
		if r, ok, err := selfNetworkRoute(*d.self); err != nil || ok {
			return r, err
		}
	}
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
		strictness.Fail(report.KindIsolation,
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
	d := Docker{rootless: dockerIsRootless()}
	d.self = resolveSelf(d)
	return d
}
