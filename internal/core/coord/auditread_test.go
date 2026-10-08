package coord

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditEntry is c.audit's durable, no-projection payload (facts.go).
type auditEntry struct {
	Kind   string            `json:"kind"`
	Actor  string            `json:"actor,omitempty"`
	Detail map[string]string `json:"detail,omitempty"`
}

// readAuditKind reads interactions.jsonl straight off disk and returns every
// audit entry of the given kind, oldest first. The audit journal has no
// fold/query API by design (facts.go: "an audit log with no projection"), so
// reading the file IS the only way to assert what was recorded.
func readAuditKind(t *testing.T, c *Coordinator, kind string) []auditEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.stateDir, "interactions.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []auditEntry
	for _, line := range completeJournalLines(raw) {
		var f Fact
		require.NoError(t, json.Unmarshal(line, &f))
		if f.Kind != "interaction" {
			continue
		}
		var e auditEntry
		require.NoError(t, json.Unmarshal(f.Data, &e))
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// completeJournalLines returns the newline-terminated lines of a journal read
// off disk. A reader of a LIVE journal is not serialised with its writer
// (Store.execLocked), so its read can land inside an append: the final line
// then has no newline yet. That is the next fact, still being written, and is
// dropped here exactly as Store.replay drops a torn tail. Every complete line
// is returned for the caller to parse; one that does not parse is corruption.
func completeJournalLines(raw []byte) [][]byte {
	end := bytes.LastIndexByte(raw, '\n')
	var out [][]byte
	for _, line := range bytes.Split(raw[:end+1], []byte("\n")) {
		if len(bytes.TrimSpace(line)) != 0 {
			out = append(out, line)
		}
	}
	return out
}

// heldAppendFs lands the first half of the next journal append once armed,
// then holds the rest at a latch: the state a reader of a LIVE journal meets
// whenever its read lands inside an append (a write crossing a page boundary
// publishes the file's new size a page at a time).
type heldAppendFs struct {
	afero.Fs
	armed   atomic.Bool
	landed  chan struct{}
	release chan struct{}
}

func newHeldAppendFs(fs afero.Fs) *heldAppendFs {
	return &heldAppendFs{Fs: fs, landed: make(chan struct{}), release: make(chan struct{})}
}

func (h *heldAppendFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := h.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &heldAppendFile{File: f, fs: h}, nil
}

type heldAppendFile struct {
	afero.File
	fs *heldAppendFs
}

func (f *heldAppendFile) Write(p []byte) (int, error) {
	if !f.fs.armed.CompareAndSwap(true, false) {
		return f.File.Write(p)
	}
	n, err := f.File.Write(p[:len(p)/2])
	close(f.fs.landed)
	if err != nil {
		return n, err
	}
	<-f.fs.release
	m, err := f.File.Write(p[len(p)/2:])
	return n + m, err
}

// TestJournalReaders_AnAppendInFlightIsNotRead: the test helpers that read a
// LIVE journal off disk (readAuditKind for interactions.jsonl, readFacts under
// journaled for runs.jsonl) race the store's writer, which nothing in a
// running coordinator quiesces. A read landing inside an append sees a final
// line with no newline; it is the next fact, not yet written, and parsing it
// failed TestFinalReport_LateFinalFromAnEndedRunDoesNotEndTheResumedRun once
// under the full suite with "unexpected end of JSON input". The helpers must
// read what replay would keep: every complete line, never the torn tail.
func TestJournalReaders_AnAppendInFlightIsNotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "interactions.jsonl")
	held := newHeldAppendFs(afero.NewOsFs())
	s, err := openStore(held, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	c := &Coordinator{stateDir: dir, auditJ: s, now: time.Now, rep: termRep()}

	c.audit("final_report_end", "coord", map[string]string{"run_id": "run-a"})
	held.armed.Store(true)
	appended := make(chan struct{})
	go func() {
		defer close(appended)
		c.audit("final_report_end", "coord", map[string]string{"run_id": "run-b"})
	}()
	var once sync.Once
	releaseAppend := func() { once.Do(func() { close(held.release); <-appended }) }
	t.Cleanup(releaseAppend)
	<-held.landed

	got := readAuditKind(t, c, "final_report_end")
	require.Len(t, got, 1, "the half-written append is the next fact, not one to read")
	assert.Equal(t, "run-a", got[0].Detail["run_id"])
	assert.Len(t, readFacts(t, path), 1, "readFacts reads the same journal the same way")

	releaseAppend()
	got = readAuditKind(t, c, "final_report_end")
	require.Len(t, got, 2, "once the append completes it is read")
	assert.Equal(t, "run-b", got[1].Detail["run_id"])
}
