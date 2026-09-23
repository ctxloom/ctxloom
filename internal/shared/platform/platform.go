// Package platform holds the facts about the host OS that code branches on.
// Each fact's value lives in a per-OS file the compiler selects, so no caller
// reads runtime.GOOS and no OS-specific arm is compiled where it cannot run.
package platform

// ContainersInVM reports that containers run inside a VM (Docker Desktop, a
// podman machine) rather than on this kernel, so a container reaches the host
// through the runtime's forwarding hostname, not a host address.
const ContainersInVM = containersInVM

// KeychainCredentials reports that a subscription login keeps its OAuth token
// in the macOS Keychain rather than in a credentials file.
const KeychainCredentials = keychainCredentials

// LoginShell reports that the user has a POSIX login shell whose rc files
// resolve PATH, so running it is how that PATH is recovered.
const LoginShell = loginShell

// LinuxHost reports that the host kernel is Linux: a container shares it, so
// the running binary can serve as the in-container one.
const LinuxHost = linuxHost

// TempBase is the parent directory for a container run's host-side scratch
// tree. Empty means os.TempDir.
const TempBase = tempBase
