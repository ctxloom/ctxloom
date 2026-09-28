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

// machineVMIsWSL is true: a runtime VM here is a WSL distro on WSL's own
// NAT network by default (podman-machine-init(1): user-mode networking is
// off unless asked for), where a VM's host alias can name the VM itself
// rather than this host. A runtime that ships its own host proxy is
// unaffected; one that relies on the VM's network reads this.
const machineVMIsWSL = true
