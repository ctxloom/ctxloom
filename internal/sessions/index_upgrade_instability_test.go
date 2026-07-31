package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeUnparseableIndex writes an index whose started_at matches none of
// tsLayouts, and asserts the fixture is hostile from the upgrade pipeline's own
// vantage point before any caller relies on it: if the value happened to parse,
// every assertion downstream would be vacuous.
func writeUnparseableIndex(t *testing.T, harpName string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.yaml")
	body := "sessions:\n" +
		"  - harp_name: " + harpName + "\n" +
		"    project_dir: /proj\n" +
		"    started_at: not-a-timestamp\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, ok := parseTimestamp("not-a-timestamp")
	require.False(t, ok, "the fixture timestamp must be genuinely unparseable")
	return path
}

// TestUpgrade_UnparseableTimestampIsUnstableAcrossLoads characterizes the
// behaviour U099-F09 names. It is NOT a fix: the value chosen for an
// unparseable timestamp is a migration decision guarded by a deliberately named
// pin (TestNormalizeTimestampNode_UnparseableDegradesToRecent) and an in-code
// rationale, so changing it is the human's call. This records what the current
// choice actually costs, so the decision is made against a measurement.
//
// The degrade is time.Now(), and loadLocked runs the upgrade pipeline on EVERY
// load without persisting, so the same on-disk bytes decode to a different
// StartedAt every single read.
func TestUpgrade_UnparseableTimestampIsUnstableAcrossLoads(t *testing.T) {
	path := writeUnparseableIndex(t, "amber-quiet-kite")

	read := func() time.Time {
		m, err := Open(path)
		require.NoError(t, err)
		idx, err := m.Load()
		require.NoError(t, err)
		require.Len(t, idx.Sessions, 1)
		return idx.Sessions[0].StartedAt
	}

	first := read()
	require.False(t, first.IsZero(), "the degrade must not be the zero time")
	time.Sleep(2 * time.Millisecond)
	second := read()

	require.NotEqual(t, first, second,
		"the same on-disk index decodes to a different StartedAt on every load")
	require.True(t, second.After(first), "each load stamps a fresher fabricated time")
}

// TestUpgrade_UnparseableTimestampIsPersistedByAnOrdinaryMutation records the
// sharper half. loadLocked's comment says the in-memory upgrade is persisted
// only after the user confirms, via CommitUpgrade, "so a read-only command
// never silently rewrites the index". That holds for READS. Any ordinary
// MUTATION re-saves the whole in-memory index, so the fabricated timestamp is
// written to disk with no prompt and no PendingUpgrade consent -- and which
// fabricated value gets frozen in depends on which process happened to write
// next.
func TestUpgrade_UnparseableTimestampIsPersistedByAnOrdinaryMutation(t *testing.T) {
	const harpName = "amber-quiet-kite"
	path := writeUnparseableIndex(t, harpName)

	m, err := Open(path)
	require.NoError(t, err)

	// An ordinary bind: nothing about it asks to migrate anything.
	require.NoError(t, m.BindSession(harpName, "sess-1", ""))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "not-a-timestamp",
		"the unparseable value was rewritten on disk by a plain BindSession")

	// And the fabricated replacement is a real, recent timestamp the user was
	// never asked about.
	m2, err := Open(path)
	require.NoError(t, err)
	idx, err := m2.Load()
	require.NoError(t, err)
	require.Len(t, idx.Sessions, 1)
	require.False(t, idx.Sessions[0].StartedAt.IsZero())
	require.Nil(t, m2.PendingUpgrade(),
		"after the silent rewrite there is nothing left to prompt about")
}
