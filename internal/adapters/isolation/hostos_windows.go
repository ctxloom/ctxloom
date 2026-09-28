//go:build windows

package isolation

// hostMapper maps a Windows path by its drive letter: Docker Desktop and
// podman machine both take the native C:\... bind source and translate it
// themselves; the target is a Linux path we choose.
func hostMapper() pathMapper { return driveLetterMapper{} }

// runIdentity is the image's own ctxloom user. Windows has no POSIX uid
// (os.Getuid is -1, which the entrypoint cannot remap to), and none is
// needed: Docker Desktop's and WSL's drive sharing do not carry a container
// uid onto NTFS — what the run writes lands owned by the Windows user either
// way. The value still matters: without a PUID a rootful daemon (Docker
// Desktop's is) would run the engine as root, and the entrypoint's drop to
// the baked uid is a no-op remap.
func runIdentity() (uid, gid int) { return imageUserID, imageUserID }

// podmanMachineRoute: the default Windows machine runs under WSL, where
// host.containers.internal names the machine VM, not this host, and a
// connection to a Windows-host listener times out (podman issues #14933 and
// #25152). This host's own primary address is the route the reports show
// working, so it is taken explicitly — public, and warned — rather than
// dialling an alias that lands in the VM.
func podmanMachineRoute() (hostRoute, error) {
	return publicRoute("podman machine on Windows (WSL) routes host.containers.internal to the machine VM, not this host; the coordinator listens on this host's primary address instead — allow it through Windows Firewall if the runner cannot connect, or use Docker Desktop")
}
