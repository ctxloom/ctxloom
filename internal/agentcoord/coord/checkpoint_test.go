package coord

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// D4 CHECKPOINT compaction — replay equivalence including the snapshot fold
// (Wave D playbook acceptance). Mirrors TestReplayEquivalence_Items'
// seeded-sequence style, extended with a snapshot taken mid-sequence: a
// fresh fold restored from that snapshot and replayed from its offset must
// reach the IDENTICAL projection as a fold replayed fully from byte 0 —
// snapshot-then-tail and full replay are equivalent by construction.
func TestReplayEquivalence_ItemsSnapshot(t *testing.T) {
	kinds := []string{"run_started", "message_started", "message_delta", "message_completed", "tool_call_started", "tool_call_args_delta", "tool_call_completed", "status_changed", "run_completed"}
	for seed := int64(0); seed < 16; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "items.jsonl")
			itemsF := newItemsFold()
			store, err := openStore(path, itemsF)
			require.NoError(t, err)

			rng := rand.New(rand.NewSource(seed + 300))
			at := time.Unix(1_700_000_000, 0)
			runs := []string{"run-a", "run-b"}
			seq := map[string]uint64{}

			appendSteps := func(n int) {
				for step := 0; step < n; step++ {
					at = at.Add(time.Second)
					run := runs[rng.Intn(len(runs))]
					seq[run]++
					fact := factAt(factItem, at, itemFact{
						RunID: run, Seq: seq[run],
						Kind:  kinds[rng.Intn(len(kinds))],
						Chars: rng.Intn(80),
					})
					require.NoError(t, store.Exec(func() ([]Fact, error) { return []Fact{fact}, nil }))
				}
			}

			// First half: the sequence a checkpoint would cover.
			appendSteps(30)
			offset, err := store.Offset()
			require.NoError(t, err)
			snap := itemsF.snapshot(offset)

			// Second half: the tail a snapshot-aware restart must still pick up.
			appendSteps(30)

			project := func(f *itemsFold) map[string]any {
				out := map[string]any{}
				for _, run := range runs {
					out[run+"/counts"] = f.countsFor(run)
					out[run+"/chars"] = f.chars[run]
					out[run+"/maxSeq"] = f.maxSeq[run]
				}
				return out
			}
			want := project(itemsF)
			require.NoError(t, store.Close())

			// Full replay from 0 (the existing, always-correct path).
			fullF := newItemsFold()
			fullStore, err := openStore(path, fullF)
			require.NoError(t, err)
			assert.Equal(t, want, project(fullF), "sanity: full replay from 0 must match the live fold")
			require.NoError(t, fullStore.Close())

			// Snapshot-restore + tail replay (the D4 shortcut).
			restoredF := newItemsFold()
			restoredF.restore(snap)
			tailStore, err := openStoreFromOffset(path, snap.Offset, restoredF)
			require.NoError(t, err)
			assert.Equal(t, want, project(restoredF), "restore(snapshot) + tail replay must equal a full replay from 0")
			require.NoError(t, tailStore.Close())
		})
	}
}

// TestOpenStoreFromOffset_StaleOffsetFallsBackToFullReplay pins the safety
// property that makes the snapshot purely additive: an offset that no
// longer makes sense against the file on disk (past EOF — a stale snapshot
// from a since-shrunk/rewritten journal, or simply corrupt) is NEVER
// trusted; replay silently falls back to byte 0, so a bad snapshot costs
// performance, never correctness.
func TestOpenStoreFromOffset_StaleOffsetFallsBackToFullReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "items.jsonl")
	itemsF := newItemsFold()
	store, err := openStore(path, itemsF)
	require.NoError(t, err)
	for i := 1; i <= 5; i++ {
		fact := factAt(factItem, time.Unix(1_700_000_000+int64(i), 0), itemFact{RunID: "run-a", Seq: uint64(i), Kind: "run_started"})
		require.NoError(t, store.Exec(func() ([]Fact, error) { return []Fact{fact}, nil }))
	}
	require.NoError(t, store.Close())

	fi, err := os.Stat(path)
	require.NoError(t, err)

	staleF := newItemsFold()
	staleStore, err := openStoreFromOffset(path, fi.Size()+1_000_000, staleF)
	require.NoError(t, err)
	defer staleStore.Close()
	assert.Equal(t, 5, staleF.countsFor("run-a")["run_started"],
		"an out-of-range offset must fall back to a full replay from 0, not silently under-count")
}

// TestWriteItemsSnapshot_RoundTrips drives the production write/load path
// (checkpoint.go) end to end: a SCOPE_CHECKPOINT agent_report triggers a
// snapshot write, and loadItemsSnapshot reads back exactly the state the
// live fold held at that instant.
func TestWriteItemsSnapshot_RoundTrips(t *testing.T) {
	resetStrictness(t)
	sp := startRunSpawner(nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	c.recordSummary(out.Harp, "run-1", 1, &agentcoordpb.Summary{
		Scope:            agentcoordpb.Summary_SCOPE_CHECKPOINT,
		Text:             "checkpoint",
		CoversThroughSeq: 1,
	})

	snap, ok := loadItemsSnapshot(c.stateDir)
	require.True(t, ok, "a SCOPE_CHECKPOINT report must produce a loadable snapshot file")

	var live itemsSnapshot
	offset, err := c.items.Offset()
	require.NoError(t, err)
	c.items.View(func() { live = c.itemsF.snapshot(offset) })

	assert.Equal(t, live.MaxSeq, snap.MaxSeq)
	assert.Equal(t, live.Counts, snap.Counts)
	assert.Equal(t, offset, snap.Offset)
}
