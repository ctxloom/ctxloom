package sessions

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SetSummary(harp, "", nil, 0) once succeeded and ERASED an existing good
// summary, its detail lines and its staleness fingerprint — the destructive
// variant of the empty-input bug. The only production caller guards against it
// at the call site; the writer itself must not accept the erasure.
func TestSetSummary_EmptySummaryDoesNotEraseAGoodOne(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m.SetSummary(e.HarpName, "a real distilled summary", []string{"open item"}, 4096))

	err = m.SetSummary(e.HarpName, "", nil, 0)
	assert.Error(t, err, "an empty summary must be refused, not written over a good one")

	got, _ := m.Find(e.HarpName)
	require.NotNil(t, got)
	assert.Equal(t, "a real distilled summary", got.Summary, "the good summary must survive")
	assert.Equal(t, []string{"open item"}, got.Detail)
	assert.Equal(t, int64(4096), got.SourceSize)
}

// saveLocked(nil) would marshal to the literal `null` and atomically
// overwrite the whole index with it — a total silent wipe. No caller passes
// nil today; the writer must still refuse rather than destroy the file.
func TestSaveLocked_NilIndexIsRefused(t *testing.T) {
	m := newManager(t)
	_, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	before, err := os.ReadFile(m.path)
	require.NoError(t, err)

	assert.Error(t, m.saveLocked(nil), "writing a nil index must be refused, not marshalled to `null`")

	after, err := os.ReadFile(m.path)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the index file must be untouched")
}
