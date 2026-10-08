//go:build !docker_integration && !windows

package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnitSuite_NeverExecsAContainerRuntime pins installHermeticRuntimes: with
// logging docker/podman shims first on PATH, every way a unit test reaches a
// runtime — a launch survey (what chainFor/prepareChain consult), SelectRuntime
// and ProbeRuntime — answers Host{} and runs neither shim.
func TestUnitSuite_NeverExecsAContainerRuntime(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "exec.log")
	for _, bin := range []string{"docker", "podman"} {
		shim := "#!/bin/sh\necho \"" + bin + " $*\" >> '" + log + "'\nexit 1\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, bin), []byte(shim), 0o755))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	survey := surveyRuntimes()
	for _, want := range []RuntimeAxis{RuntimeContainerRootless, RuntimeContainerRootful} {
		assert.IsType(t, Host{}, survey(want), "survey %s", want)
		assert.IsType(t, Host{}, SelectRuntime("", want), "select %s", want)
		assert.IsType(t, Host{}, SelectRuntime("docker", want), "select docker %s", want)
	}
	assert.IsType(t, Host{}, ProbeRuntime(""))

	_, err := os.Stat(log)
	assert.True(t, os.IsNotExist(err), "a unit test exec'd a container runtime")
}
