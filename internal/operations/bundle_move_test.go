package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

const moveBundleBody = "version: 1.0.0\nfragments:\n  a:\n    content: hi\n"

// moveSigBody is a REAL publish signature over moveBundleBody's exact bytes.
// A placeholder blob would do here no longer: move/export now verify that the
// signature they carry actually covers the bytes it ships with, which is the
// invariant these tests exist to protect.
var moveSigBody = mustSignBody(moveBundleBody)

func mustSignBody(body string) string {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := ssh.NewSignerFromSigner(priv)
	if err != nil {
		panic(err)
	}
	armored, err := signing.Sign([]byte(body), signer, signing.NamespacePublish)
	if err != nil {
		panic(err)
	}
	return string(armored)
}

// memMoveFS seeds an in-memory project with one authored bundle ("seed") and,
// when signed, its detached .sig sibling.
func memMoveFS(t *testing.T, signed bool) (afero.Fs, *config.Config) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	bdir := paths.LocalBundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bdir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "seed.yaml"), []byte(moveBundleBody), 0644))
	if signed {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(bdir, "seed.yaml.sig"), []byte(moveSigBody), 0644))
	}
	return fs, config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

func srcBundlePath(cfg *config.Config) string {
	return filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "seed.yaml")
}

// failWriteFs fails every create/write whose path matches a predicate — a fake
// disk that eats the destination write, so tests can assert the source survives.
type failWriteFs struct {
	afero.Fs
	fail func(name string) bool
}

