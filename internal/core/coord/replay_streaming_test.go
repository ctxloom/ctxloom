package coord

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heapSamplingFold records the live heap at the first fact it is handed, which
// is a point inside replay: whatever replay is holding is live at that moment.
type heapSamplingFold struct {
	applied  int
	peakLive uint64
}

func (f *heapSamplingFold) apply(Fact) {
	f.applied++
	if f.applied == 1 {
		runtime.GC() // collect the write path's garbage; what remains is retained
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		f.peakLive = ms.HeapAlloc
	}
}

// TestReplay_DoesNotHoldTheWholeJournalInMemory: replay slurped the journal
// with io.ReadAll, so recovery cost memory proportional to the FILE, not to a
// line. That is the opposite of what the checkpoint offset exists for — and the
// checkpoint's own documented degraded paths (missing, stale, or out-of-range
// offset) all fall back to replaying from byte 0, which is exactly when the
// journal is largest.
func TestReplay_DoesNotHoldTheWholeJournalInMemory(t *testing.T) {
	const (
		lineSize  = 64 << 10
		lineCount = 256 // ~16 MiB of journal
	)
	path := filepath.Join(t.TempDir(), "items.jsonl")
	writeFatJournal(t, path, lineCount, lineSize)

	fold := &heapSamplingFold{}
	s, err := openStore(path, fold)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.Equal(t, lineCount, fold.applied, "every fact must still be applied")
	assert.Less(t, fold.peakLive, uint64(lineCount*lineSize/2),
		"replay retained %d bytes against a %d-byte journal: it is holding the file, not a line",
		fold.peakLive, lineCount*lineSize)
}

// TestReplay_TornTailIsStillTruncated: the streaming read must keep replay's
// three documented behaviours. A final line with no newline is a crash
// mid-append: truncate it away and keep everything before it.
func TestReplay_TornTailIsStillTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	good := mustFactLine(t, "one")
	require.NoError(t, os.WriteFile(path, []byte(good+`{"kind":"item","at":`), 0o600))

	fold := &countingFold{}
	s, err := openStore(path, fold)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	assert.Equal(t, 1, fold.n, "the intact line survives")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, good, string(raw), "the torn tail must be truncated off the file")
}

// TestReplay_UnparseableTailIsTruncated: a final line that is complete but does
// not parse is the same crash shape (a partial write that happened to land a
// newline) and truncates too.
func TestReplay_UnparseableTailIsTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	good := mustFactLine(t, "one")
	require.NoError(t, os.WriteFile(path, []byte(good+"not json at all\n"), 0o600))

	fold := &countingFold{}
	s, err := openStore(path, fold)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	assert.Equal(t, 1, fold.n)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, good, string(raw))
}

// TestReplay_CorruptionMidJournalFailsLoudly: an unparseable line that is NOT
// the tail is corruption, never a torn append — it must refuse to open rather
// than project a journal it cannot read.
func TestReplay_CorruptionMidJournalFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	body := mustFactLine(t, "one") + "not json at all\n" + mustFactLine(t, "three")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	_, err := openStore(path, &countingFold{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corrupt")
}

// TestReplay_AppendsLandAfterTheReplayedTail: replay leaves the write offset at
// the end of the good region, so the first append after it does not overwrite
// or leave a hole.
func TestReplay_AppendsLandAfterTheReplayedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(mustFactLine(t, "one")+mustFactLine(t, "two")), 0o600))

	fold := &countingFold{}
	s, err := openStore(path, fold)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.Equal(t, 2, fold.n)

	require.NoError(t, s.Exec(func() ([]Fact, error) { return []Fact{{Kind: factItem}}, nil }))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(raw), "\n"), "the append must extend the journal, not overwrite it")
}

// countingFold counts every fact replay or Exec hands it.
type countingFold struct{ n int }

func (f *countingFold) apply(Fact) { f.n++ }

// mustFactLine renders one journal line carrying tag as its payload.
func mustFactLine(t *testing.T, tag string) string {
	t.Helper()
	raw, err := json.Marshal(Fact{Kind: factItem, Data: json.RawMessage(fmt.Sprintf(`{"run_id":%q}`, tag))})
	require.NoError(t, err)
	return string(raw) + "\n"
}

// writeFatJournal writes count lines of roughly size bytes each.
func writeFatJournal(t *testing.T, path string, count, size int) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	filler := strings.Repeat("x", size)
	for i := range count {
		raw, merr := json.Marshal(Fact{Kind: factItem, Data: json.RawMessage(fmt.Sprintf(`{"run_id":"run-%d","pad":%q}`, i, filler))})
		require.NoError(t, merr)
		_, werr := f.Write(append(raw, '\n'))
		require.NoError(t, werr)
	}
}
