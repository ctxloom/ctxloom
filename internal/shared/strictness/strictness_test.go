package strictness

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetForTest restores pristine strict-mode state and registers cleanup so
// the package-global collector never bleeds between tests.
func resetForTest(t *testing.T) {
	t.Helper()
	Reset()
	SetDegraded(false)
	t.Cleanup(func() {
		Reset()
		SetDegraded(false)
	})
}

// Strict mode (the default) collects every Fail as a Finding, preserving
// order, class, formatted message, and fix-it — the abort listing depends on
// all four.
func TestFail_StrictCollectsFindings(t *testing.T) {
	resetForTest(t)

	Fail(ClassConfig, "edit config.yaml", "failed to parse config at %s: %v", "/p/config.yaml", "yaml: bad")
	Fail(ClassSync, "ctxloom remote pull", "bundle %s unfetchable", "core")

	got := All()
	require.Len(t, got, 2)
	assert.Equal(t, ClassConfig, got[0].Class)
	assert.Equal(t, "failed to parse config at /p/config.yaml: yaml: bad", got[0].Message)
	assert.Equal(t, "edit config.yaml", got[0].FixIt)
	assert.Equal(t, ClassSync, got[1].Class)
	assert.Equal(t, "ctxloom remote pull", got[1].FixIt)
}

// Degraded mode is the escape hatch: Fail/FailOnce/Record become pure
// warn-and-continue — nothing is ever recorded, so no choke owner can abort.
func TestDegraded_NothingRecorded(t *testing.T) {
	resetForTest(t)
	SetDegraded(true)

	Fail(ClassConfig, "", "broken config")
	FailOnce(ClassRef, "", "missing parent")
	Record(ClassSync, "", "unfetchable")

	assert.Empty(t, All(), "degraded mode must not collect findings")
	assert.True(t, Degraded())
}

// FailOnce dedups the recording per formatted message (mirroring
// clidiag.WarnOnce's print dedup): a diagnostic re-fired by every subsystem
// that rebuilds a loader yields exactly one finding.
func TestFailOnce_DedupsRecording(t *testing.T) {
	resetForTest(t)

	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "other")

	got := All()
	require.Len(t, got, 2, "identical FailOnce messages collapse; distinct ones don't")
	assert.Contains(t, got[0].Message, `parent core`)
	assert.Contains(t, got[1].Message, `parent other`)
}

// Record collects without printing — the variant for chokes that already own
// their stderr reporting (the sync summary breakdown).
func TestRecord_CollectsInStrict(t *testing.T) {
	resetForTest(t)

	Record(ClassSync, "check network", "pinned bundle %s neither cached nor fetchable", "x")

	got := All()
	require.Len(t, got, 1)
	assert.Equal(t, ClassSync, got[0].Class)
	assert.Equal(t, "pinned bundle x neither cached nor fetchable", got[0].Message)
}

// Checkpoint/Since scope a choke owner's abort decision to its own startup
// window: findings recorded before the mark (a previous session or test in the
// same process) are invisible to it.
func TestCheckpointSince_ScopesFindings(t *testing.T) {
	resetForTest(t)

	Fail(ClassConfig, "", "earlier invocation's finding")
	mark := Checkpoint()
	assert.Empty(t, Since(mark), "fresh checkpoint sees nothing")

	Fail(ClassApply, "", "this invocation's finding")
	got := Since(mark)
	require.Len(t, got, 1)
	assert.Equal(t, "this invocation's finding", got[0].Message)
	assert.Len(t, All(), 2, "All still returns the full history")
}

// A stale mark beyond the current findings length (e.g. after Reset) returns
// nil rather than panicking.
func TestSince_StaleMarkIsNil(t *testing.T) {
	resetForTest(t)
	Fail(ClassConfig, "", "one")
	mark := Checkpoint()
	Reset()
	assert.Nil(t, Since(mark))
}
