package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHomeLocksDir_RefusesAnUnsandboxedHomeUnderATestBinary is the checked
// guard for the home lock directory. Every foreign-file lock (HomePathFor)
// resolves through HomeLocksDir, so a test package that drives a locked write
// without the sandbox would otherwise drop a durable lock file into the
// developer's real ~/.ctxloom/locks on every run. Refusing here turns that
// omission into a red test in whichever package made it.
func TestHomeLocksDir_RefusesAnUnsandboxedHomeUnderATestBinary(t *testing.T) {
	t.Setenv("HOME", "/definitely-not-a-temp-root/home/dev")

	_, err := HomeLocksDir()
	require.Error(t, err, "a lock directory in a real home must be refused from a test binary")
	assert.Contains(t, err.Error(), "REFUSING")

	_, err = HomePathFor(filepath.Join(t.TempDir(), "settings.json"))
	require.Error(t, err, "every foreign-file lock resolves through HomeLocksDir, so it must refuse too")
}

// TestHomeLocksDir_AcceptsASandboxedHome pins the other arm: a HOME under a
// recognized temp root (what testsupport.SandboxedMain installs) resolves.
func TestHomeLocksDir_AcceptsASandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, AppDirName, HomeLocksDirName), got)
}
