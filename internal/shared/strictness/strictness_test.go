package strictness

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	Fail(ClassSync, "ctxloom deps pull", "bundle %s unfetchable", "core")

	got := All()
	require.Len(t, got, 2)
	assert.Equal(t, ClassConfig, got[0].Class)
	assert.Equal(t, "failed to parse config at /p/config.yaml: yaml: bad", got[0].Message)
	assert.Equal(t, "edit config.yaml", got[0].FixIt)
	assert.Equal(t, ClassSync, got[1].Class)
	assert.Equal(t, "ctxloom deps pull", got[1].FixIt)
}

// Degraded mode suppresses FATALITY, not RECORDING. Fail/FailOnce/Record
// still warn AND still collect, so a degraded run can answer "what did you
// skip?" and the retained records can feed metrics; what degraded changes is
// that no gate acts on them. Asserting the COLLECTION and the SILENT GATE
// together is the point: either alone is satisfied by the other mode.
func TestDegraded_RecordsButNeverAborts(t *testing.T) {
	resetForTest(t)
	mark := Checkpoint()
	SetDegraded(true)

	Fail(ClassConfig, "", "broken config")
	FailOnce(ClassRef, "", "missing parent")
	Record(ClassSync, "", "unfetchable")

	require.Len(t, All(), 3, "degraded still collects every finding")
	assert.NoError(t, FindingsError(mark),
		"the GATE is what degraded silences: findings exist, nothing aborts on them")
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

	FailOnce(ClassRef, "ctxloom deps pull", "profile %q: parent %s not installed", "dev", "core")
	FailOnce(ClassRef, "ctxloom deps pull", "profile %q: parent %s not installed", "dev", "core")
	FailOnce(ClassRef, "ctxloom deps pull", "profile %q: parent %s not installed", "dev", "other")

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
	FailOnce(ClassRef, "ctxloom deps pull", "profile %q: parent %s not installed", "dev", "core")
	require.Len(t, Since(mark1), 1, "first window collects the finding")

	// The session is retried unfixed: a new window, the same FailOnce.
	mark2 := Checkpoint()
	FailOnce(ClassRef, "ctxloom deps pull", "profile %q: parent %s not installed", "dev", "core")
	got := Since(mark2)
	require.Len(t, got, 1, "the re-fired finding must be visible to the NEW window — otherwise the retried session opens silently on broken context")
	assert.Contains(t, got[0].Message, "parent core")

	// Within the second window the dedup still collapses repeats.
	FailOnce(ClassRef, "ctxloom deps pull", "profile %q: parent %s not installed", "dev", "core")
	assert.Len(t, Since(mark2), 1, "within one window the recording dedup still applies")
}

