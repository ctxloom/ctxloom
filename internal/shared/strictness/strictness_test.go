package strictness

import (
	"fmt"
	"sync"
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

// FailOnce dedups the recording per formatted message WITHIN one checkpoint
// window (the print dedup, clidiag.WarnOnce, stays process-wide): a diagnostic
// re-fired by every subsystem that rebuilds a loader yields exactly one
// finding per window. DELIBERATE CHANGE: this test used to pin process-wide
// recording dedup; that swallowed re-fired findings in later windows of a
// long-lived server (`ctxloom acp`), letting a session that was refused on a
// broken profile silently open on retry — see
// TestFailOnce_RefiresAcrossCheckpoints for the cross-window contract.
func TestFailOnce_DedupsRecording(t *testing.T) {
	resetForTest(t)

	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "other")

	got := All()
	require.Len(t, got, 2, "identical FailOnce messages collapse within a window; distinct ones don't")
	assert.Contains(t, got[0].Message, `parent core`)
	assert.Contains(t, got[1].Message, `parent other`)
}

// A long-lived server (`ctxloom acp`) opens each session under a fresh
// Checkpoint. A session refused over a FailOnce finding and retried UNFIXED
// re-fires the same FailOnce — the recording must land in the NEW window, or
// the retry opens silently on broken context (the print dedup even suppresses
// the stderr line). The recording dedup is therefore scoped per checkpoint
// generation, not per process; worst case is a duplicate line inside one
// findings listing.
func TestFailOnce_RefiresAcrossCheckpoints(t *testing.T) {
	resetForTest(t)

	mark1 := Checkpoint()
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	require.Len(t, Since(mark1), 1, "first window collects the finding")

	// The session is retried unfixed: a new window, the same FailOnce.
	mark2 := Checkpoint()
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	got := Since(mark2)
	require.Len(t, got, 1, "the re-fired finding must be visible to the NEW window — otherwise the retried session opens silently on broken context")
	assert.Contains(t, got[0].Message, "parent core")

	// Within the second window the dedup still collapses repeats.
	FailOnce(ClassRef, "ctxloom remote pull", "profile %q: parent %s not installed", "dev", "core")
	assert.Len(t, Since(mark2), 1, "within one window the recording dedup still applies")
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

// TestConcurrentWindows_NoCrossAttribution is the crux regression for the
// concurrency defect documented on Mark: two open windows share ONE
// process-global findings slice, so a finding recorded by goroutine B lands
// inside goroutine A's still-open window too, even though A's own work never
// produced it. Ordering is pinned with channel handshakes (not sleeps), so
// the interleaving — B's Fail landing strictly BETWEEN A's Checkpoint and A's
// Since — is guaranteed on every run, not just likely under load. This must
// fail for a SPECIFIC reason: foundA is non-empty and contains B's message,
// not merely "flaky."
func TestConcurrentWindows_NoCrossAttribution(t *testing.T) {
	resetForTest(t)

	aCheckpointed := make(chan struct{})
	bRecorded := make(chan struct{})
	aDone := make(chan struct{})
	var foundA []Finding

	go func() {
		defer close(aDone)
		markA := Checkpoint()
		close(aCheckpointed)
		<-bRecorded // wait until B has recorded ITS OWN finding
		foundA = Since(markA)
	}()

	<-aCheckpointed // A's window is open before B does any work
	Fail(ClassSync, "", "goroutine B's own finding")
	close(bRecorded)
	<-aDone

	assert.Empty(t, foundA,
		"goroutine A's window must not see a finding recorded by a DIFFERENT concurrent goroutine (B) — cross-attribution")
}

// TestConcurrentWindows_EachSeesOnlyOwnFindings widens the same scenario to N
// goroutines, each opening its own window, recording its OWN uniquely
// identifiable finding, and reading back only after every goroutine has
// recorded (maximizing interleaving opportunity) — pinning "each window
// contains EXACTLY the finding(s) its own goroutine produced, nothing more,
// nothing less" as the general per-window-ownership contract, not just the
// 2-goroutine minimal repro above.
func TestConcurrentWindows_EachSeesOnlyOwnFindings(t *testing.T) {
	resetForTest(t)

	const n = 12
	var start sync.WaitGroup
	var recorded sync.WaitGroup
	var done sync.WaitGroup
	release := make(chan struct{})
	results := make([][]Finding, n)

	start.Add(n)
	recorded.Add(n)
	done.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer done.Done()
			mark := Checkpoint()
			start.Done()
			start.Wait() // maximize the window all goroutines have open at once
			Fail(ClassSync, "", "finding from goroutine %d", i)
			recorded.Done()
			recorded.Wait() // every goroutine has now recorded before anyone reads
			<-release
			results[i] = Since(mark)
		}(i)
	}
	close(release)
	done.Wait()

	for i, got := range results {
		require.Len(t, got, 1, "goroutine %d's window must contain exactly its own finding", i)
		assert.Equal(t, fmt.Sprintf("finding from goroutine %d", i), got[0].Message)
	}
}
