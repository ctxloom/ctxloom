package coord

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Acceptance (6): REPLAY EQUIVALENCE with the folds ENUMERATED — run
// registry, spawn queue, roster, mailbox cursors — a defined equality
// projection, and seeded/generated command sequences. Replaying the same
// journals into fresh folds must produce a byte-identical projection: this is
// what makes "a fresh `ctxloom run` adopts orphaned state from disk"
// restart-safe by construction and kills the stranded-queue bug class.

// foldProjection is the defined equality projection over ALL enumerated folds.
type foldProjection struct {
	// run registry: run_id → (state, ended, cause, credHash presence, depth)
	Runs map[string]string
	// harp → current run_id
	ByHarp map[string]string
	// active credential hashes (revocation-sensitive)
	Creds []string
	// spawn queue: queued run_ids in order + the executing count
	Queued    []string
	Executing int
	// roster: harp → state, sorted
	Roster map[string]string
	// mailbox: role → pending message ids in order (cursor-sensitive)
	Mailbox map[string][]string
	// reports (B1.6): harp → latest summary line; harp → artifact_id →
	// "rev/sha" (revision monotonicity + content addressing)
	Reports   map[string]string
	Artifacts map[string]map[string]string
}

// projectRuns builds the projection from a run-registry journal's folds.
func projectRuns(runsF *runsFold, queueF *queueFold, rosterF *rosterFold, reportsF *reportsFold) foldProjection {
	p := foldProjection{
		Runs:      map[string]string{},
		ByHarp:    map[string]string{},
		Roster:    map[string]string{},
		Mailbox:   map[string][]string{},
		Reports:   map[string]string{},
		Artifacts: map[string]map[string]string{},
	}
	for id, r := range runsF.runs {
		p.Runs[id] = fmt.Sprintf("state=%s ended=%v cause=%s depth=%d cred=%v", r.State, r.Ended, r.Cause, r.Depth, r.CredHash != "")
	}
	for harp, id := range runsF.byHarp {
		p.ByHarp[harp] = id
	}
	for hash := range runsF.creds {
		p.Creds = append(p.Creds, hash)
	}
	sortStrings(p.Creds)
	p.Queued = queueF.queued()
	p.Executing = queueF.executing
	for _, e := range rosterF.snapshot() {
		p.Roster[e.Harp] = e.State
	}
	for harp := range reportsF.latest {
		p.Reports[harp] = reportsF.latestSummary(harp)
	}
	for harp, byID := range reportsF.artifacts {
		m := map[string]string{}
		for id, rec := range byID {
			m[id] = fmt.Sprintf("%d/%s", rec.Revision, rec.SHA256)
		}
		p.Artifacts[harp] = m
	}
	return p
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestReplayEquivalence_RunRegistry drives a seeded command sequence of run
// lifecycle facts through one store, then replays the same journal into fresh
// folds, and asserts the projections are identical. Deterministic: an
// implementer knows when it's done.
func TestReplayEquivalence_RunRegistry(t *testing.T) {
	for seed := int64(0); seed < 16; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "runs.jsonl")
			runsF, queueF, rosterF, reportsF := newRunsFold(), newQueueFold(), newRosterFold(), newReportsFold()
			store, err := openStore(path, runsF, queueF, rosterF, reportsF)
			require.NoError(t, err)

			rng := rand.New(rand.NewSource(seed))
			at := time.Unix(1_700_000_000, 0)
			live := map[string]string{} // run_id → harp for live runs
			var allRuns []string
			nextRun := 0

			appendFact := func(f Fact) {
				require.NoError(t, store.Exec(func() ([]Fact, error) { return []Fact{f}, nil }))
			}

			for step := 0; step < 40; step++ {
				at = at.Add(time.Second)
				switch rng.Intn(6) {
				case 0: // enqueue a fresh run
					id := fmt.Sprintf("run-%d", nextRun)
					harp := fmt.Sprintf("harp-%d", nextRun%7) // harps reused (resume)
					nextRun++
					appendFact(factAt(factRunEnqueued, at, runEnqueued{
						RunID: id, Harp: harp, Agent: "worker",
						CredHash: fmt.Sprintf("hash-%d", nextRun), Depth: 1,
					}))
					live[id] = harp
					allRuns = append(allRuns, id)
				case 1: // transition a live run
					if id := pickLive(rng, live); id != "" {
						states := []string{StateExecuting, StateParked, StateIdle, StateQueued}
						appendFact(factAt(factRunState, at, runState{RunID: id, State: states[rng.Intn(len(states))]}))
					}
				case 2: // end a live run
					if id := pickLive(rng, live); id != "" {
						appendFact(factAt(factRunEnded, at, runEnded{RunID: id, Cause: CauseChatClose}))
						delete(live, id)
					}
				case 3: // bind a harness session id
					if len(allRuns) > 0 {
						id := allRuns[rng.Intn(len(allRuns))]
						appendFact(factAt(factRunHarness, at, runHarness{RunID: id, HarnessSessionID: "sid"}))
					}
				case 4: // file a report (B1.6 fact kind)
					harp := fmt.Sprintf("harp-%d", rng.Intn(7))
					appendFact(factAt(factSummary, at, summaryFact{
						Harp: harp, Scope: "SCOPE_PROGRESS",
						Text: fmt.Sprintf("progress %d", step),
					}))
				case 5: // stamp an artifact manifest (B1.6 fact kind)
					harp := fmt.Sprintf("harp-%d", rng.Intn(7))
					appendFact(factAt(factArtifact, at, artifactFact{
						Harp: harp, ArtifactID: fmt.Sprintf("plan/p%d", rng.Intn(3)),
						Revision: uint32(rng.Intn(4) + 1), SHA256: fmt.Sprintf("%02x", rng.Intn(256)),
					}))
				}
			}
			before := projectRuns(runsF, queueF, rosterF, reportsF)
			require.NoError(t, store.Close())

			// Replay the same journal into fresh folds.
			rRunsF, rQueueF, rRosterF, rReportsF := newRunsFold(), newQueueFold(), newRosterFold(), newReportsFold()
			rStore, err := openStore(path, rRunsF, rQueueF, rRosterF, rReportsF)
			require.NoError(t, err)
			replayed := projectRuns(rRunsF, rQueueF, rRosterF, rReportsF)
			require.NoError(t, rStore.Close())

			assert.Equal(t, before, replayed, "replaying the run-registry journal must reproduce the projection exactly")
		})
	}
}

