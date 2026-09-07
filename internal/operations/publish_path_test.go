package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// THE REMOTE PATH IS ONE VALUE, NOT TWO AGREEING SPELLINGS.
//
// A push reports a destination (PushBundleResult.TargetPath, which the CLI
// prints and which operations.moveToRemote derives SigDest from) and writes to a
// destination (the path handed to Publisher.CreateOrUpdateFile). Those used to
// be computed independently — PushBundle spelled one expression,
// remote.preparePublish spelled the same one — with nothing binding them, so a
// change to either alone would have made `bundle push` report one path and
// publish to another, and `bundle move` would have deleted the local source
// after recording a SigDest that named a file nobody wrote.
//
// The tests below are that binding. They pass the reported path and the written
// path through the same assertion for BOTH bundle shapes, so the two can only
// stay equal by actually being the same value.

// writeDirFormBundleFixture hand-builds a directory-form bundle
// (`<name>/bundle.yaml` plus a skills subtree) and returns its manifest path.
// Nothing in operations creates one — CreateBundle only ever writes
// `<name>.yaml` — but the loader accepts the shape, and it is the ONLY shape
// that may carry skills (bundles.Loader refuses `skills:` in a single-file
// bundle), so it is the shape whose publish path matters most.
func writeDirFormBundleFixture(t *testing.T, cfg *config.Config, name string) string {
	t.Helper()
	dir := filepath.Join(authoredV1(cfg.GetAppPaths()[0]), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "greet"), 0o755))
	manifest := filepath.Join(dir, "bundle.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(
		"version: 1.0.0\nskills:\n  greet:\n    notes: say hello\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills", "greet", "SKILL.md"),
		[]byte("# greet\n\nSay hello.\n"), 0o644))
	return manifest
}

// pushOneBundle publishes path through the fixture's mock publisher and returns
// the reported target path alongside the paths this push actually wrote (in
// call order — more than one only if publishing ever grows past the manifest).
func pushOneBundle(t *testing.T, cfg *config.Config, fix pushManagerFixture, path string) (reported string, written []string) {
	t.Helper()
	before := len(fix.mock.createOrUpdateCalls)
	res, err := PushBundle(context.Background(), cfg, PushBundleRequest{
		Path:           path,
		Remote:         "personal",
		PublishManager: fix.mgr,
		Message:        "publish",
	})
	require.NoError(t, err)
	for _, c := range fix.mock.createOrUpdateCalls[before:] {
		written = append(written, c.Path)
	}
	require.NotEmpty(t, written, "publisher must have been invoked")
	return res.TargetPath, written
}

// pushManagerFixture pairs a PublishManager with the mock Publisher behind it,
// so a test can read back what was written.
type pushManagerFixture struct {
	mock *mockPublisher
	mgr  *remote.PublishManager
}

func newPushManagerFixture(t *testing.T) (*config.Config, pushManagerFixture) {
	t.Helper()
	mock := &mockPublisher{returnCommitSHA: "sha0001"}
	cfg, _, mgr := pushTestSetup(t, mock)
	return cfg, pushManagerFixture{mock: mock, mgr: mgr}
}

// TestPushBundle_ReportedPathIsTheWrittenPath_SingleFile is the binding for the
// ordinary shape: what push says it published is the path it published to.
func TestPushBundle_ReportedPathIsTheWrittenPath_SingleFile(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	bundlePath := filepath.Join(authoredV1(cfg.GetAppPaths()[0]), "for-push.yaml")

	reported, written := pushOneBundle(t, cfg, fix, bundlePath)

	require.Equal(t, []string{repoV1("for-push.yaml")}, written)
	assert.Equal(t, written[0], reported,
		"the reported target path IS the path published to — one computation, not two that agree")
}

// TestPushBundle_DirectoryForm_IsRefusedUnderADocumentLayout replaces two tests
// that pinned DIRECTORY-form publishing under v1 — the shape this project has
// now removed from v1. v1 is the single-file document form; only v2 holds trees.
//
// WHAT THOSE TWO TESTS PROVED, and what must come back when the layout moves to
// v2, because none of it is covered while the refusal stands:
//   - the reported target path IS the path published to, for a tree: every file
//     that travels sits under the reported root, manifest directly in it
//   - two directory bundles publish under their OWN names rather than colliding
//     at a shared bundles/bundle.yaml — a silent data-loss overwrite
//   - the WHOLE tree travels, not the manifest alone
//
// See taskloom row resolute-runway.
func TestPushBundle_DirectoryForm_IsRefusedUnderADocumentLayout(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	manifest := writeDirFormBundleFixture(t, cfg, "dir-form")

	before := len(fix.mock.createOrUpdateCalls)
	_, err := PushBundle(context.Background(), cfg, PushBundleRequest{
		Path:           manifest,
		Remote:         "personal",
		PublishManager: fix.mgr,
		Message:        "publish",
	})

	require.Error(t, err, "a directory-form bundle must not publish into a document layout")
	assert.Len(t, fix.mock.createOrUpdateCalls[before:], 0,
		"REFUSING means writing NOTHING — a partial tree under a document's name is bytes no reader looks for")
}
