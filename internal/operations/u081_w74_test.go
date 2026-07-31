package operations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// ---------------------------------------------------------------------------
// U081-F14 — the bundle read/export/move trio must canonicalize a short bundle
// name against the SAME filesystem they load it from.
// ---------------------------------------------------------------------------

const w74RemoteURL = "https://github.com/example/personal"

// w74ShortNameFS seeds an in-memory project that has BOTH a configured remote
// alias "personal" AND (optionally) a local bundle spelled "personal/tool", so
// decision E — a local file of the same spelling wins over the remote alias —
// is actually exercisable rather than vacuously true.
func w74ShortNameFS(t *testing.T, withLocalFile bool) (afero.Fs, *config.Config, []string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	require.NoError(t, fs.MkdirAll(appDir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(appDir, "remotes.yaml"),
		[]byte("default: personal\nremotes:\n  personal:\n    url: "+w74RemoteURL+"\n    version: v1\n"), 0644))

	bdir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(filepath.Join(bdir, "personal"), 0755))
	if withLocalFile {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "personal", "tool.yaml"),
			[]byte("version: 1.0.0\nfragments:\n  a:\n    content: hi\n"), 0644))
	}
	return fs, config.NewFixture(config.Fixture{AppPaths: []string{appDir}}), []string{bdir}
}

// TestCanonicalizeBundleArg_ResolvesAgainstTheInjectedFS proves the filesystem
// argument is LOAD-BEARING, which is the whole reason the call sites must hand
// it the filesystem they themselves use. Both of canonicalizeBundleArg's inputs
// — the remote registry and the local-file existence probe — are read through
// that argument, so an operation that resolves the name on one filesystem and
// loads the bundle on another is asking two different questions.
func TestCanonicalizeBundleArg_ResolvesAgainstTheInjectedFS(t *testing.T) {
	fs, cfg, dirs := w74ShortNameFS(t, false)

	// With the project's filesystem: the alias is a known remote and no local
	// file claims the spelling, so the short ref canonicalizes to the remote.
	assert.Equal(t, w74RemoteURL+"@bundles/tool",
		canonicalizeBundleArg(cfg, "personal/tool", dirs, fs),
		"the injected filesystem must be the one the registry is read from")

	// Without it, the very same call cannot see the remotes.yaml that lives
	// only in the injected filesystem, and the ref is left as authored. This
	// difference is what makes the assertion above a pin rather than a tautology.
	assert.Equal(t, "personal/tool",
		canonicalizeBundleArg(cfg, "personal/tool", dirs, nil))

	// And with a local file of the same spelling present, decision E keeps the
	// ref local — again only visible through the injected filesystem.
	fsLocal, cfgLocal, dirsLocal := w74ShortNameFS(t, true)
	assert.Equal(t, "personal/tool",
		canonicalizeBundleArg(cfgLocal, "personal/tool", dirsLocal, fsLocal))
}

