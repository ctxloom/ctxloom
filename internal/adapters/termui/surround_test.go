package termui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSurround(tty *bytes.Buffer, info BarInfo) *surround {
	var mu sync.Mutex
	return newSurround(&mu, tty, true, info)
}

func TestSurround_EstablishSetsScrollRegionAndPaints(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "perky-same-chevy", Engine: "claude-code", Model: "opus", PrefixHint: "^]"})
	s.SetSize(24, 80)

	out := tty.String()
	assert.True(t, strings.HasPrefix(out, "\x1b[1;23r"),
		"DECSTBM must protect exactly the reserved bottom row (rows−1)")
	assert.Contains(t, out, "\x1b[24;1H", "bar paints on the bottom row")
	assert.Contains(t, out, "\x1b7", "cursor saved before the bar paint")
	assert.Contains(t, out, "\x1b8", "cursor restored after the bar paint")
	assert.Contains(t, out, "perky-same-chevy · claude-code/opus")
	assert.Contains(t, out, "^] viewer")
}

func TestSurround_BarContentWidthExact(t *testing.T) {
	info := BarInfo{Harp: "h", Agent: "dev", Engine: "claude-code", Model: "opus", PrefixHint: "^]"}
	got := string(appendBarContent(nil, info, "1● 0◐ 0✓ · kid→executing", 60))
	assert.Equal(t, 60, len([]rune(got)), "bar content pads to exactly the terminal width")
	assert.Contains(t, got, "h · dev · claude-code/opus │ 1● 0◐ 0✓ · kid→executing")
}

func TestSurround_BarContentTruncatesByRune(t *testing.T) {
	info := BarInfo{Harp: "a-very-long-harp-name-indeed", Agent: "developer", Engine: "claude-code", Model: "opus"}
	got := string(appendBarContent(nil, info, "5● 3◐ 9✓ · x→ended", 20))
	assert.Equal(t, 20, len([]rune(got)), "over-wide content truncates to the width")
}

func TestSurround_TinyTerminalSkipsReservation(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(5, 80) // below minRowsForReserve
	assert.Empty(t, tty.String(), "no region, no bar on a tiny terminal")

	// Growing past the threshold establishes; shrinking hands the region back.
	s.SetSize(24, 80)
	require.Contains(t, tty.String(), "\x1b[1;23r")
	tty.Reset()
	s.SetSize(4, 80)
	assert.Equal(t, "\x1b[r", tty.String(), "shrink below threshold resets the scroll region")
}

// TestSurround_ZeroColsSkipsReservation pins that fitWidth used to
// return b[:start] when width<=0, discarding every byte appendBarContent had
// just appended, so a cols==0 report (rows still >= minRowsForReserve) still
// established the region and painted a structurally-valid but completely
// empty bar row, with no warning. The reservation must be honestly OFF when
// there is no column space to render into — the same skip a too-short
// terminal already gets on rows.
func TestSurround_ZeroColsSkipsReservation(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 0) // rows fine, cols==0
	assert.Empty(t, tty.String(), "no region, no blank-bar paint when there are zero columns")
}

func TestSurround_ResizeReestablishes(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 80)
	tty.Reset()
	s.SetSize(40, 120) // SIGWINCH
	out := tty.String()
	assert.Contains(t, out, "\x1b[1;39r", "region tracks the new height")
	assert.Contains(t, out, "\x1b[40;1H", "bar moves to the new bottom row")
}

func TestSurround_RestoreResetsRegionAndClearsBar(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 80)
	tty.Reset()
	s.Restore()
	assert.Equal(t, "\x1b[r\x1b[24;1H\x1b[2K", tty.String(),
		"exit restores the full scroll region and clears the bar row")

	tty.Reset()
	s.Restore()
	assert.Empty(t, tty.String(), "Restore is idempotent")
	s.SetRoster([]RosterEntry{{Harp: "kid", State: "executing"}})
	assert.Empty(t, tty.String(), "nothing paints after Restore")
}

