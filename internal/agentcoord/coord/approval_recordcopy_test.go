package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// U019-F10 claimed serveApproval's `rec = *r` copies a RunRecord whose
// Ladder is a slice header, so the later rec.Ladder.matchingRungs(kind) reads
// the fold's backing array OUTSIDE the View window — a data race.
//
// Refuted, but only because of an invariant that was nowhere asserted: a
// published RunRecord.Ladder is WRITE-ONCE. applyEnqueued mints a brand-new
// record with a brand-new slice from ladderFromFact, and no other arm of
// runsFold.apply ever appends to or assigns into an existing record's
// Ladder — a re-enqueue of the same run_id REPLACES the record rather than
// mutating it, leaving the old backing array untouched for whoever copied
// its header. So the copy escapes the View window over memory nothing will
// ever write again, which is safe; it would stop being safe the moment any
// arm mutated a ladder in place.
//
// That is what these pin. They go red exactly when the row's premise
// becomes true.
func TestRunRecordLadder_IsWriteOnceOncePublished(t *testing.T) {
	base := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	f := newRunsFold()
	f.apply(factAt(factRunEnqueued, at(0), runEnqueued{
		RunID: "r1", Harp: "kid", Agent: "worker", ParentHarp: "owner",
		CredHash: "c1", Depth: 1,
		Ladder: []ladderRungFact{
			{Action: string(ActionRelayToRole), Role: "owner", Timeout: "30s"},
			{Action: string(ActionAutoDecline)},
		},
	}))

	// The copy serveApproval takes, and the header it will still be holding
	// after the View window closes.
	rec := *f.run("r1")
	require.Len(t, rec.Ladder, 2)
	before := append(Ladder(nil), rec.Ladder...)

	// Every later arm that touches this run, plus a re-enqueue of the SAME
	// run_id — the one case that could plausibly write through the header.
	f.apply(factAt(factRunState, at(1), runState{RunID: "r1", State: StateExecuting}))
	f.apply(factAt(factRunHarness, at(2), runHarness{RunID: "r1", HarnessSessionID: "sess-1"}))
	f.apply(factAt(factRunEnded, at(3), runEnded{RunID: "r1", Cause: string(CauseStopped)}))
	f.apply(factAt(factRunEnqueued, at(4), runEnqueued{
		RunID: "r1", Harp: "kid", CredHash: "c2", Depth: 1,
		Ladder: []ladderRungFact{{Action: string(ActionAutoAccept)}},
	}))

	assert.Equal(t, before, rec.Ladder,
		"a RunRecord copy's Ladder must never be written through by a later fact — "+
			"serveApproval reads it outside the journal's View window")
}

// The other half: the walk itself must not hand back memory that aliases the
// fold, or a caller mutating a rung would reach into the registry.
func TestMatchingRungs_DoesNotAliasTheLadder(t *testing.T) {
	l := Ladder{
		{Action: ActionRelayToRole, Role: "owner", Timeout: 30 * time.Second},
		{Action: ActionAutoDecline},
	}
	kind := agentcoordpb.ApprovalRequest_APPROVAL_KIND_UNSPECIFIED

	got := l.matchingRungs(kind)
	require.Len(t, got, 2)
	got[0].Action = ActionAutoAccept
	got[0].Role = "someone-else"

	assert.Equal(t, ActionRelayToRole, l[0].Action, "matchingRungs must return copies, not the ladder's own entries")
	assert.Equal(t, "owner", l[0].Role)
}

// U019-F16 claimed serveApproval uses TWO names for one identity — the audit
// actor is caller.Harp while every detail field comes from rec, and
// relayApproval warns with rec.Harp.
//
// They are one identity, and this is why: the record is looked up BY the
// credential-derived caller.RunID, and applyEnqueued mints the credential
// and the record from the same fact, so runsFold.identityFor(cred).Harp and
// runsFold.run(that run_id).Harp are the same string by construction. Making
// the two names cosmetically uniform would edit the escalation ladder to no
// behavioural end; pinning the invariant is what actually protects the audit
// trail, because the day they diverge the audit entry names one session and
// its details another.
func TestRunsFold_CredentialHarpMatchesItsRunRecord(t *testing.T) {
	f := newRunsFold()
	f.apply(factAt(factSessionCred, time.Now(), sessionCred{Harp: "owner", Project: "proj", CredHash: "owner-hash"}))
	f.apply(factAt(factRunEnqueued, time.Now(), runEnqueued{
		RunID: "r1", Harp: "kid", ParentHarp: "owner", CredHash: "c1", Depth: 1,
	}))

	id, ok := f.identityFor("c1")
	require.True(t, ok)
	require.True(t, id.IsChild())

	rec := f.run(id.RunID)
	require.NotNil(t, rec, "the credential's own run_id must resolve to a record")
	assert.Equal(t, id.Harp, rec.Harp,
		"the audit actor (caller.Harp) and the audited run's harp (rec.Harp) are ONE identity; "+
			"if they can differ, every approval audit entry names two different sessions")
}
