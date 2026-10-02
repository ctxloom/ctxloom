package paths

import (
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/realpath"
)

// TestHomeLocksDir_RefusesTheAccountsRealHomeUnderATestBinary is the checked
// guard for the home lock directory. Every foreign-file lock (HomePathFor)
// resolves through HomeLocksDir, so a test package that drives a locked write
// without the sandbox would otherwise drop a durable lock file into the
// developer's real ~/.ctxloom/locks on every run. Refusing here turns that
// omission into a red test in whichever package made it.
//
// HOME is pointed at the account's home from the passwd database, which is
// exactly what an unsandboxed test binary inherits.
func TestHomeLocksDir_RefusesTheAccountsRealHomeUnderATestBinary(t *testing.T) {
	u, err := user.Current()
	require.NoError(t, err, "the account's home is what this guard protects; without it there is nothing to test")
	t.Setenv("HOME", u.HomeDir)

	_, err = HomeLocksDir()
	require.Error(t, err, "a lock directory in the account's real home must be refused from a test binary")
	assert.Contains(t, err.Error(), "REFUSING")

	_, err = HomePathFor(filepath.Join(t.TempDir(), "settings.json"))
	require.Error(t, err, "every foreign-file lock resolves through HomeLocksDir, so it must refuse too")
}

// TestHomeLocksDir_ResolvesAForeignHomeItOnlyComputes pins the narrowing. A
// caller may point HOME at a home that is not this account's — the container
// mount builder's tests compute where a CONTAINER's own HomePathFor will look
// (HOME=/home/ctxloom) — and nothing is written there by resolving it. Only
// the account's real home is the developer's state; refusing every non-temp
// path refused a derivation, not a write.
func TestHomeLocksDir_ResolvesAForeignHomeItOnlyComputes(t *testing.T) {
	const foreign = "/definitely-not-a-temp-root/home/ctxloom"
	if u, err := user.Current(); err == nil && realpath.Under(foreign, u.HomeDir) {
		t.Skipf("the account's home %s contains %s, so it is not foreign here", u.HomeDir, foreign)
	}
	t.Setenv("HOME", foreign)

	got, err := HomeLocksDir()
	require.NoError(t, err, "a home that is not the account's must resolve: nothing is written by deriving it")
	assert.Equal(t, filepath.Join(foreign, AppDirName, HomeLocksDirName), got)
}

// TestHomeLocksDir_AcceptsASandboxedHome pins the sandbox arm: a HOME under a
// recognized temp root (what testsupport.SandboxedMain installs) resolves.
func TestHomeLocksDir_AcceptsASandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := HomeLocksDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, AppDirName, HomeLocksDirName), got)
}

// TestHomeLocksDir_HonoursTheTestingOverride: a test that keeps the real HOME
// on purpose (the integration helpers resolve a real installed companion
// through it) redirects exactly the lock directory instead, and is not
// refused. Without this the guard could be satisfied by a resolution that
// always fails.
func TestHomeLocksDir_HonoursTheTestingOverride(t *testing.T) {
	u, err := user.Current()
	require.NoError(t, err)
	t.Setenv("HOME", u.HomeDir)
	want := t.TempDir()
	t.Cleanup(SetHomeLocksDirForTesting(want))

	got, err := HomeLocksDir()
	require.NoError(t, err, "a redirected lock directory must resolve, not refuse")
	assert.Equal(t, want, got)
}
