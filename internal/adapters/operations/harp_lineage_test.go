package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nativeLog writes one conversation log under harp's native history, stamping
// its mtime.
func nativeLog(t *testing.T, harp, rel string, age time.Duration) string {
	t.Helper()
	native, err := paths.HarpNativeDir(harp)
	require.NoError(t, err)
	p := filepath.Join(native, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o600))
	require.NoError(t, os.Chtimes(p, time.Now().Add(-age), time.Now().Add(-age)))
	return p
}

func TestHarpTranscripts_NewestFirstWithTheIDFromTheFileName(t *testing.T) {
	testsupport.Isolate(t)
	nativeLog(t, "some-harp", "claude/projects/-proj/9847366d-9f1a-4a34-b2c2-de0000000000.jsonl", 2*time.Hour)
	nativeLog(t, "some-harp", "claude/projects/-proj/fc735796-6258-4354-a1b0-cc0000000000.jsonl", 24*time.Hour)
	newest := nativeLog(t, "some-harp", "claude/projects/-proj/0b1d5db0-37fd-4df4-b733-91f000000000.jsonl", time.Minute)

	got, err := HarpTranscripts("some-harp")
	require.NoError(t, err)
	require.Len(t, got, 3, "every conversation log is one lineage entry")
	assert.Equal(t, []string{
		"0b1d5db0-37fd-4df4-b733-91f000000000",
		"9847366d-9f1a-4a34-b2c2-de0000000000",
		"fc735796-6258-4354-a1b0-cc0000000000",
	}, []string{got[0].SessionID, got[1].SessionID, got[2].SessionID}, "newest first")
	assert.Equal(t, newest, got[0].Path)
}

// A subagent's interior log and a non-log file are not conversations of the
// session.
func TestHarpTranscripts_SkipsSubagentInteriorsAndNonLogs(t *testing.T) {
	testsupport.Isolate(t)
	live := nativeLog(t, "some-harp", "claude/projects/-proj/live-session.jsonl", time.Minute)
	nativeLog(t, "some-harp", "claude/projects/-proj/live-session/subagents/agent-1.jsonl", time.Second)
	nativeLog(t, "some-harp", "claude/projects/-proj/memory/MEMORY.md", time.Second)

	got, err := HarpTranscripts("some-harp")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, live, got[0].Path)
}

func TestHarpTranscripts_MissingNativeDirIsNotAFault(t *testing.T) {
	testsupport.Isolate(t)
	got, err := HarpTranscripts("never-existed")
	require.NoError(t, err, "a harp that has authored nothing is not an error")
	assert.Empty(t, got)
}
