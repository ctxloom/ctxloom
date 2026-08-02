package coord

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The manifest is fsynced; the blob it names must be too.
//
// writeAtomic fsyncs the temp file's CONTENT, then renames. The rename makes
// the blob visible but not durable: nothing fsyncs the directory entry. The
// manifest that references the blob, meanwhile, goes through the journal,
// which fsyncs before its own response returns. So the durable reference
// could outlive its referent across a crash — a manifest naming a blob whose
// rename never landed, answering NOT_FOUND for the rest of its life.
//
// A crash cannot be staged in a unit test, so this pins the seam instead:
// that the publish path calls the directory sync at all, that it does not
// call it on the free-dedupe path (nothing new was named), and that a
// failure fails the upload rather than issuing a durability-asserting
// receipt for something that is not durable.
func TestArtifactStore_PublishSyncsTheDirectory(t *testing.T) {
	var synced []string
	swapArtifactDirSync(t, func(dir string) error {
		synced = append(synced, dir)
		return nil
	})

	st, err := newArtifactStore(t.TempDir())
	require.NoError(t, err)

	shaHex, _, err := st.writeAtomic(strings.NewReader("durable please"), nil, 0)
	require.NoError(t, err)
	require.Equal(t, []string{st.dir}, synced, "publishing a new blob must fsync the store directory")

	// The dedupe path renames nothing, so there is no new directory entry to
	// make durable — syncing again would be pure cost on every re-upload.
	again, _, err := st.writeAtomic(strings.NewReader("durable please"), nil, 0)
	require.NoError(t, err)
	assert.Equal(t, shaHex, again)
	assert.Equal(t, []string{st.dir}, synced, "a dedupe hit publishes no new name and must not fsync again")
}

func TestArtifactStore_DirSyncFailureFailsTheUpload(t *testing.T) {
	boom := errors.New("device is on fire")
	swapArtifactDirSync(t, func(string) error { return boom })

	st, err := newArtifactStore(t.TempDir())
	require.NoError(t, err)

	_, _, err = st.writeAtomic(strings.NewReader("content"), nil, 0)
	require.Error(t, err, "a receipt asserts durability; it must not be issued when the fsync failed")
	assert.ErrorIs(t, err, boom)
}

// swapArtifactDirSync installs a stand-in for the directory fsync for the
// duration of one test.
func swapArtifactDirSync(t *testing.T, fn func(string) error) {
	t.Helper()
	prev := syncArtifactDir
	syncArtifactDir = fn
	t.Cleanup(func() { syncArtifactDir = prev })
}
