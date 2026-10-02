package vendorreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadJSONLLines_TrimsAndDropsEmpty(t *testing.T) {
	in := "  {\"a\":1}  \n\n\t\n{\"b\":2}\n"
	lines, err := ReadJSONLLines(strings.NewReader(in), 0)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, `{"a":1}`, string(lines[0].Bytes))
	assert.Equal(t, `{"b":2}`, string(lines[1].Bytes))
}

// TestReadJSONLLines_NoTrailingNewline pins the case a bufio.Reader loop gets
// wrong by default: a file whose last line has no trailing '\n' at all must
// still surface that final line, not silently drop it. There is one
// implementation to get this right, so this is the one place it is pinned.
func TestReadJSONLLines_NoTrailingNewline(t *testing.T) {
	lines, err := ReadJSONLLines(strings.NewReader(`{"only":"line"}`), 0)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	assert.Equal(t, `{"only":"line"}`, string(lines[0].Bytes))
}

func TestReadJSONLLines_Empty(t *testing.T) {
	lines, err := ReadJSONLLines(strings.NewReader(""), 0)
	require.NoError(t, err)
	assert.Empty(t, lines)
}

// TestReadJSONLLines_LongLine pins the whole reason this reads via an
// unbounded bufio.Reader instead of a capped bufio.Scanner: a single line
// longer than a Scanner's default 64KiB token cap must still come through
// whole, never truncated or dropped.
func TestReadJSONLLines_LongLine(t *testing.T) {
	long := strings.Repeat("x", 5*1024*1024)
	lines, err := ReadJSONLLines(strings.NewReader(long+"\n"), 0)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	assert.Len(t, lines[0].Bytes, len(long))
}

func TestOpenAndReadJSONLLines_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"a\":1}\n{\"b\":2}\n"), 0o644))

	lines, err := OpenAndReadJSONLLines("claude-code", path)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, `{"a":1}`, string(lines[0]))
}

func TestOpenAndReadJSONLLines_OpenFailureWrapsVendorPrefix(t *testing.T) {
	_, err := OpenAndReadJSONLLines("claude", filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude: open")
}

// TestReadJSONLLines_ReportsWhereEachLineEnds pins the offsets a resumed read
// depends on: Start/End are positions in the SOURCE (base-relative, counting
// the bytes trimming removed and the blank lines dropped), and only a line
// that reached its newline is Terminated — an unterminated last line may
// still be mid-write, so a checkpoint must never be taken past it.
func TestReadJSONLLines_ReportsWhereEachLineEnds(t *testing.T) {
	in := " {\"a\":1}\n\n{\"b\":2}\n{\"c\":"
	lines, err := ReadJSONLLines(strings.NewReader(in), 100)
	require.NoError(t, err)
	require.Len(t, lines, 3)

	assert.Equal(t, Line{Bytes: []byte(`{"a":1}`), Start: 100, End: 109, Terminated: true}, lines[0])
	assert.Equal(t, Line{Bytes: []byte(`{"b":2}`), Start: 110, End: 118, Terminated: true}, lines[1])
	assert.Equal(t, Line{Bytes: []byte(`{"c":`), Start: 118, End: 123, Terminated: false}, lines[2])
}
