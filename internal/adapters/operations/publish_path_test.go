package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
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

// TestPushBundle_TreeForm_ReportedPathIsTheWrittenManifestPath,
// TestPushBundle_TreeForm_TwoBundlesPublishUnderTheirOwnNames and
// TestPushBundle_TreeForm_WholeTreeTravels REPLACE
// TestPushBundle_DirectoryForm_IsRefusedUnderADocumentLayout, which pinned a
// REFUSAL that predates format v2: PushBundle's treeForm branch (runTreePush)
// now publishes a directory-form bundle instead of refusing it, so a test
// asserting the refusal simply asserts something false about current
// production. These three are exactly what that test's own doc comment named
// as what must come back once the refusal lifted — see taskloom row
// resolute-runway.
func TestPushBundle_TreeForm_ReportedPathIsTheWrittenManifestPath(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	manifest := writeDirFormBundleFixture(t, cfg, "dir-form")

	reported, written := pushOneBundle(t, cfg, fix, manifest)

	root := repoV2("dir-form")
	require.Contains(t, written, root+"/bundle.yaml",
		"the manifest travels at the reported root's own bundle.yaml, not a hand-picked path")
	assert.Equal(t, root, reported,
		"the reported target path IS the tree's own root — one computation, not two that agree")
}

// TestPushBundle_TreeForm_TwoBundlesPublishUnderTheirOwnNames guards the
// data-loss shape a shared "bundles/bundle.yaml" root would produce: a SECOND
// directory-form bundle must not overwrite the first at one collapsed path.
func TestPushBundle_TreeForm_TwoBundlesPublishUnderTheirOwnNames(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	first := writeDirFormBundleFixture(t, cfg, "dir-form-a")
	second := writeDirFormBundleFixture(t, cfg, "dir-form-b")

	reportedA, _ := pushOneBundle(t, cfg, fix, first)
	reportedB, _ := pushOneBundle(t, cfg, fix, second)

	assert.NotEqual(t, reportedA, reportedB,
		"two directory bundles must publish under their OWN names, not collide at one shared root")
	assert.Equal(t, repoV2("dir-form-a"), reportedA)
	assert.Equal(t, repoV2("dir-form-b"), reportedB)
}

// TestPushBundle_TreeForm_WholeTreeTravels: every file under the bundle's
// directory — the manifest AND its skill package — must travel in the same
// publish, not the manifest alone. A skill left behind is a bundle that loads
// with an item silently missing.
func TestPushBundle_TreeForm_WholeTreeTravels(t *testing.T) {
	cfg, fix := newPushManagerFixture(t)
	manifest := writeDirFormBundleFixture(t, cfg, "dir-form")

	_, written := pushOneBundle(t, cfg, fix, manifest)

	root := repoV2("dir-form")
	assert.ElementsMatch(t, []string{
		root + "/bundle.yaml",
		root + "/skills/greet/SKILL.md",
	}, written, "the whole tree must travel, not just the manifest")
}
