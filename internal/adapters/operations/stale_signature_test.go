package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- (a) invalidate at the cause --------------------------------------------

// --- (b) defend at the boundary ---------------------------------------------

// Export must not copy a signature that does not cover the bytes it ships with:
// that is precisely the broken pair every consumer raises a tamper alarm on.
func TestExportBundle_StaleSignature_Refuses(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	dir := stageSignedTreeOn(t, fs, authoredV1(cfg.GetAppPaths()[0]), "stale", testSigner(t))
	staleTree(t, fs, dir)

	_, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "stale", DestDir: "/out", FS: fs})
	require.ErrorIs(t, err, ErrStaleSignature)
	assert.Contains(t, err.Error(), "ctxloom bundle sign stale")

	exists, _ := afero.DirExists(fs, "/out/stale")
	assert.False(t, exists, "a refused export must not leave a half-exported copy")
}

// A move to a local path rides ExportBundle, so it inherits the refusal — and,
// critically, must leave the source untouched.
func TestMoveBundle_ToLocalPath_StaleSignature_RefusesAndKeepsSource(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	require.NoError(t, fs.MkdirAll("/out", 0755))
	dir := stageSignedTreeOn(t, fs, authoredV1(cfg.GetAppPaths()[0]), "stale", testSigner(t))
	staleTree(t, fs, dir)

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "stale", To: "/out", FS: fs})
	require.ErrorIs(t, err, ErrStaleSignature)

	exists, _ := afero.DirExists(fs, dir)
	assert.True(t, exists, "a refused move must leave the source in place")
}

// A move to a REMOTE carries the .sig verbatim into publish. A stale one must be
// refused before anything is published or the source removed.
func TestMoveBundle_ToRemote_StaleSignature_RefusesAndPublishesNothing(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "nope"}
	cfg, _, mgr := pushTestSetup(t, mock)
	dir := stageSignedTreeOn(t, afero.NewOsFs(), authoredV1(cfg.GetAppPaths()[0]), "stale", testSigner(t))
	staleTree(t, afero.NewOsFs(), dir)

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{
		Name: "stale", To: "personal", PublishManager: mgr,
	})
	require.ErrorIs(t, err, ErrStaleSignature)

	assert.Empty(t, mock.createOrUpdateCalls, "nothing may be published")
	assert.DirExists(t, dir, "a refused move must leave the source intact")
}