// A finding whose message formats to nothing renders as a bare "  - " bullet
// carrying a fix-it and no statement of what broke — the abort is loud but the
// PAYLOAD is empty, which is this project's characteristic failure. The class
// is the one thing still known at that point, so it is what the substitute has
// to report. Asserted on the payload, never on the exit code.
func TestFail_EmptyMessageStillSaysWhatBroke(t *testing.T) {
	resetForTest(t)

	mark := Checkpoint()
	Fail(ClassConfig, "ctxloom manage config edit", "")

	got := Since(mark)
	require.Len(t, got, 1)
	assert.NotEmpty(t, strings.TrimSpace(got[0].Message), "a finding must always say something")
	assert.Contains(t, got[0].Message, string(ClassConfig), "the class is the only detail left to report")

	err := FindingsError(mark)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\n  - (fix:", "no bare bullet whose only content is the fix-it")
}

// The same guard covers the other two entry points, and a whitespace-only
// message is as empty as "" once rendered into a bullet.
func TestFailOnceAndRecord_BlankMessageStillSayWhatBroke(t *testing.T) {
	resetForTest(t)

	mark := Checkpoint()
	FailOnce(ClassRef, "ctxloom deps pull", "")
	Record(ClassSync, "", "   \n ")

	got := Since(mark)
	require.Len(t, got, 2)
	for i, f := range got {
		assert.NotEmpty(t, strings.TrimSpace(f.Message), "finding %d must say something", i)
		assert.Contains(t, f.Message, string(f.Class))
	}
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

// FailOnce carries TWO dedups with different scopes, and conflating them is
// how a reader reasons wrongly about this package: the WARNING line is deduped
// process-wide and permanently (clidiag.WarnOnce), while the FINDING is deduped
// only within one checkpoint window. Both halves of that sentence are asserted
// here in one place, because the doc used to claim the finding was
// per-process too — which would mean a refused-and-retried session opening
// silently on broken context.
func TestFailOnce_PrintDedupIsProcessWideRecordDedupIsPerWindow(t *testing.T) {
	resetForTest(t)

	var out bytes.Buffer
	restore := clidiag.SetSink(&out)
	t.Cleanup(restore)

	const msg = "u119f07 probe: profile parent not installed"
	mark1 := Checkpoint()
	FailOnce(ClassRef, "ctxloom deps pull", "%s", msg)
	_ = Checkpoint() // a new window: the retried session
	FailOnce(ClassRef, "ctxloom deps pull", "%s", msg)

	assert.Equal(t, 1, strings.Count(out.String(), msg),
		"the warning LINE is deduped process-wide — it prints at most once whatever the window")
	assert.Len(t, Since(mark1), 2,
		"the FINDING is deduped only within a window, so the re-fire in the next window records again")
}

// strictness and clidiag dedup DIFFERENT things on DIFFERENT keys, and the
// extra dimension is a contract rather than drift: clidiag dedups PRINTING on
// the rendered line and has no notion of a failure class (giving it one would
// invert the dependency — clidiag is the family-wide convention shared with
// the companion binaries), while strictness dedups RECORDING on generation +
// class + message. One message text raised under two classes therefore prints
// ONE line and records TWO findings, each keeping its own class and its own
// fix-it. Unifying the keys on the message alone would swallow a genuinely
// different fault whose text happens to match, and take its fix-it with it.
func TestFailOnce_SameMessageUnderTwoClassesRecordsBothButPrintsOnce(t *testing.T) {
	resetForTest(t)

	var out bytes.Buffer
	restore := clidiag.SetSink(&out)
	t.Cleanup(restore)

	const msg = "u119f08 probe: could not be satisfied as requested"
	mark := Checkpoint()
	FailOnce(ClassIsolation, "ctxloom manage image build", "%s", msg)
	FailOnce(ClassTask, "check the project task log", "%s", msg)

	assert.Equal(t, 1, strings.Count(out.String(), msg),
		"clidiag keys on the rendered line and has no class dimension, so the line prints once")

	fixByClass := map[Class]string{}
	for _, f := range Since(mark) {
		fixByClass[f.Class] = f.FixIt
	}
	require.Len(t, fixByClass, 2, "the class dimension keeps two genuinely different faults apart")
	assert.Equal(t, "ctxloom manage image build", fixByClass[ClassIsolation])
	assert.Equal(t, "check the project task log", fixByClass[ClassTask],
		"collapsing the keys onto the message alone would lose this fault and its fix-it entirely")
}

// Recording no longer consults the mode at all: degraded suppresses FATALITY,
// not RECORDING, so a concurrent SetDegraded(true) can neither lose a finding
// nor block one. The count must keep GROWING across the flip -- that is what
// dies if anyone reinstates record's old degraded early-return, which used to
// make the flip linearizable with collection and now would silently discard
// the audit trail a degraded run exists to leave behind.
//
// The mode's observer is the GATE, asserted here alongside so the pair reads
// together: findings accumulate, FindingsError stays silent.
func TestSetDegraded_DoesNotStopRecording(t *testing.T) {
	resetForTest(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					Record(ClassSync, "", "racing finding")
				}
			}
		}()
	}
	for len(All()) < 200 { // the recorders are demonstrably running
		runtime.Gosched()
	}

	mark := Checkpoint()
	SetDegraded(true)
	atFlip := len(All())
	close(stop)
	wg.Wait()

	assert.Greater(t, len(All()), atFlip,
		"recording must continue across the flip: degraded suppresses fatality, not the audit trail")
	assert.NoError(t, FindingsError(mark),
		"the gate is the mode's only observer — it stays silent while collection continues")
}

