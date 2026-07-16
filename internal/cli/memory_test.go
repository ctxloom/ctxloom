// `memory list --format json` feeds machine consumers the same session rows
// the text table shows, so the entry construction — status computed once from
// the distilled set, and every SessionMeta field carried through under its
// json tag — must hold.
package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestMemoryListEntries_MarksDistilledStatus(t *testing.T) {
	started := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	sessions := []agent.SessionMeta{
		{ID: "session-a", StartTime: started, EntryCount: 5},
		{ID: "session-b", StartTime: started, EntryCount: 2},
	}
	distilled := map[string]bool{"session-a": true}

	entries := memoryListEntries(sessions, distilled)

	require.Len(t, entries, 2)
	assert.Equal(t, "session-a", entries[0].ID)
	assert.Equal(t, "compacted", entries[0].Status)
	assert.Equal(t, "session-b", entries[1].ID)
	assert.Equal(t, "pending", entries[1].Status)
}

func TestMemoryListEntries_EmptySessionsYieldsEmptySlice(t *testing.T) {
	entries := memoryListEntries(nil, map[string]bool{})
	assert.Empty(t, entries)
}

// TestMemoryListSessionEntry_JSONRoundTrip proves SessionMeta's embedded
// fields carry real json tags (not Go's default capitalized field names) —
// the fix that makes `memory list --format json` machine-parseable at all.
func TestMemoryListSessionEntry_JSONRoundTrip(t *testing.T) {
	started := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	entry := memoryListSessionEntry{
		SessionMeta: agent.SessionMeta{ID: "abc123", StartTime: started, EntryCount: 7},
		Status:      "pending",
	}

	b, err := json.Marshal(entry)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.Equal(t, "abc123", got["id"])
	assert.Equal(t, float64(7), got["entry_count"])
	assert.Equal(t, "pending", got["status"])
	assert.Contains(t, got, "start_time")
	// Path is a zero-valued string tagged omitempty — it must not appear at
	// all. (EndTime is a struct; encoding/json's omitempty never elides a
	// zero-valued struct, so it still rides as the zero time.)
	assert.NotContains(t, got, "path")
}
