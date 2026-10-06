package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHomeCompanionAllowPath_RefusesAnUnsandboxedHomeUnderATestBinary: the
// allow store decides which binaries ctxloom executes, so a test that wrote a
// record into the developer's real home would admit a binary on their machine.
func TestHomeCompanionAllowPath_RefusesAnUnsandboxedHomeUnderATestBinary(t *testing.T) {
	t.Setenv("HOME", "/definitely-not-a-temp-root/home/dev")

	_, err := HomeCompanionAllowPath()
	require.Error(t, err, "a companion allow store in a real home must be refused from a test binary")
	assert.Contains(t, err.Error(), "REFUSING")
}

// TestHomeCompanionAllowPath_AcceptsASandboxedHome: a HOME under a temp root
// resolves to the per-user file beside the other home stores.
func TestHomeCompanionAllowPath_AcceptsASandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := HomeCompanionAllowPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, AppDirName, CompanionAllowFileName+".yaml"), got)
}

// TestLayout_ClassifiesTheCompanionAllowStore: the store is personal state
// that nothing rebuilds, so it must be in the home layout as local.
func TestLayout_ClassifiesTheCompanionAllowStore(t *testing.T) {
	want := filepath.Join(AppDirName, CompanionAllowFileName+".yaml")
	for _, e := range Layout() {
		if e.Rel == want && e.Root == RootHome {
			assert.Equal(t, TierLocal, e.Tier)
			assert.NotEmpty(t, e.Lost)
			return
		}
	}
	t.Fatalf("no home Layout row for %s", want)
}