// goroutineID's ParseInt cannot fail, so its swallowed error is a dead branch:
// runtime.Stack always writes the fixed "goroutine <id> [<status>]:" preamble
// for the CALLING goroutine, and even the widest id an int64 counter can reach
// leaves the 64-byte buffer room to spare (asserted below). The hazard the
// dead branch would create if it ever fired is worth pinning anyway, because
// it is not the one it looks like: returning 0 for every goroutine would
// collapse them onto ONE shared window — the cross-attribution defect the
// window type exists to fix. Pin the observable property rather than the dead
// branch: ids are nonzero, stable per goroutine, and distinct across
// goroutines.
func TestGoroutineID_IsNonZeroStableAndDistinctPerGoroutine(t *testing.T) {
	assert.Less(t, len("goroutine 9223372036854775807 [running]:"), 64,
		"the widest preamble Go can print still fits the buffer goroutineID reads into")

	first := goroutineID()
	require.NotZero(t, first, "a parse failure would return 0 here")
	assert.Equal(t, first, goroutineID(), "one goroutine keeps ONE id, so Checkpoint and record share a window")

	const n = 24
	ids := make([]int64, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ids[i] = goroutineID()
		}(i)
	}
	wg.Wait()

	seen := map[int64]int{}
	for i, id := range ids {
		require.NotZero(t, id, "goroutine %d must not fall back to the shared 0 window", i)
		seen[id]++
	}
	assert.Len(t, seen, n,
		"every goroutine must get its OWN id — a swallowed parse error collapses them all onto 0, which is exactly the cross-attribution defect windows exist to prevent")
	assert.NotContains(t, seen, first, "the spawning goroutine's id is not reused by any child")
}

// Nested checkpoints on ONE goroutine share ONE window, so releasing the
// registry entry when the INNER one closes silently detaches the still-live
// OUTER mark: the next record builds a fresh window and Since(outer) reads the
// orphaned old one, missing every finding recorded after the inner Close. That
// is a fail-loudly gate going quiet, which is the one failure this package
// exists to prevent — the registry entry may only be released when the LAST
// bracketing checkpoint closes.
func TestClose_InnerCloseDoesNotDetachAStillLiveOuterMark(t *testing.T) {
	resetForTest(t)

	outer := Checkpoint()
	func() {
		inner := Checkpoint()
		defer Close(inner)
		Fail(ClassConfig, "", "finding inside the inner bracket")
	}()
	Fail(ClassApply, "", "finding recorded after the inner Close")

	got := Since(outer)
	require.Len(t, got, 2, "the outer window must still collect after an inner Close")
	assert.Equal(t, "finding inside the inner bracket", got[0].Message)
	assert.Equal(t, "finding recorded after the inner Close", got[1].Message)
}

// The release is still a release: once the LAST bracketing checkpoint closes,
// the goroutine-keyed registry entry is gone, so a long-lived process that
// opens one window per request goroutine does not accumulate an entry forever.
func TestClose_LastCloseStillReleasesTheRegistryEntry(t *testing.T) {
	resetForTest(t)

	outer := Checkpoint()
	inner := Checkpoint()
	gid := outer.w.gid
	Close(inner)

	windowsMu.Lock()
	_, stillOpen := windows[gid]
	windowsMu.Unlock()
	require.True(t, stillOpen, "the outer bracket is still live, so the entry stays")

	Close(outer)
	windowsMu.Lock()
	_, afterLast := windows[gid]
	windowsMu.Unlock()
	assert.False(t, afterLast, "the last Close releases the entry")
}

