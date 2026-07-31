package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLocateTranscript_NewestJSONWins characterizes the .json arm's
// newest-wins comparison, the one arm of LocateTranscript's walk callback that
// had no test of its own: the existing cases cover the .jsonl arm with two
// candidates and the .json arm with only one, so the .json replacement branch
// was never exercised.
//
// Added BEFORE the callback's complexity reduction, since behaviour is
// unchanged by definition there and no test can discriminate a correct
// extraction from an incorrect one unless it already covers the arm.
func TestLocateTranscript_NewestJSONWins(t *testing.T) {
	testsupport.Isolate(t)
	const harpName = "swift-amber-falcon"

	store, err := paths.HarpTranscriptStoreDir(harpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(store, "a"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(store, "b"), 0o755))

	older := filepath.Join(store, "a", "old.json")
	newer := filepath.Join(store, "b", "new.json")
	require.NoError(t, os.WriteFile(older, []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(newer, []byte("{}"), 0o644))

	base := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(older, base, base))
	require.NoError(t, os.Chtimes(newer, base.Add(30*time.Minute), base.Add(30*time.Minute)))

	// The fixture must be hostile: the two candidates must really differ in
	// mtime, or "newest wins" is untested whichever file comes back.
	oi, err := os.Stat(older)
	require.NoError(t, err)
	ni, err := os.Stat(newer)
	require.NoError(t, err)
	require.True(t, ni.ModTime().After(oi.ModTime()), "the fixture mtimes must differ")

	got, ok := LocateTranscript(harpName)
	require.True(t, ok)
	require.Equal(t, newer, got, "with no .jsonl present, the newest .json wins")
}
