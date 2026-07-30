package coord

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// syncBuf is a concurrency-safe clidiag sink: coordinator teardown runs
// alongside tracked goroutines that may warn on their own.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestCoordinatorClose_SurfacesJournalCloseFailures pins that a journal that
// fails to close is REPORTED. Store.Close closes the journal's file handle, so
// this is the last moment an ENOSPC/EIO on the final flush can be observed at
// all — and the coordinator's own durability claim ("fsynced before return")
// is exactly what such a failure retracts. Discarded with `_ =`, a full disk
// looked identical to a clean shutdown.
//
// Closing the handle out from under the store is the deterministic shape of
// that failure, and a real one: a state dir yanked away mid-session.
func TestCoordinatorClose_SurfacesJournalCloseFailures(t *testing.T) {
	sink := &syncBuf{}
	restore := clidiag.SetSink(sink)
	defer restore()

	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	// Break two of the four journals' handles.
	assert.NoError(t, c.runs.f.Close())
	assert.NoError(t, c.items.f.Close())

	c.Close()

	out := sink.String()
	assert.Contains(t, out, "runs.jsonl", "the failing journal must be named")
	assert.Contains(t, out, "items.jsonl", "every failing journal must be named, not just the first")
	assert.Contains(t, out, "file already closed")
	assert.NotContains(t, out, "mailbox.jsonl", "a journal that closed cleanly must not be reported")
}