func pickLive(rng *rand.Rand, live map[string]string) string {
	if len(live) == 0 {
		return ""
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sortStrings(ids)
	return ids[rng.Intn(len(ids))]
}

// TestReplayEquivalence_Mailbox drives a seeded queue/consume sequence through
// the mailbox journal and asserts the cursor projection replays identically —
// consumed messages stay consumed, dedupe holds, order is preserved.
func TestReplayEquivalence_Mailbox(t *testing.T) {
	for seed := int64(0); seed < 16; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mailbox.jsonl")
			mailF := newMailFold()
			store, err := openStore(path, mailF)
			require.NoError(t, err)

			rng := rand.New(rand.NewSource(seed + 100))
			at := time.Unix(1_700_000_000, 0)
			roles := []string{"harp-a", "harp-b", "harp-c"}
			queued := map[string][]string{} // role → unconsumed ids in order
			nextMsg := 0

			appendFact := func(f Fact) {
				require.NoError(t, store.Exec(func() ([]Fact, error) { return []Fact{f}, nil }))
			}

			for step := 0; step < 40; step++ {
				at = at.Add(time.Second)
				role := roles[rng.Intn(len(roles))]
				if rng.Intn(2) == 0 { // queue
					id := fmt.Sprintf("m-%d", nextMsg)
					nextMsg++
					appendFact(factAt(factMailQueued, at, mailQueued{MessageID: id, From: "x", To: role, Body: "b"}))
					queued[role] = append(queued[role], id)
					// A dedupe attempt: re-queue the same id (must be ignored).
					if rng.Intn(3) == 0 {
						appendFact(factAt(factMailQueued, at, mailQueued{MessageID: id, From: "x", To: role, Body: "b"}))
					}
				} else if len(queued[role]) > 0 { // consume a prefix
					n := 1 + rng.Intn(len(queued[role]))
					appendFact(factAt(factMailConsumed, at, mailConsumed{Role: role, MessageIDs: queued[role][:n]}))
					queued[role] = queued[role][n:]
				}
			}

			project := func(f *mailFold) map[string][]string {
				out := map[string][]string{}
				for _, role := range roles {
					for _, m := range f.pendingFor(role) {
						out[role] = append(out[role], m.ID)
					}
				}
				return out
			}
			before := project(mailF)
			require.NoError(t, store.Close())

			rMailF := newMailFold()
			rStore, err := openStore(path, rMailF)
			require.NoError(t, err)
			after := project(rMailF)
			require.NoError(t, rStore.Close())

			assert.Equal(t, before, after, "replaying the mailbox journal must reproduce pending/consumed exactly")
		})
	}
}

// TestJournal_TruncatesTornTail pins the torn-tail discipline: a crash
// mid-append (a final line without its newline, or an unparseable final line)
// is truncated on replay, and the earlier facts survive.
func TestJournal_TruncatesTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.jsonl")
	runsF, queueF, rosterF := newRunsFold(), newQueueFold(), newRosterFold()
	store, err := openStore(path, runsF, queueF, rosterF)
	require.NoError(t, err)
	at := time.Unix(1_700_000_000, 0)
	require.NoError(t, store.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factRunEnqueued, at, runEnqueued{RunID: "run-1", Harp: "harp-1", CredHash: "h1", Depth: 1})}, nil
	}))
	require.NoError(t, store.Close())

	// Simulate a torn append: a partial second line without a newline.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"kind":"run.state","at":"2023`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Replay must truncate the torn tail and keep run-1.
	rRunsF, rQueueF, rRosterF := newRunsFold(), newQueueFold(), newRosterFold()
	rStore, err := openStore(path, rRunsF, rQueueF, rRosterF)
	require.NoError(t, err)
	require.NotNil(t, rRunsF.run("run-1"), "the intact fact before the torn tail survives")
	assert.Equal(t, StateQueued, rRunsF.run("run-1").State)
	_ = rQueueF
	_ = rRosterF

	// A fresh append after truncation lands cleanly (the file offset is
	// correct: no torn bytes remain).
	require.NoError(t, rStore.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factRunState, at, runState{RunID: "run-1", State: StateExecuting})}, nil
	}))
	require.NoError(t, rStore.Close())

	vRunsF, vQueueF, vRosterF := newRunsFold(), newQueueFold(), newRosterFold()
	vStore, err := openStore(path, vRunsF, vQueueF, vRosterF)
	require.NoError(t, err)
	assert.Equal(t, StateExecuting, vRunsF.run("run-1").State, "the post-truncation append replays cleanly")
	_ = vQueueF
	_ = vRosterF
	require.NoError(t, vStore.Close())
}
