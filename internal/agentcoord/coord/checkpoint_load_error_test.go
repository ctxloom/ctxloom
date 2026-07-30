package coord

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// U019-F11 — "no checkpoint yet" and "the checkpoint is there but I could
// not read it" are DIFFERENT facts, and only one of them is normal.
//
// A missing snapshot is the ordinary first-boot case and must stay silent:
// warning on it would fire on every fresh state dir and train the operator to
// ignore the channel. Any OTHER read failure — EISDIR, EACCES, EIO — means a
// checkpoint that exists is being silently ignored, and the coordinator
// quietly falls back to a full replay of items.jsonl forever. The compaction
// is a performance contract, so the failure has no functional symptom to
// notice: it just gets slower, permanently, with zero diagnostic. Same class
// as the corrupt-JSON arm right below it, which already warns.
func TestLoadItemsSnapshot_UnreadableWarns(t *testing.T) {
	dir := t.TempDir()
	// A directory where the snapshot file belongs is the deterministic,
	// non-root-dependent way to make os.ReadFile fail with something that is
	// not "not exist" (a chmod-based EACCES is a no-op when tests run as root).
	require.NoError(t, os.MkdirAll(itemsSnapshotPath(dir), 0o700))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	snap, ok := loadItemsSnapshot(dir)
	assert.False(t, ok, "an unreadable snapshot must still fall back to a full replay")
	assert.Equal(t, itemsSnapshot{}, snap)
	assert.Contains(t, buf.String(), "warning:",
		"an unreadable checkpoint must reach the diagnostic channel, not be filed as 'no checkpoint yet'")
	assert.Contains(t, buf.String(), itemsSnapshotFileName,
		"the warning must name the file it could not read")
}

// The absent-file case is the normal one and stays silent — pinned so the
// fix above cannot be over-applied into a warning on every fresh state dir.
func TestLoadItemsSnapshot_MissingIsSilent(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	snap, ok := loadItemsSnapshot(dir)
	assert.False(t, ok)
	assert.Equal(t, itemsSnapshot{}, snap)
	assert.Empty(t, buf.String(), "a first boot with no checkpoint must not warn")
}

// A present-but-corrupt snapshot already warned before this row; pinned here
// so the not-exist split does not accidentally re-silence it.
func TestLoadItemsSnapshot_CorruptWarns(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, itemsSnapshotFileName), []byte("{not json"), 0o600))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	_, ok := loadItemsSnapshot(dir)
	assert.False(t, ok)
	assert.Contains(t, buf.String(), "warning:")
}