// Close is documented as safe to call more than once with the same Mark. It
// must therefore be one-shot PER CHECKPOINT: a repeated Close of the inner
// mark must not consume the outer bracket's release and re-open the detach
// hazard above.
func TestClose_RepeatedCloseOfTheSameMarkIsOneShot(t *testing.T) {
	resetForTest(t)

	outer := Checkpoint()
	inner := Checkpoint()
	Close(inner)
	Close(inner)
	Close(inner)
	Fail(ClassSync, "", "recorded after three Closes of the inner mark")

	assert.Len(t, Since(outer), 1, "repeated Close of one mark must not release the outer bracket")
	assert.NotPanics(t, func() { Close(Mark{}) }, "a zero Mark stays a no-op")
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

// TestFailOnce_ConcurrentWindows_DedupDoesNotCrossWindows pins the fix:
// FailOnce's recording dedup key used to be built from the LIVE global
// generation counter read at record() time, not the generation the
// RECORDING goroutine's own window captured at ITS last Checkpoint. generation
// is a single counter shared by every goroutine (each Checkpoint call bumps
// it once, regardless of caller) and strictly increases — so a goroutine
// that checkpoints EARLIER (a lower generation) but records LATER than a
// second, concurrently checkpointing goroutine used to compute its dedup key
// from whatever generation the counter had advanced to BY THEN, which could
// be the same value the second goroutine's own record() call also reads.
// Two different windows landing on the same key means the second goroutine's
// FailOnce for a message it has never actually seen gets silently swallowed
// as "already recorded" by the first goroutine's entry.
//
// Repro shape: A checkpoints (generation -> 1), THEN B checkpoints while A's
// window is still open (generation -> 2), THEN A records — pre-fix, A's key
// read the LIVE generation (2, B's value, not A's own 1) — THEN B records the
// identical message. Pre-fix both keys read the live counter (2) and
// collide; B's finding vanishes. Post-fix each window remembers its own
// captured generation (A: 1, B: 2), so the keys differ and both windows see
// their own recording.
func TestFailOnce_ConcurrentWindows_DedupDoesNotCrossWindows(t *testing.T) {
	resetForTest(t)

	aCheckpointed := make(chan struct{})
	bCheckpointed := make(chan struct{})
	aRecorded := make(chan struct{})
	aDone := make(chan struct{})
	var markA Mark
	var foundA []Finding

	go func() {
		defer close(aDone)
		markA = Checkpoint() // generation 1
		close(aCheckpointed)
		<-bCheckpointed // B has now bumped generation to 2 while A's window is still open
		FailOnce(ClassSync, "", "duplicate message")
		close(aRecorded)
		foundA = Since(markA)
	}()

	<-aCheckpointed
	markB := Checkpoint() // generation 2, while A's window is still open
	close(bCheckpointed)
	<-aRecorded // A has already recorded, under whatever generation ITS window remembers
	FailOnce(ClassSync, "", "duplicate message")
	foundB := Since(markB)
	<-aDone

	require.Len(t, foundA, 1, "goroutine A's own FailOnce must land in its own window")
	require.Len(t, foundB, 1, "goroutine B's FailOnce for the SAME message must still record — it is a DIFFERENT goroutine's window/generation, not a repeat within A's, and must not be swallowed by a dedup key collision on the shared live generation counter")
}

// TestFailAlways_SurvivesDegraded pins the ONE property NonDegradable exists
// for: a fault whose harm is the launch itself must still abort under
// --degraded, while an ordinary finding raised in the same window must not.
//
// Both halves are asserted together deliberately. "The non-degradable one
// aborts" alone passes if degraded were ignored entirely; "the ordinary one
// does not" alone passes if nothing ever aborted. Only the pair distinguishes
// a working filter from either failure.
func TestFailAlways_SurvivesDegraded(t *testing.T) {
	resetForTest(t)
	mark := Checkpoint()
	SetDegraded(true)

	Fail(ClassConfig, "edit the config", "an ordinary degradable fault")
	FailAlways(ClassIsolation, "start the runtime it claimed", "the launch itself is the harm")

	require.Len(t, All(), 2, "degraded records both; it is fatality that differs")

	act := Actionable(Since(mark))
	require.Len(t, act, 1, "only the non-degradable finding may survive --degraded")
	assert.Equal(t, ClassIsolation, act[0].Class)
	assert.True(t, act[0].NonDegradable)

	err := FindingsError(mark)
	require.Error(t, err, "a non-degradable finding must still abort under --degraded")
	assert.Contains(t, err.Error(), "the launch itself is the harm")
	assert.NotContains(t, err.Error(), "an ordinary degradable fault",
		"the degradable finding must not be dragged into the abort alongside it")
}

// TestActionable_StrictModePassesEverything is the other arm: outside degraded
// mode the filter must be transparent, or it would silently narrow every gate
// in the product to non-degradable faults only.
func TestActionable_StrictModePassesEverything(t *testing.T) {
	resetForTest(t)
	Fail(ClassConfig, "", "degradable")
	FailAlways(ClassIsolation, "", "non-degradable")

	assert.Len(t, Actionable(All()), 2, "strict mode acts on every finding")
}
