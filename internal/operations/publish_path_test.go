package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
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
	dir := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills", "greet"), 0o755))
	manifest := filepath.Join(dir, "bundle.yaml")
	require.NoError(t, os.WriteFile(manifest, []byte(
		"version: 1.0.0\nskills:\n  greet:\n    description: say hello\n"), 0o644))
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
	bundlePath := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "for-push.yaml")

	reported, written := pushOneBundle(t, cfg, fix, bundlePath)

	require.Equal(t, []string{".ctxloom/content/bundles/for-push.yaml"}, written)
	assert.Equal(t, written[0], reported,
		"the reported target path IS the path published to — one computation, not two that agree")
}

// TestPushBundle_ReportedPathIsTheWrittenPath_DirectoryForm is the same binding
// for the shape that used to diverge. Both halves were wrong together before the
// fix, so this assertion holds either way; it is here to make a FUTURE
// divergence impossible rather than to catch the naming defect (the tests below
// do that).
func TestPushBundle_ReportedPathIsTheWrittenPath_DirectoryForm(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	manifest := writeDirFormBundleFixture(t, cfg, "dir-form")

	reported, written := pushOneBundle(t, cfg, fix, manifest)

	require.Len(t, written, 1)
	assert.Equal(t, written[0], reported,
		"the reported target path IS the path published to, for directory form too")
}

// `bundle move --to <remote>` is the highest-stakes reader of the reported
// path: it publishes, derives SigDest from what PushBundle SAYS it wrote, and
// then DELETES the local source. If the reported path and the written path ever
// diverged, the user would be left with the source gone, a signature recorded
// at a path nobody wrote, and no local copy to re-publish from. Bound here to
// what the publisher actually received.
func TestMoveBundle_ToRemote_ReportedDestIsTheWrittenPath(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "abc1234"}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)
	signOnDisk(t, afero.NewOsFs(), bundlePath)

	res, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{
		Name: "for-push", To: "personal", PublishManager: mgr,
	})
	require.NoError(t, err)

	require.Len(t, mock.createOrUpdateCalls, 2, "bundle + detached signature sibling")
	assert.Equal(t, mock.createOrUpdateCalls[0].Path, res.Dest,
		"the destination reported to the user is the one the bundle was written to")
	assert.Equal(t, mock.createOrUpdateCalls[1].Path, res.SigDest,
		"and the reported SigDest is the path the signature was written to")
}

// A directory-form bundle publishes under the BUNDLE's name, not its manifest
// file's. Naming it after the file made every such bundle land on
// `bundles/bundle.yaml` and silently overwrite the last one — a data-loss
// collision with no error and no warning, since each push succeeded on its own
// terms. The name now comes from bundles.ExtractBundleName, the same rule the
// loader itself names a bundle by, so the remote name matches the name the user
// pushed.
func TestPushBundle_DirectoryFormBundles_PublishUnderTheirOwnNames(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	first := writeDirFormBundleFixture(t, cfg, "alpha-form")
	second := writeDirFormBundleFixture(t, cfg, "beta-form")

	_, firstWritten := pushOneBundle(t, cfg, fix, first)
	_, secondWritten := pushOneBundle(t, cfg, fix, second)

	assert.Equal(t, []string{".ctxloom/content/bundles/alpha-form.yaml"}, firstWritten,
		"named after the bundle, not after its bundle.yaml manifest")
	assert.Equal(t, []string{".ctxloom/content/bundles/beta-form.yaml"}, secondWritten,
		"a second directory-form bundle gets its own remote path instead of overwriting the first")
}