func TestSurround_SuspendResume(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 80)
	s.Suspend()
	tty.Reset()
	s.SetRoster([]RosterEntry{{Harp: "kid", State: "executing"}})
	assert.Empty(t, tty.String(), "no painting while an overlay owns the screen")

	seq := string(s.ResumeSequence())
	assert.Contains(t, seq, "\x1b[1;23r", "resume re-establishes the scroll region the overlay dropped")
	assert.Contains(t, seq, "kid→executing", "resume repaints with the latest roster")
	assert.NotContains(t, seq, "\x1b7", "no DECSC — the engine's engage-time saved cursor must survive")
	assert.Empty(t, tty.String(), "the resume bytes are returned for the release preamble, not written")

	tty.Reset()
	s.SetRoster([]RosterEntry{{Harp: "kid", State: "ended"}})
	assert.Contains(t, tty.String(), "kid→ended", "painting works again after resume")
}

// TestSurround_SetSizeWhileSuspendedDoesNotPaint is the suspend-guard
// defect: SetSize did not check s.suspended, so a resize arriving while an
// overlay owns the screen (Suspend) painted the bar / re-established the
// scroll region directly onto the tty, clobbering the live overlay. The new
// size must still be RECORDED (so ResumeSequence and the resize translator
// pick up the new dimensions on release) but nothing may reach the tty until
// then.
func TestSurround_SetSizeWhileSuspendedDoesNotPaint(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 80)
	s.Suspend()
	tty.Reset()

	s.SetSize(30, 100) // SIGWINCH while the overlay is engaged
	assert.Empty(t, tty.String(), "a resize while suspended must not write to the tty")

	seq := string(s.ResumeSequence())
	assert.Contains(t, seq, "\x1b[1;29r", "resume re-establishes the region at the NEW (recorded) size")
	assert.NotContains(t, seq, "\x1b[1;23r", "the stale pre-resize region must not be what resume re-establishes")
}

// TestSurround_SetSizeWhileSuspendedTracksReservationActiveFlip covers the
// edge where a resize during suspension crosses the minRowsForReserve
// threshold: the active flag must reflect the LATEST size so ResumeSequence
// makes the right call (paint or not) without ever painting mid-suspension.
func TestSurround_SetSizeWhileSuspendedTracksReservationActiveFlip(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 80)
	s.Suspend()
	tty.Reset()

	s.SetSize(4, 80) // shrink below minRowsForReserve while suspended
	assert.Empty(t, tty.String(), "no tty write while suspended, even on a reservation-deactivating resize")

	assert.Nil(t, s.ResumeSequence(), "resume must not re-establish a region the terminal is now too small for")
}

func TestSurround_RosterRepaintWhenIdle(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h", PrefixHint: "^]"})
	s.lastEngineWrite = func() int64 { return 0 } // engine idle forever
	s.SetSize(24, 80)
	tty.Reset()

	s.SetRoster([]RosterEntry{
		{Harp: "swift-elm-fox", State: "executing", LastActivityUnix: 30},
		{Harp: "deep-oak-hen", State: "ended", LastActivityUnix: 10},
		{Harp: "flat-ash-owl", State: "parked", LastActivityUnix: 20},
	})
	out := tty.String()
	assert.Contains(t, out, "1● 1◐ 1✓ · swift-elm-fox→executing",
		"digest counts by state and names the latest transition")
}

func TestSurround_BusyEngineDefersToFlush(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	now := int64(1_000_000_000)
	restore := nowNanos
	nowNanos = func() int64 { return now }
	defer func() { nowNanos = restore }()

	s.lastEngineWrite = func() int64 { return now - 1 } // engine wrote 1ns ago: busy
	s.SetSize(24, 80)
	tty.Reset()

	s.SetRoster([]RosterEntry{{Harp: "kid", State: "queued"}})
	assert.Empty(t, tty.String(), "mid-stream repaint defers to the gate's flush")

	s.FlushLocked() // what the gate runs after its next passthrough write
	assert.Contains(t, tty.String(), "kid→queued")

	tty.Reset()
	s.FlushLocked()
	assert.Empty(t, tty.String(), "flush repaints once per dirty mark")
}

