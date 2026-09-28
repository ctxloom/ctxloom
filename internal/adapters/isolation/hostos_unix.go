//go:build !windows

package isolation

import "os"

// hostMapper is identity on a POSIX host: Linux shares the kernel's paths,
// and Docker Desktop and podman machine on macOS share the user's paths into
// their VM at the same names. A root outside those shares is caught by the
// shared-fs probe, not guessed here.
func hostMapper() pathMapper { return identityMapper{} }

// runIdentity is the uid:gid the image entrypoint drops to: the launching
// user, so everything the run writes through a bind mount lands owned by it.
func runIdentity() (uid, gid int) { return os.Getuid(), os.Getgid() }

// podmanMachineRoute is how a podman machine's container reaches this host.
// Off Windows the machine's network is gvproxy (user-mode networking is
// always on outside WSL, podman-machine-init(1)), whose
// host.containers.internal lands on this host's loopback.
func podmanMachineRoute() (hostRoute, error) {
	return hostRoute{dial: "host.containers.internal"}, nil
}