func (f *failWriteFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&os.O_CREATE != 0 && f.fail(name) {
		return nil, errors.New("disk on fire")
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func (f *failWriteFs) Create(name string) (afero.File, error) {
	if f.fail(name) {
		return nil, errors.New("disk on fire")
	}
	return f.Fs.Create(name)
}

// Rename intercepts the FINAL step of an atomic write (iox.WriteFileAtomicFs:
// unique temp + fsync + rename into place) by its DESTINATION name. The temp
// file itself is created under a name like ".seed.yaml.sig.<rand>.tmp", which
// never matches a suffix-shaped predicate like ".sig" — so without this, a
// predicate written against the FINAL name would see the temp-file create
// succeed and the rename into place go unintercepted, silently defeating the
// fault injection this fixture exists to provide.
func (f *failWriteFs) Rename(oldname, newname string) error {
	if f.fail(newname) {
		return errors.New("disk on fire")
	}
	return f.Fs.Rename(oldname, newname)
}

// --- local-path destination --------------------------------------------------

// A moved bundle must arrive with its signature, byte-identical: the .sig covers
// the bundle file's exact bytes (spec §3.1), so any transform en route — or a
// dropped sibling — lands an unverifiable bundle at the destination.
func TestMoveBundle_ToLocalPath_CarriesBundleAndSignatureVerbatim(t *testing.T) {
	fs, cfg := memMoveFS(t, true)
	require.NoError(t, fs.MkdirAll("/out", 0755))

	res, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.NoError(t, err)

	assert.Equal(t, "moved", res.Status)
	assert.Equal(t, "path", res.DestKind)
	assert.Equal(t, "/out/seed.yaml", res.Dest)
	assert.Equal(t, "/out/seed.yaml.sig", res.SigDest)

	gotBundle, err := afero.ReadFile(fs, "/out/seed.yaml")
	require.NoError(t, err)
	assert.Equal(t, moveBundleBody, string(gotBundle), "bundle bytes must be carried verbatim")
	gotSig, err := afero.ReadFile(fs, "/out/seed.yaml.sig")
	require.NoError(t, err)
	assert.Equal(t, moveSigBody, string(gotSig), "signature bytes must be carried verbatim")
}

func TestMoveBundle_ToLocalPath_RemovesSource(t *testing.T) {
	fs, cfg := memMoveFS(t, true)
	require.NoError(t, fs.MkdirAll("/out", 0755))

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.NoError(t, err)

	src := srcBundlePath(cfg)
	exists, _ := afero.Exists(fs, src)
	assert.False(t, exists, "source bundle must be gone after a successful move")
	sigExists, _ := afero.Exists(fs, src+".sig")
	assert.False(t, sigExists, "source signature must be gone too — no orphan .sig left behind")
}

// A ctxloom project checkout as destination: the bundle lands in that project's
// committed content tree, never in its gitignored cache.
func TestMoveBundle_ToProjectCheckout_LandsInContentBundles(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	require.NoError(t, fs.MkdirAll("/other/.ctxloom", 0755))

	res, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/other", FS: fs})
	require.NoError(t, err)

	want := filepath.Join(paths.LocalBundlesPath("/other/.ctxloom"), "seed.yaml")
	assert.Equal(t, want, res.Dest)
	exists, _ := afero.Exists(fs, want)
	assert.True(t, exists)
	inCache, _ := afero.Exists(fs, filepath.Join(paths.CacheBundlesPath("/other/.ctxloom"), "seed.yaml"))
	assert.False(t, inCache, "a moved bundle must not land in the destination's gitignored cache")
}

// THE safety invariant: a destination write that fails must leave the source
// exactly where it was.
func TestMoveBundle_LocalWriteFails_SourceIntact(t *testing.T) {
	base, cfg := memMoveFS(t, true)
	require.NoError(t, base.MkdirAll("/out", 0755))
	fs := &failWriteFs{Fs: base, fail: func(name string) bool {
		return strings.HasPrefix(filepath.ToSlash(name), "/out/")
	}}

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)

	src := srcBundlePath(cfg)
	exists, _ := afero.Exists(base, src)
	assert.True(t, exists, "a failed move must not remove the source")
	sigExists, _ := afero.Exists(base, src+".sig")
	assert.True(t, sigExists, "a failed move must not remove the source signature")
}

// A signature that exists but cannot be carried is an ERROR — never a silent
// downgrade to an unsigned bundle at the destination.
func TestMoveBundle_SignatureUncopyable_ErrorsAndKeepsSource(t *testing.T) {
	base, cfg := memMoveFS(t, true)
	require.NoError(t, base.MkdirAll("/out", 0755))
	fs := &failWriteFs{Fs: base, fail: func(name string) bool {
		return strings.HasSuffix(name, ".sig")
	}}

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature")

	src := srcBundlePath(cfg)
	exists, _ := afero.Exists(base, src)
	assert.True(t, exists, "source must survive a signature-carry failure")
	sigExists, _ := afero.Exists(base, src+".sig")
	assert.True(t, sigExists)
}

func TestMoveBundle_UnsignedBundle_MovesFine(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	require.NoError(t, fs.MkdirAll("/out", 0755))

	res, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.NoError(t, err)
	assert.Empty(t, res.SigDest)
	exists, _ := afero.Exists(fs, "/out/seed.yaml.sig")
	assert.False(t, exists)
}

// --- destination resolution --------------------------------------------------

func TestMoveBundle_UnresolvableDestination_Errors(t *testing.T) {
	fs, cfg := memMoveFS(t, false)

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "not-a-remote", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a configured remote nor an existing directory")

	// Source untouched.
	exists, _ := afero.Exists(fs, srcBundlePath(cfg))
	assert.True(t, exists)
}

func TestMoveBundle_MissingDestination_Errors(t *testing.T) {
	fs, cfg := memMoveFS(t, false)

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destination is required")
}

// A configured remote NAME wins over a same-named local directory — stated in
// the help text, pinned here so it can never become a silent coin-flip.
func TestResolveMoveDest_RemoteNameWinsOverSamePath(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	require.NoError(t, afero.WriteFile(fs, filepath.Join(cfg.GetAppPaths()[0], "remotes.yaml"), []byte(
		"default: personal\nremotes:\n  personal:\n    url: https://github.com/example/personal\n    version: v1\n"), 0644))
	require.NoError(t, fs.MkdirAll("personal", 0755)) // a directory of the same spelling

	dest, err := resolveMoveDest(cfg, fs, "personal")
	require.NoError(t, err)
	assert.Equal(t, moveDestRemote, dest.Kind)
	assert.Equal(t, "personal", dest.Remote)
}

func TestResolveMoveDest_PlainDirectory(t *testing.T) {
	fs, cfg := memMoveFS(t, false)
	require.NoError(t, fs.MkdirAll("/somewhere/bundles", 0755))

	dest, err := resolveMoveDest(cfg, fs, "/somewhere/bundles")
	require.NoError(t, err)
	assert.Equal(t, moveDestPath, dest.Kind)
	assert.Equal(t, "/somewhere/bundles", dest.Dir)
}

// --- remote destination ------------------------------------------------------

func TestMoveBundle_ToRemote_PublishesAndRemovesSource(t *testing.T) {
	mock := &mockPublisher{returnCommitSHA: "abc1234"}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)
	sigPath := bundlePath + ".sig"
	// A real signature over THIS bundle's exact bytes: move carries a signature
	// only when it actually covers what is being published.
	sigBody := string(signOnDisk(t, afero.NewOsFs(), bundlePath))
	srcBytes, err := os.ReadFile(bundlePath)
	require.NoError(t, err)

	res, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{
		Name: "for-push", To: "personal", PublishManager: mgr,
	})
	require.NoError(t, err)

	assert.Equal(t, "moved", res.Status)
	assert.Equal(t, "remote", res.DestKind)
	assert.Equal(t, "personal", res.Remote)
	assert.Equal(t, "abc1234", res.CommitSHA)
	assert.True(t, res.Signed, "the carried signature must be published alongside")

	require.Len(t, mock.createOrUpdateCalls, 2, "bundle + detached signature sibling")
	assert.Equal(t, srcBytes, mock.createOrUpdateCalls[0].Content, "published bytes must be the local bytes, verbatim")
	assert.True(t, strings.HasSuffix(mock.createOrUpdateCalls[1].Path, ".sig"))
	assert.Equal(t, sigBody, string(mock.createOrUpdateCalls[1].Content), "the existing signature is carried, not regenerated")

	assert.NoFileExists(t, bundlePath, "source must be removed after a successful publish")
	assert.NoFileExists(t, sigPath)
}