func TestRosterDigest_Empty(t *testing.T) {
	assert.Equal(t, "no agents", rosterDigest([]RosterEntry{}, ""))
}

// TestApprovalsDigest pins the bar's approvals element: "⚑ N · oldest mm:ss"
// while anything is pending (the age of the request waiting longest), nothing
// at zero, and no age when none was given.
func TestApprovalsDigest(t *testing.T) {
	assert.Equal(t, "", approvalsDigest(0, 0, true), "nothing pending, nothing shown")
	assert.Equal(t, "⚑ 2 · oldest 03:12", approvalsDigest(2, 192*time.Second, true))
	assert.Equal(t, "⚑ 1 · oldest 00:00", approvalsDigest(1, -time.Second, true), "a clock step backwards never shows a negative age")
	assert.Equal(t, "⚑ 1 · oldest 61:05", approvalsDigest(1, 61*time.Minute+5*time.Second, true), "minutes keep counting past the hour")
	assert.Equal(t, "⚑ 3", approvalsDigest(3, 0, false), "no arrival known, no age")
}

// TestRosterDigest_ApprovalsLead pins that the approvals element leads the
// digest — even on an otherwise-empty roster, where the digest still owes the
// human the signal — and is absent when nothing is pending.
func TestRosterDigest_ApprovalsLead(t *testing.T) {
	assert.Equal(t, "no agents", rosterDigest(nil, ""), "no approvals element when nothing is pending")
	assert.Equal(t, "⚑ 2 · oldest 00:07 │ no agents", rosterDigest(nil, "⚑ 2 · oldest 00:07"))
	withRoster := []RosterEntry{{Harp: "kid", State: "executing"}}
	assert.NotContains(t, rosterDigest(withRoster, ""), "⚑")
	assert.True(t, strings.HasPrefix(rosterDigest(withRoster, "⚑ 3"), "⚑ 3 │ "), "the approvals element is the LEADING one")
}

// TestSurround_ApprovalsBellRingsOnlyWhenAskedAndVisible pins that the bar
// rings exactly when told to, and only while it is showing — the policy of
// WHEN to ring is the controller's.
func TestSurround_ApprovalsBellRingsOnlyWhenAskedAndVisible(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	s.SetSize(24, 80)
	tty.Reset()

	assert.True(t, s.SetApprovals(2, time.Time{}, true))
	assert.Equal(t, 1, strings.Count(tty.String(), "\a"), "asked to ring, it rings once")

	tty.Reset()
	assert.False(t, s.SetApprovals(2, time.Time{}, false))
	assert.NotContains(t, tty.String(), "\a")

	s.Suspend()
	tty.Reset()
	assert.False(t, s.SetApprovals(3, time.Time{}, true), "a suspended bar does not ring, and says so")
	assert.NotContains(t, tty.String(), "\a")
}

// TestSurround_ApprovalsPaintsCountAndOldestAge confirms the count and the
// oldest request's age actually reach the painted bar row, measured on the
// bar's own clock at paint time.
func TestSurround_ApprovalsPaintsCountAndOldestAge(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "h"})
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.lastEngineWrite = func() int64 { return 0 } // engine idle forever
	s.SetSize(24, 80)
	tty.Reset()

	s.SetApprovals(2, now.Add(-75*time.Second), false)
	assert.Contains(t, tty.String(), "⚑ 2 · oldest 01:15", "the painted bar carries the count and the oldest age")

	tty.Reset()
	now = now.Add(time.Second)
	s.RequestPaint()
	assert.Contains(t, tty.String(), "⚑ 2 · oldest 01:16", "a repaint re-measures the age")

	tty.Reset()
	s.SetApprovals(0, time.Time{}, false)
	assert.NotContains(t, tty.String(), "⚑", "clearing to zero clears the element from the bar")
}
