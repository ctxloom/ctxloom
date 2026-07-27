package coord

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// U019-F01: checkpoint.go's writeItemsSnapshot used to read the journal's
// offset (Offset()) and the fold's state (View()) as TWO separate locked
// windows. A concurrent Exec landing between them lands in the gap: the read
// observes fold state NEWER than the recorded offset, so restore(snapshot) +
// a tail replay from that offset later re-applies the facts that landed in
// the gap on top of a fold that already counted them (checkpoint.go,
// journal.go).
//
// TestStore_OffsetViewIsAtomicWithConcurrentExec pins the fix directly: a
// concurrent Exec must not be able to complete WHILE an OffsetView read
// callback is running, because that is exactly the window the old two-call
// pattern left open. It requires OffsetView (journal.go) to exist and to
// hold the journal's lock for the read callback's whole duration — run with
// -race (as `just test-pkg` always does) to also catch a data race directly.
func TestStore_OffsetViewIsAtomicWithConcurrentExec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "items.jsonl")
	itemsF := newItemsFold()
	store, err := openStore(path, itemsF)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factItem, time.Unix(1_700_000_000, 0), itemFact{RunID: "run-a", Seq: 1, Kind: "run_started"})}, nil
	}))

	// The concurrent Exec must only be ATTEMPTED once we are already inside
	// OffsetView's read callback (i.e. once the RLock is already held) —
	// otherwise it can race ahead and complete before the read window even
	// opens, which would prove nothing about atomicity.
	readStarted := make(chan struct{})
	execDone := make(chan struct{})
	go func() {
		<-readStarted
		// This Exec must block (inside execLocked's s.mu.Lock()) for as
		// long as OffsetView below holds the RLock for its read callback.
		_ = store.Exec(func() ([]Fact, error) {
			return []Fact{factAt(factItem, time.Unix(1_700_000_001, 0), itemFact{RunID: "run-a", Seq: 2, Kind: "run_started"})}, nil
		})
		close(execDone)
	}()

	var offset int64
	var countAtOffset int
	_, err = store.OffsetView(func(o int64) {
		offset = o
		close(readStarted) // only now may the concurrent Exec attempt to run
		// The whole point: for as long as this callback runs, the
		// concurrent Exec above must NOT have completed — proving Offset
		// and the read are one atomic window, not two.
		time.Sleep(30 * time.Millisecond)
		select {
		case <-execDone:
			t.Error("a concurrent Exec completed WHILE OffsetView's read callback was still running — " +
				"not atomic, the exact TOCTOU gap U019-F01 exploited")
		default:
		}
		countAtOffset = itemsF.countsFor("run-a")["run_started"]
	})
	require.NoError(t, err)
	<-execDone // let the background Exec finish before Close()

	// Since the window was exclusive, the state read inside it can only be
	// the ONE fact that existed before OffsetView was called — never the
	// concurrent second one, and never a torn mix of "new state, old offset".
	require.Equal(t, 1, countAtOffset,
		"the read inside OffsetView must reflect exactly the state as of the returned offset")
	require.Greater(t, offset, int64(0))
}