func TestMoveBundle_RemotePublishFails_SourceIntact(t *testing.T) {
	mock := &mockPublisher{returnErr: errors.New("github is down")}
	cfg, bundlePath, mgr := pushTestSetup(t, mock)
	sigPath := bundlePath + ".sig"
	signOnDisk(t, afero.NewOsFs(), bundlePath)

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{
		Name: "for-push", To: "personal", PublishManager: mgr,
	})
	require.Error(t, err)

	assert.FileExists(t, bundlePath, "a failed publish must leave the source intact")
	assert.FileExists(t, sigPath)
}

// --- directory-form bundles: the move that cannot be whole ---------------------

// memMoveDirFS seeds a project with a DIRECTORY-form bundle: "<name>/bundle.yaml"
// plus a skill package beside it. That shape exists for exactly one reason —
// bundles.Loader refuses `skills:` in single-file form — so a move that carries
// only the manifest carries the one thing the shape was created NOT to be.
func memMoveDirFS(t *testing.T) (afero.Fs, *config.Config) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	dir := filepath.Join(paths.LocalBundlesPath(appDir), "seed")
	require.NoError(t, fs.MkdirAll(filepath.Join(dir, "skills", "reviewer"), 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "bundle.yaml"),
		[]byte("version: 1.0.0\nskills:\n  reviewer: {}\n"), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "skills", "reviewer", "SKILL.md"),
		[]byte("---\nname: reviewer\ndescription: d\n---\n\nbody\n"), 0644))
	return fs, config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// THE DATA-LOSS PATH (taskloom hurried-showplace). Move publishes the manifest,
// then deletes the source. For a directory-form bundle only bundle.yaml travels,
// so the user is left with an orphaned local skills/ whose manifest is gone and a
// destination copy with no skills — neither half whole, exit 0, no warning.
//
// Refusing is the only honest answer until publish can carry a whole tree: a move
// that cannot be whole must not begin.
func TestMoveBundle_DirectoryFormWithSkills_RefusesRatherThanMovingHalfOfIt(t *testing.T) {
	fs, cfg := memMoveDirFS(t)
	require.NoError(t, fs.MkdirAll("/out", 0755))

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skills/reviewer/SKILL.md",
		"the refusal must name a file that would have been left behind, not just the shape")
}

// The refusal has to happen BEFORE anything is written or removed. A guard that
// fired after the destination write would have already produced the split state
// it exists to prevent.
func TestMoveBundle_DirectoryFormRefusal_LeavesBothSidesUntouched(t *testing.T) {
	fs, cfg := memMoveDirFS(t)
	require.NoError(t, fs.MkdirAll("/out", 0755))

	_, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.Error(t, err)

	dir := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "seed")
	for _, p := range []string{
		filepath.Join(dir, "bundle.yaml"),
		filepath.Join(dir, "skills", "reviewer", "SKILL.md"),
	} {
		exists, _ := afero.Exists(fs, p)
		assert.True(t, exists, "%s must survive a refused move", p)
	}
	wrote, _ := afero.Exists(fs, "/out/bundle.yaml")
	assert.False(t, wrote, "nothing may be written at the destination when the move is refused")
}

// A directory-form bundle carrying NOTHING but its manifest loses nothing by
// moving, so it must still move. The guard is about unmovable payload, not about
// the directory shape — refusing the shape itself would break a move that is
// perfectly whole.
func TestMoveBundle_DirectoryFormWithNoPayloadBesideTheManifest_StillMoves(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := filepath.Join("/proj", ".ctxloom")
	dir := filepath.Join(paths.LocalBundlesPath(appDir), "seed")
	require.NoError(t, fs.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "bundle.yaml"), []byte(moveBundleBody), 0644))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	require.NoError(t, fs.MkdirAll("/out", 0755))

	res, err := MoveBundle(context.Background(), cfg, MoveBundleRequest{Name: "seed", To: "/out", FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "moved", res.Status)
}
