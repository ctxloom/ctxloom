package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flushItems detached ch.items ("facts := ch.items; ch.items =
// nil") BEFORE attempting the journal append, and on an Exec failure just
// warned and returned — the facts existed in no buffer and no journal,
// permanently lost. The comment claimed "the runner keeps the events unacked
// and re-emits them on reconnect", but that only holds if the CHANNEL dies;
// on a live channel, a LATER successful flushItems call reads a fresh
// `target := ch.ackSeq` (which climbs independently, in handleAgentEvent, on
// every event regardless of journal success) and advances flushedSeq/
// ackThrough straight past the lost seqs — falsely certifying durability for
// facts that were never written.
//
// A closed items journal (the same deterministic failure shape
// mailbox_takefail_test.go and journal_offsetview_test.go already use) pins
// it: after a failed flush, the facts must be back in ch.items (so the next
// attempt retries them) and flushedSeq must NOT have advanced.
func TestFlushItems_JournalFailure_RestoresFactsAndDoesNotAdvanceAck(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	ch := &runChan{role: "child-a", ackSeq: 5}
	fact := factAt(factItem, time.Now(), itemFact{RunID: "run-a", Seq: 1, Kind: "message_delta", Chars: 3})
	ch.items = []Fact{fact}

	require.NoError(t, c.items.Close(), "break the append path the way a shutdown/disk failure does")

	c.flushItems(ch)

	assert.Zero(t, ch.flushedSeq,
		"flushedSeq must NOT advance past a target whose facts failed to journal — that would certify "+
			"durability for events that were never written")
	require.Len(t, ch.items, 1,
		"a fact that failed to journal must be restored to the buffer so the next flush retries it, not dropped")
	assert.Equal(t, fact, ch.items[0])
}

// TestFlushItems_JournalFailure_PrependsAheadOfNewerBuffered: facts
// accumulated (bufferItem) WHILE the failed flush's own goroutine was still
// unscheduled must land AFTER the restored (older) facts, preserving order.
func TestFlushItems_JournalFailure_PrependsAheadOfNewerBuffered(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	ch := &runChan{role: "child-a", ackSeq: 5}
	older := factAt(factItem, time.Now(), itemFact{RunID: "run-a", Seq: 1, Kind: "message_delta", Chars: 3})
	newer := factAt(factItem, time.Now(), itemFact{RunID: "run-a", Seq: 2, Kind: "message_delta", Chars: 4})
	ch.items = []Fact{older}

	require.NoError(t, c.items.Close())

	c.flushItems(ch)
	require.Len(t, ch.items, 1)

	// Simulate a newer event buffering in while the failure was being
	// handled (bufferItem's own append, done here directly since the
	// journal is closed).
	ch.items = append(ch.items, newer)

	require.Len(t, ch.items, 2)
	assert.Equal(t, older, ch.items[0], "the restored (older) fact must stay ahead of anything buffered after it")
	assert.Equal(t, newer, ch.items[1])
}
