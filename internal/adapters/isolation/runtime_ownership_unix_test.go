//go:build !windows

package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelectRuntime_SlowEngineIsStillSelected forces the interleaving a loaded
// host produces: the engine is live and answers every probe correctly, just
// slowly. Selection gates a run FATALLY — a container request that lands on
// Host{} is refused outright — so a probe that reads "slow" as "absent" refuses
// a run the engine would have served. The shim is the only thing on PATH, so
// the answer cannot come from a real runtime, and the delay is chosen to be
// well past what a loaded host has been measured taking yet far below a hang.
func TestSelectRuntime_SlowEngineIsStillSelected(t *testing.T) {
	dir := t.TempDir()
	shim := "#!/bin/sh\n" +
		"case \"$1\" in info) /bin/sleep 6 ;; *) exit 0 ;; esac\n" +
		"case \"$*\" in *--format*) echo 'true pasta' ;; esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "podman"), []byte(shim), 0o755))
	t.Setenv("PATH", dir)

	rt := SelectRuntime("", RuntimeContainerRootless)
	assert.IsType(t, Podman{}, rt,
		"a live rootless podman that answers `info` slowly must be selected, not reported absent")
}
