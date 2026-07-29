package tasks

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddTriggerGuardParity pins the Store-level and log-level add paths to
// the same verdict on every (text, status, trigger) shape that decides the
// Deferred invariant. The log-level guard is the authoritative one — it also
// covers eventLog's own callers and it is where the lock-protected write
// happens — so the Store entry points must not answer differently.
func TestAddTriggerGuardParity(t *testing.T) {
	for _, tc := range []struct{ text, status, trigger string }{
		{"t", "", ""},
		{"t", StatusToDo, ""},
		{"t", StatusDeferred, ""},
		{"t", StatusDeferred, "   "},
		{"t", StatusDeferred, "v2 ships"},
		{"", StatusDeferred, ""},
		{"   ", StatusDeferred, ""},
		{"", StatusToDo, ""},
	} {
		viaStore, errStore := newStore(t).AddWithTrigger(tc.text, tc.status, tc.trigger)
		viaLog, errLog := newStore(t).log.addWithTags(tc.text, tc.status, tc.trigger, nil)

		if (errStore == nil) != (errLog == nil) {
			t.Errorf("%+v: Store err=%v, log err=%v", tc, errStore, errLog)
			continue
		}
		if errStore != nil {
			assert.Equal(t, errLog.Error(), errStore.Error(), "%+v", tc)
			continue
		}
		assert.Equal(t, viaLog.Status, viaStore.Status, "%+v", tc)
		assert.Equal(t, viaLog.Trigger, viaStore.Trigger, "%+v", tc)
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenLog(filepath.Join(t.TempDir(), "tasks.jsonl"), "swift-amber-falcon")
	require.NoError(t, err)
	return s
}
