package isolation

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// pathMapper names, inside the container's mount namespace, where the SAME
// resource a host path names is mounted. It decides the mount TARGET only:
// the bind SOURCE is always the host path as this process sees it, and the
// runtime translates that itself (Docker Desktop and podman machine both
// accept a native `C:\...` source). Identity is one configuration of the
// seam, not an assumption built into its callers.
//
// Which mapper applies depends on the HOST OS alone, so it is chosen at
// compile time (hostMapper, in the build-constrained twins) and never by
// runtime name.
//
// The rule every mount site follows: the container side of a HOST-anchored
// path is toContainer(hostPath); the container side of a CONTAINER-anchored
// path (under the fixed instance home or $HOME) is path.Join over a POSIX
// root. filepath never builds a container path — on Windows it would emit
// backslashes the daemon rejects.
//
// Not this rule: docker-outside-of-docker, where this process's paths are
// not the daemon's. That rewrites the SOURCE (the daemon's name for our
// path) — sourceMapper, the other rule pathSeam carries; overloading
// toContainer would conflate the two.
type pathMapper interface {
	// toContainer maps a host path to the in-container path the SAME resource
	// is mounted/reached at. It fails for a host path the runtime cannot
	// mount at all; the container relocator reports that as
	// present.ErrUnreachableRoot naming the root.
	toContainer(hostPath string) (string, error)
}

// identityMapper is Host==Container: a POSIX host, whose runtime shares its
// path namespace natively or through a VM that shares it at the same paths.
type identityMapper struct{}

func (identityMapper) toContainer(hostPath string) (string, error) { return hostPath, nil }

var (
	// errUNCPath refuses a share or device path (\\server\share, \\wsl$\...,
	// \\wsl.localhost\..., \\.\..., \\?\UNC\...): no container runtime on
	// Windows is known to bind one, and podman's own conversion rejects them.
	// It is raised carrying uncPathRemedy.
	errUNCPath = errors.New("isolation: a UNC, WSL-share or device path has no route into a Linux container")
	// errNotDriveAbsolute refuses anything but <letter>:\... — layout paths
	// are absolute, so this is a guard, not a guess.
	errNotDriveAbsolute = errors.New("isolation: not a drive-absolute Windows path")
)

// uncPathRemedy is the fix for a share-path root. The usual one is a project
// kept inside a WSL distro, which the Linux build run in that distro reaches
// natively with no mapping at all.
const uncPathRemedy = "run ctxloom's Linux build inside the WSL distro that holds the project (or move the project onto a local drive), or run with `runtime: host`"

// driveMountRoot is where driveLetterMapper places each drive: the WSL and
// podman-machine convention (C:\ is /mnt/c inside the VM), so a path reads
// the same in the container as it does in the VM that runs it.
const driveMountRoot = "/mnt"

// driveLetterMapper names a Windows host path in the Linux container:
// C:\Users\ben\proj is /mnt/c/Users/ben/proj. The drive letter is lowercased
// (drive letters are case-insensitive, and the VM names the mount that way);
// the rest keeps its case byte-for-byte, because NTFS preserves case and a
// folded name would rename directories the engine prints and records. A
// `\\?\` long-path prefix is stripped. Pure string work — no filepath — so
// its behaviour is the same, and tested, on every OS.
type driveLetterMapper struct{}

func (driveLetterMapper) toContainer(hostPath string) (string, error) {
	p := strings.ReplaceAll(hostPath, `\`, "/")
	if rest, ok := strings.CutPrefix(p, "//?/"); ok && !strings.HasPrefix(strings.ToUpper(rest), "UNC/") {
		p = rest
	}
	if strings.HasPrefix(p, "//") {
		return "", report.Errorf(uncPathRemedy, "%w: %s", errUNCPath, hostPath)
	}
	if len(p) < 3 || !isASCIILetter(p[0]) || p[1] != ':' || p[2] != '/' {
		return "", fmt.Errorf("%w: %q", errNotDriveAbsolute, hostPath)
	}
	// Cleaned as a ROOTED path first, so `..` can never climb out of the drive.
	return path.Join(driveMountRoot, string(p[0]|0x20), path.Clean(p[2:])), nil
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