// TestShortNameLocalWins_ReadAndExport is the SHARED behaviour the U081-F14
// cleanup must preserve at the public seam: `bundle view` and `bundle export`
// both accept a per-remote short name whose spelling is claimed by a local
// file, and both serve that local file (decision E). The two call sites spelled
// their filesystem argument differently; collapsing them onto the defaulted one
// must not move this.
func TestShortNameLocalWins_ReadAndExport(t *testing.T) {
	fs, cfg, _ := w74ShortNameFS(t, true)

	read, err := ReadBundle(context.Background(), cfg, ReadBundleRequest{Name: "personal/tool", FS: fs})
	require.NoError(t, err)
	assert.Contains(t, string(read.Raw), "content: hi", "the local file's bytes, not a remote ref")

	exp, err := ExportBundle(context.Background(), cfg, ExportBundleRequest{Name: "personal/tool", DestDir: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/out", "tool.yaml"), exp.Dest)
	got, err := afero.ReadFile(fs, exp.Dest)
	require.NoError(t, err)
	assert.Contains(t, string(got), "content: hi")
}

// ---------------------------------------------------------------------------
// U081-F13 — an unreadable lockfile silently empties the removed-upstream walk.
// ---------------------------------------------------------------------------

// TestBundleListDeletedResolver_WarnsOnUnreadableLockfile pins the LOUD path.
// Every remote source the deleted-item walk inspects comes from the lockfile,
// so a lockfile that will not parse leaves the resolver with nothing to look
// at and `bundle list` stops flagging bundles removed upstream — while still
// printing a complete-looking listing. The listing is allowed to degrade; it is
// not allowed to degrade in silence.
func TestBundleListDeletedResolver_WarnsOnUnreadableLockfile(t *testing.T) {
	projectDir := testsupport.ProjectDir(t) // isolates HOME/env; never the real ~/.ctxloom
	appDir := filepath.Join(projectDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	lockPath := paths.LockPath(appDir)
	require.NoError(t, os.WriteFile(lockPath, []byte("bundles: [this is not\n  a: mapping\n"), 0644))

	// The fixture must be hostile FROM THE CODE'S VANTAGE POINT: assert the
	// lockfile really is unreadable through the exact manager the resolver
	// builds, before asserting anything about what the resolver says.
	_, loadErr := remote.NewLockfileManager(appDir, remote.WithLockfileFS(afero.NewOsFs())).Load()
	require.Error(t, loadErr, "fixture is not broken — the rest of this test would prove nothing")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NotNil(t, bundleListDeletedResolver(cfg))

	assert.Contains(t, sink.String(), lockPath,
		"the warning must name the unreadable lockfile")
	assert.Contains(t, sink.String(), "removed upstream",
		"the warning must say what the user is no longer being told")
}

// TestBundleListDeletedResolver_SilentOnGoodLockfile keeps the warning honest:
// the normal path stays quiet, so the diagnostic above means something when it
// does appear.
func TestBundleListDeletedResolver_SilentOnGoodLockfile(t *testing.T) {
	projectDir := testsupport.ProjectDir(t)
	appDir := filepath.Join(projectDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NotNil(t, bundleListDeletedResolver(cfg))
	assert.Empty(t, sink.String(), "a missing lockfile is the ordinary case, not a fault")
}

// ---------------------------------------------------------------------------
// U081-F07 — a nil BundleByteSource means "nothing is installed", silently.
// ---------------------------------------------------------------------------

// TestNewBundleReaderForConfig_WarnsWhenLockfileUnreadable pins the loud path
// for the reader every installed-probe runs through. isInstalled answers false
// for a nil source, so an unreadable lockfile does not merely lose the reader —
// it converts a fully-installed project into "every remote bundle is missing".
// The verdict stands; the silence does not.
func TestNewBundleReaderForConfig_WarnsWhenLockfileUnreadable(t *testing.T) {
	projectDir := testsupport.ProjectDir(t)
	appDir := filepath.Join(projectDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	lockPath := paths.LockPath(appDir)
	require.NoError(t, os.WriteFile(lockPath, []byte("bundles: [this is not\n  a: mapping\n"), 0644))

	// Prove the fixture is hostile through the code's own loader first.
	_, loadErr := remote.NewLockfileManager(appDir).Load()
	require.Error(t, loadErr, "fixture is not broken — the rest of this test would prove nothing")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	reader := NewBundleReaderForConfig(cfg)

	assert.Nil(t, reader, "an unreadable lockfile still yields no reader")
	assert.Contains(t, sink.String(), lockPath, "the warning must name the unreadable lockfile")
	assert.Contains(t, sink.String(), "not installed",
		"the warning must name the consequence the user will actually observe")

	// The consequence itself, pinned so the warning's claim stays true: a nil
	// source reports every reference as not installed.
	assert.False(t, isInstalled(context.Background(), "https://github.com/example/personal@bundles/tool", nil))
}

// TestNewBundleReaderForConfig_SilentOnGoodLockfile keeps the diagnostic
// meaningful: a project with no lockfile at all is the ordinary first-run case
// and must stay quiet.
func TestNewBundleReaderForConfig_SilentOnGoodLockfile(t *testing.T) {
	projectDir := testsupport.ProjectDir(t)
	appDir := filepath.Join(projectDir, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	assert.NotNil(t, NewBundleReaderForConfig(cfg))
	assert.Empty(t, sink.String())
}

// ---------------------------------------------------------------------------
// U081-F12 — the destination switch must be exhaustive, not default-to-copy.
// ---------------------------------------------------------------------------

// TestMoveByDest_UnrecognisedKindRefuses is the guard on a move's most
// dangerous property: the caller deletes the source as soon as this returns
// without an error. A destination kind the router does not understand must
// therefore stop the move, not fall through to the local-copy branch with
// whatever Dir happens to be set.
func TestMoveByDest_UnrecognisedKindRefuses(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	src := srcBundlePath(cfg)

	res, err := moveByDest(context.Background(), cfg, fs, MoveBundleRequest{Name: "seed", To: "/out"},
		"seed", src, moveDest{Kind: moveDestKind("sftp"), Dir: "/out"})

	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "sftp", "the refusal must name the kind it did not understand")

	// The payload assertion, not the exit path: nothing was written at the
	// destination the fall-through branch would have used.
	copied, _ := afero.Exists(fs, filepath.Join("/out", "seed.yaml"))
	assert.False(t, copied, "an unrecognised destination must not be copied to")

	// And the source is still there — a refusal must never be the step that
	// makes MoveBundle eligible to delete it.
	stillThere, _ := afero.Exists(fs, src)
	assert.True(t, stillThere)
}

// TestMoveByDest_KnownKindsStillRoute is the characterization half: both
// recognised kinds must keep reaching their writers unchanged.
func TestMoveByDest_KnownKindsStillRoute(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	src := srcBundlePath(cfg)

	res, err := moveByDest(context.Background(), cfg, fs, MoveBundleRequest{Name: "seed", To: "/out"},
		"seed", src, moveDest{Kind: moveDestPath, Dir: "/out"})
	require.NoError(t, err)
	assert.Equal(t, MoveDestPath, res.DestKind)
	assert.Equal(t, filepath.Join("/out", "seed.yaml"), res.Dest)

	// The remote branch is reached (and fails on its own terms, with no
	// PublishManager wired) rather than being routed to the local copy.
	_, rerr := moveByDest(context.Background(), cfg, fs, MoveBundleRequest{Name: "seed", To: "nope"},
		"seed", src, moveDest{Kind: moveDestRemote, Remote: "nope"})
	require.Error(t, rerr)
	assert.NotContains(t, rerr.Error(), "destination is the bundle's own directory",
		"a remote destination must not be answered by the local-copy branch")
}

// ---------------------------------------------------------------------------
// U081-F09 — a move that fully happened must not read as a move that did not.
// ---------------------------------------------------------------------------

// failRemoveFs fails Remove for paths matching a predicate — a disk that will
// not let the last cleanup step of a move complete.
type failRemoveFs struct {
	afero.Fs
	fail func(name string) bool
}

func (f *failRemoveFs) Remove(name string) error {
	if f.fail(name) {
		return errors.New("permission denied")
	}
	return f.Fs.Remove(name)
}

// TestMoveBundle_StraySourceSignature_ErrorNamesDestinationAndForbidsRetry is
// the worst-shaped outcome this command has: the bundle IS at the destination
// and the source YAML IS gone, so the move happened — only the source .sig
// survived. The old message said "bundle moved, but its source signature could
// not be removed", named no destination, and left the user with an exit code
// that invites a re-run. Re-running cannot work: there is no source left.
func TestMoveBundle_StraySourceSignature_ErrorNamesDestinationAndForbidsRetry(t *testing.T) {
	base, cfg := memMoveFS(t, true)
	require.NoError(t, base.MkdirAll("/out", 0755)) // resolveMoveDest requires an EXISTING dir
	src := srcBundlePath(cfg)
	fs := &failRemoveFs{Fs: base, fail: func(name string) bool { return strings.HasSuffix(name, sigSuffix) }}

	// The fixture must be hostile in exactly one place: the .sig removal, and
	// nothing else. Prove that before asserting on the message.
	require.Error(t, fs.Remove(src+sigSuffix), "fixture is not broken")
	ok, _ := afero.Exists(fs, src)
	require.True(t, ok, "the source bundle must still be removable for the move to reach the .sig step")

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)

	// The payload: the destination the bundle actually reached, and the fact
	// that a retry is not the remedy.
	assert.Contains(t, err.Error(), filepath.Join("/out", "seed.yaml"),
		"the failure must name where the bundle actually went")
	assert.Contains(t, err.Error(), "re-running the move will not work",
		"the failure must stop the retry it would otherwise invite")

	// And the state the message describes is the real one.
	moved, _ := afero.Exists(fs, filepath.Join("/out", "seed.yaml"))
	assert.True(t, moved, "the bundle did land at the destination")
	srcGone, _ := afero.Exists(fs, src)
	assert.False(t, srcGone, "the source YAML was removed — the move happened")
}

// TestMoveBundle_UnremovableSourceYAML_ErrorSaysBothPlaces is the other half:
// here the bundle exists in TWO places and re-running (or deleting the
// duplicate) IS the remedy, so the message must not be interchangeable with the
// one above.
func TestMoveBundle_UnremovableSourceYAML_ErrorSaysBothPlaces(t *testing.T) {
	base, cfg := memMoveFS(t, false)
	require.NoError(t, base.MkdirAll("/out", 0755))
	src := srcBundlePath(cfg)
	fs := &failRemoveFs{Fs: base, fail: func(name string) bool { return name == src }}

	require.Error(t, fs.Remove(src), "fixture is not broken")

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both places")
	assert.Contains(t, err.Error(), filepath.Join("/out", "seed.yaml"))
	assert.NotContains(t, err.Error(), "re-running the move will not work")

	stillThere, _ := afero.Exists(fs, src)
	assert.True(t, stillThere)
}

// ---------------------------------------------------------------------------
// U081-F05 — a distill run where everything failed must not rewrite the file.
// ---------------------------------------------------------------------------

// TestDistillBundleFile_AllFailuresLeaveTheFileByteIdentical is the payload
// assertion, not an exit-code one: with every distill attempt failing there is
// nothing new to persist, so the author's document — comments, key order and
// all — must come back unchanged. The old gate was "was there at least one
// TARGET", which rewrote the file after a run that accomplished nothing, losing
// the comments to a re-marshal and (via Store.Save's stale-signature check)
// deleting any detached publisher signature over the original bytes.
func TestDistillBundleFile_AllFailuresLeaveTheFileByteIdentical(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.yaml")
	bundleYAML := `# an author comment that a re-marshal would silently drop
version: 1.0.0
fragments:
  rules:
    content: "fresh content the stale distillation no longer matches"
    distilled: "stale distilled text"
    distilled_by: "stale-model"
    content_hash: "0000000000000000000000000000000000000000000000000000000000000000"
`
	require.NoError(t, os.WriteFile(path, []byte(bundleYAML), 0o644))

	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{
		Path:      path,
		Distiller: errDistiller{},
	})
	require.NoError(t, err)

	// The fixture is only meaningful if the run really did have a target and
	// really did fail it — otherwise this would pass for the wrong reason.
	require.Len(t, res.Items, 1)
	require.Equal(t, "distill_failed", res.Items[0].Reason,
		"fixture is not hostile: the distill was expected to fail")

	assert.False(t, res.Saved, "nothing was distilled, so nothing may be written")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, bundleYAML, string(after),
		"a run that distilled nothing must leave the author's file byte-identical")
}

// TestDistillBundleFile_PartialSuccessStillSaves keeps the new gate honest: one
// success among failures still has something to persist, and must still write.
func TestDistillBundleFile_PartialSuccessStillSaves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.yaml")
	bundleYAML := `version: 1.0.0
fragments:
  rules:
    content: "needs distilling"
`
	require.NoError(t, os.WriteFile(path, []byte(bundleYAML), 0o644))

	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{
		Path:      path,
		Distiller: okDistiller{body: "distilled body"},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.Equal(t, DistillStatusDistilled, res.Items[0].Status)
	assert.True(t, res.Saved, "a successful distillation must still be persisted")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(after), "distilled body")
}
