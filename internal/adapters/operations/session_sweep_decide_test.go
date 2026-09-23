package operations

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// sdNow anchors every age in the decision table: DecideSweep is pure, so its
// cutoffs and the facts' activity are all stated relative to one instant.
var sdNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func sdReq() SweepRequest {
	return SweepRequest{
		ProjectDir:    "/proj",
		ReclaimCutoff: sdNow.Add(-30 * 24 * time.Hour),
		PurgeCutoff:   sdNow.Add(-90 * 24 * time.Hour),
	}
}

// sdDead is an ended human session, distilled, older than both cutoffs,
// holding reclaimable and purgeable data and nothing that spares it: the
// fact set every row below perturbs one field of.
func sdDead() SessionFacts {
	return SessionFacts{
		Harp:         "aged-quiet-heron",
		ProjectDir:   "/proj",
		Origin:       sessions.OriginSession,
		LastActive:   sdNow.Add(-120 * 24 * time.Hour),
		Lock:         sessionlock.Dead,
		LockReason:   "its lock file exists and nothing holds it",
		Distilled:    true,
		Reclaimable:  true,
		ReclaimBytes: 10,
		Purgeable:    true,
		PurgeBytes:   20,
	}
}

func sdWorktree(path string, v isolation.WorktreeVerdict) isolation.WorktreeCandidate {
	return isolation.WorktreeCandidate{Path: path, Harp: "aged-quiet-heron", Owner: sessionlock.Dead, Verdict: v, Reason: "it has uncommitted changes"}
}

// sdActions projects rows onto their actions, the table's shape.
func sdActions(rows []SweepRow) []SweepAction {
	out := make([]SweepAction, len(rows))
	for i, r := range rows {
		out[i] = r.Action
	}
	return out
}

// TestDecideSweep is the decision table, one case per row, applied in order.
// Each case perturbs the one fact its row turns on.
func TestDecideSweep(t *testing.T) {
	cases := []struct {
		name  string
		facts func() SessionFacts
		want  []SweepAction
		check func(t *testing.T, rows []SweepRow)
	}{
		{
			name: "1 a running session is skipped whole",
			facts: func() SessionFacts {
				f := sdDead()
				f.Lock = sessionlock.Alive
				f.LockReason = "held by pid 42"
				return f
			},
			want:  []SweepAction{SweepSkip},
			check: func(t *testing.T, rows []SweepRow) { assert.Contains(t, rows[0].Reason, "running") },
		},
		{
			name: "2 an unprovable lock is skipped and names the manual route",
			facts: func() SessionFacts {
				f := sdDead()
				f.Lock = sessionlock.Indeterminate
				f.LockReason = "no lock file"
				return f
			},
			want: []SweepAction{SweepSkip},
			check: func(t *testing.T, rows []SweepRow) {
				assert.Equal(t, "ctxloom session purge aged-quiet-heron --even-if-live", rows[0].Command)
			},
		},
		{
			name:  "3 the keep marker keeps it",
			facts: func() SessionFacts { f := sdDead(); f.Kept = true; return f },
			want:  []SweepAction{SweepKeep},
		},
		{
			name: "4 uncommitted work: reap the clean trees, spare the session from reclaim and purge",
			facts: func() SessionFacts {
				f := sdDead()
				f.Worktrees = []isolation.WorktreeCandidate{sdWorktree("/wt/clean", isolation.VerdictReapable), sdWorktree("/wt/wip", isolation.VerdictSpared)}
				return f
			},
			want: []SweepAction{SweepReapWorktrees, SweepSpare},
			check: func(t *testing.T, rows []SweepRow) {
				assert.Equal(t, []string{"/wt/clean"}, rows[0].Worktrees)
				assert.Contains(t, rows[1].Reason, "wip")
			},
		},
		{
			name: "5 reapable worktrees are reaped whatever the age",
			facts: func() SessionFacts {
				f := sdDead()
				f.LastActive = sdNow.Add(-time.Hour)
				f.Worktrees = []isolation.WorktreeCandidate{sdWorktree("/wt/clean", isolation.VerdictReapable)}
				return f
			},
			want: []SweepAction{SweepReapWorktrees},
		},
		{
			name:  "6 aged past the reclaim cutoff only: reclaim",
			facts: func() SessionFacts { f := sdDead(); f.LastActive = sdNow.Add(-60 * 24 * time.Hour); return f },
			want:  []SweepAction{SweepReclaim},
		},
		{
			name:  "7 distilled and aged past the purge cutoff: reclaim, then purge",
			facts: sdDead,
			want:  []SweepAction{SweepReclaim, SweepPurge},
			check: func(t *testing.T, rows []SweepRow) { assert.Equal(t, SweepPlanned, rows[1].Verdict) },
		},
		{
			name:  "8 a human's undistilled session is never purged, and is told how to distill",
			facts: func() SessionFacts { f := sdDead(); f.Distilled = false; return f },
			want:  []SweepAction{SweepReclaim, SweepSpare},
			check: func(t *testing.T, rows []SweepRow) {
				assert.Equal(t, "ctxloom session distill aged-quiet-heron", rows[1].Command)
			},
		},
		{
			name:  "9 an internal one-shot is purged without a distill",
			facts: func() SessionFacts { f := sdDead(); f.Distilled = false; f.Origin = sessions.OriginOneShot; return f },
			want:  []SweepAction{SweepReclaim, SweepPurge},
		},
		{
			name:  "10 undelivered mail spares it from purge and is counted",
			facts: func() SessionFacts { f := sdDead(); f.Mail = 3; return f },
			want:  []SweepAction{SweepReclaim, SweepSpare},
			check: func(t *testing.T, rows []SweepRow) { assert.Contains(t, rows[1].Reason, "3 undelivered") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := DecideSweep(tc.facts(), sdReq())
			assert.Equal(t, tc.want, sdActions(rows))
			for _, r := range rows {
				assert.Equal(t, "aged-quiet-heron", r.Harp)
				if r.Action == SweepSkip || r.Action == SweepKeep || r.Action == SweepSpare {
					assert.NotEmpty(t, r.Reason, "a row that leaves something alone says why")
				}
			}
			if tc.check != nil {
				tc.check(t, rows)
			}
		})
	}
}

// With no purge age stated there is no default: the purge row is reported
// held, never planned, so an apply cannot act on it.
func TestDecideSweep_NoPurgeCutoffHoldsThePurge(t *testing.T) {
	req := sdReq()
	req.PurgeCutoff = time.Time{}
	rows := DecideSweep(sdDead(), req)
	assert.Equal(t, []SweepAction{SweepReclaim, SweepPurge}, sdActions(rows))
	assert.Equal(t, SweepHeld, rows[1].Verdict)
}

// `clean` is the reclaim rows alone: no worktree reaping below the reclaim
// age, no purge, and a session newer than the bound yields no row at all.
func TestDecideSweep_ReclaimOnly(t *testing.T) {
	req := sdReq()
	req.ReclaimOnly = true
	assert.Equal(t, []SweepAction{SweepReclaim}, sdActions(DecideSweep(sdDead(), req)))

	young := sdDead()
	young.LastActive = sdNow.Add(-time.Hour)
	young.Worktrees = []isolation.WorktreeCandidate{sdWorktree("/wt/clean", isolation.VerdictReapable)}
	assert.Empty(t, DecideSweep(young, req))

	running := sdDead()
	running.Lock = sessionlock.Alive
	assert.Equal(t, []SweepAction{SweepSkip}, sdActions(DecideSweep(running, req)))
}
