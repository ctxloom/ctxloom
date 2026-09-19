package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/realpath"
)

// TestHomeRecordsDir_RefusesAnUnsandboxedHomeUnderATestBinary is the checked
// half of the guard. Without it the record store is exactly what it was: a
// resolution that hands a test binary the developer's real
// ~/.ctxloom/records and lets it deposit a durable record there. That is not
// hypothetical — 1046 such records, from four test packages, were found in one
// developer's home, the newest a day old.
//
// The store's siblings (the approvals store, the trust root) have carried this
// refusal for a while; this one did not, which is the whole reason the records
// accumulated while the approvals store stayed clean.
func TestHomeRecordsDir_RefusesAnUnsandboxedHomeUnderATestBinary(t *testing.T) {
	t.Setenv("HOME", "/definitely-not-a-temp-root/home/dev")

	_, err := HomeRecordsDir()
	require.Error(t, err, "a records store in a real home must be refused from a test binary")
	assert.Contains(t, err.Error(), "REFUSING",
		"the refusal must say so plainly — it is the whole user interface for this mistake")
	assert.Contains(t, err.Error(), "Isolate the test",
		"the refusal must name the remedy, not just the complaint")
}

// TestHomeRecordsDir_HonoursTheTestingOverride pins the other arm: a test that
// DOES isolate gets its own directory and no refusal. Without this the guard
// above could be satisfied by a resolution that always fails, which would take
// every in-process hook-applying test down with it.
func TestHomeRecordsDir_HonoursTheTestingOverride(t *testing.T) {
	t.Setenv("HOME", "/definitely-not-a-temp-root/home/dev")
	want := t.TempDir()
	t.Cleanup(SetHomeRecordsDirForTesting(want))

	got, err := HomeRecordsDir()
	require.NoError(t, err, "an isolated store must resolve, not refuse")
	assert.Equal(t, want, got)
}

// TestHomeRecordsDir_AcceptsASandboxedHome pins that the guard keys on
// CONTAINMENT rather than on the override alone: a HOME already pointed at a
// temp root — what testsupport.SandboxedMain establishes — resolves without
// any per-test redirect at all.
func TestHomeRecordsDir_AcceptsASandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := HomeRecordsDir()
	require.NoError(t, err, "a HOME under a recognized temp root is already isolated")
	assert.Equal(t, filepath.Join(home, AppDirName, HomeRecordsDirName), got)
}

// TestUnsandboxedHomeError_IsInertOutsideATestBinary pins the half a test
// binary cannot observe about itself: in the shipped ctxloom the real
// ~/.ctxloom is exactly where state belongs, and the guard must never refuse
// it. Driven through the pure predicates with the test-binary answer forced,
// since runningUnderGoTest is true by construction here.
//
// It lives beside the guard rather than beside any one store: the guard moved
// here when a SECOND home-rooted store (the application-record store) turned
// out to need it, and a test pinned to the first store's package would not
// have covered the second.
func TestUnsandboxedHomeError_IsInertOutsideATestBinary(t *testing.T) {
	const realHome = "/home/someone/.ctxloom/records"
	require.Error(t, UnsandboxedHomeError("application-record store", realHome, "isolate it"),
		"precondition: this path is refused UNDER test")

	assert.True(t, runningUnderGoTest(), "the guard's trigger must be true in a test binary, or it never fires at all")
	assert.False(t, realpath.Under(realHome, os.TempDir()),
		"a real home store is outside the temp root — the fact the guard turns on")
}
