//go:build integration || acceptance

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
)

// RootlessDockerStubScript answers exactly the probes that decide whether a
// `container-rootless` runtime is SELECTED:
//
//	`info`          -> 0 AND prints SecurityOptions containing "rootless", so
//	                   isolation.runtimeReachable reports the daemon up and
//	                   isolation.dockerIsRootless resolves the ownership a
//	                   container-rootless agent asks for.
//	`ps`            -> 0 with no ids: isolation.findSelf's id-filtered listing
//	                   answered as a daemon that owns no container of this
//	                   process's. Without it, a test run inside a container
//	                   (CI's job container) has a self-id candidate, and a
//	                   failing listing is an undecidable self — a warning on
//	                   the stderr a test may assert on.
//	`image inspect` -> 1, so isolation.Container.imagePresent reports absent.
//
// Everything else fails, because nothing else should be reached by a caller
// that only needs the runtime to be SELECTABLE: a stub that cheerfully
// answered `run` would let a broken launch gate look healthy.
const RootlessDockerStubScript = `#!/bin/sh
case "$1" in
  info) echo '[name=seccomp,profile=builtin name=rootless name=cgroupns]'; exit 0 ;;
  ps) exit 0 ;;
  *) exit 1 ;;
esac
`

// unreachableRuntimeStubScript is a runtime whose daemon never answers.
const unreachableRuntimeStubScript = "#!/bin/sh\nexit 1\n"

// StubRootlessContainerRuntime makes "a rootless container runtime is
// reachable" a fact of THIS environment rather than of the machine: a stub
// docker reporting a reachable rootless daemon and a stub podman reporting none
// are put AHEAD of the inherited PATH for this process and every ctxloom it
// spawns (restored by Cleanup).
//
// Why a stub: runtime selection filters by ownership, so a test that declares
// `runtime: container-rootless` passes on a developer's rootless docker and
// exits 3 on a rootful-only host (GitHub-hosted runners are rootful, and CI's
// devcontainer job sees that daemon through a mounted socket). Both are the
// product answering correctly; only the test's world differed.
//
// Prepended, not rebuilt from scratch: the caller still needs git and the rest
// of the inherited PATH, and shadowing BOTH runtime names is what makes the
// outcome independent of which real runtimes the machine has.
func (e *TestEnvironment) StubRootlessContainerRuntime() error {
	binDir := filepath.Join(e.Root, "stub-container-runtime-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create stub runtime bin dir: %w", err)
	}
	for name, script := range map[string]string{
		"docker": RootlessDockerStubScript,
		"podman": unreachableRuntimeStubScript,
	} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // must be executable
			return fmt.Errorf("write stub %s: %w", name, err)
		}
	}
	e.storeAndSetEnv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return nil
}
