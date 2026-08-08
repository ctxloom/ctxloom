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
	lines, err := readJSONLLines(strings.NewReader(in))
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, `{"a":1}`, string(lines[0]))
	assert.Equal(t, `{"b":2}`, string(lines[1]))
}

// TestReadJSONLLines_NoTrailingNewline pins the antigravity-shaped case: a
// file whose last line has no trailing '\n' at all must still surface that
// final line, not silently drop it — this is exactly the behavior the old
// os.ReadFile+bytes.Split antigravity copy and the bufio-based codex/claude
// copy both had to get right independently; now there's one implementation
// to get it right once.
func TestReadJSONLLines_NoTrailingNewline(t *testing.T) {
	lines, err := readJSONLLines(strings.NewReader(`{"only":"line"}`))
	require.NoError(t, err)
	require.Len(t, lines, 1)
	assert.Equal(t, `{"only":"line"}`, string(lines[0]))
}

func TestReadJSONLLines_Empty(t *testing.T) {
	lines, err := readJSONLLines(strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, lines)
}

// TestReadJSONLLines_LongLine pins the whole reason this reads via an
// unbounded bufio.Reader instead of a capped bufio.Scanner: a single line
// longer than a Scanner's default 64KiB token cap must still come through
// whole, never truncated or dropped.
func TestReadJSONLLines_LongLine(t *testing.T) {
	long := strings.Repeat("x", 5*1024*1024)
	lines, err := readJSONLLines(strings.NewReader(long + "\n"))
	require.NoError(t, err)
	require.Len(t, lines, 1)
	assert.Len(t, lines[0], len(long))
}

func TestOpenAndReadJSONLLines_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"a\":1}\n{\"b\":2}\n"), 0o644))

	lines, err := OpenAndReadJSONLLines("codex", path)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, `{"a":1}`, string(lines[0]))
}

func TestOpenAndReadJSONLLines_OpenFailureWrapsVendorPrefix(t *testing.T) {
	_, err := OpenAndReadJSONLLines("claude", filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude: open")
}
