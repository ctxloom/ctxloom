package operations

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// authorKit writes an authored directory-form bundle "kit" with envelope as
// its bundle.yaml and one fragment, returning the bundle directory.
func authorKit(t *testing.T, cfg *config.Config, envelope, body string) string {
	t.Helper()
	dir := filepath.Join(paths.BundlesLayoutRoot(cfg.GetBundleDirs()[0], paths.LayoutV2), "kit")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundles.DirectoryFormManifest), []byte(envelope), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte(body), 0o644))
	return dir
}

func signKit(t *testing.T, cfg *config.Config, force bool) error {
	t.Helper()
	_, err := SignBundleFile(cfg, SignBundleRequest{Target: SignTarget{BundleName: "kit"}, Signer: testSigner(t), Force: force})
	return err
}

func signedKitManifest(t *testing.T, dir string) content.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, content.ManifestPath))
	require.NoError(t, err)
	m, err := content.ParseManifest(raw)
	require.NoError(t, err)
	return m
}

func TestSignBundleFile_RefusesAVersionThatIsNotStrictSemver(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	authorKit(t, cfg, "version: \"1.0\"\n", "KEEPER\n")
	err := signKit(t, cfg, false)
	assert.True(t, errors.Is(err, ErrUnsignableVersion), "got %v", err)
}

func TestSignBundleFile_SignsTheAuthoredReleaseIncludingRetractions(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := authorKit(t, cfg, "version: 1.3.0\nretracts:\n  - version: 1.2.0\n    reason: broken hook\nwithdrawn: superseded by kit2\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))

	rel := signedKitManifest(t, dir).Release()
	assert.Equal(t, "kit", rel.Name)
	assert.Equal(t, "1.3.0", rel.Version.String())
	retracted, why := rel.Retracted(semver.MustParse("1.2.0"))
	assert.True(t, retracted)
	assert.Equal(t, "superseded by kit2", why, "a withdrawal answers for every version")
	require.Len(t, rel.Retracts, 1)
	assert.Equal(t, "broken hook", rel.Retracts[0].Reason)
}

// RULED: one version names one content. A consumer holding the floor at a
// version accepts that same version again, so re-signing it over different
// bytes would change what they run without anything they check moving.
func TestSignBundleFile_RefusesToReSignAVersionOverDifferentContent(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte("CHANGED\n"), 0o644))
	err := signKit(t, cfg, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrVersionAlreadySigned), "got %v", err)
	assert.Contains(t, err.Error(), "1.0.0")

	require.NoError(t, signKit(t, cfg, true), "--force re-signs the version deliberately")
}

func TestSignBundleFile_ABumpedVersionSignsChangedContent(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte("CHANGED\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundles.DirectoryFormManifest), []byte("version: 1.0.1\n"), 0o644))
	require.NoError(t, signKit(t, cfg, false))
	assert.Equal(t, "1.0.1", signedKitManifest(t, dir).Release().Version.String())
}

// Re-signing the same content at the same version — a second maintainer, a
// rotated key — is not a new release. (Authoring a retraction is: bundle.yaml
// is covered by the manifest, so a retraction ships in a new version.)
func TestSignBundleFile_ReSigningIdenticalContentIsAllowed(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))
	require.NoError(t, signKit(t, cfg, false))
}

// versionedProject is setupBundleTestDir with the project root recorded (the
// parent of .ctxloom, as a real project-sourced config has it) and, when
// version is non-empty, a VERSION file there holding it.
func versionedProject(t *testing.T, source config.ConfigSource, version string) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(authoredV1(appDir), 0o755))
	cfg := gatedFixture(config.Fixture{AppPaths: []string{appDir}, AppDir: appDir, AppRoot: root, Source: source})
	if version != "" {
		writeRepoVersion(t, root, version)
	}
	return cfg, root
}

func writeRepoVersion(t *testing.T, root, version string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, RepoVersionFile), []byte(version+"\n"), 0o644))
}

func authoredVersion(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, bundles.DirectoryFormManifest))
	require.NoError(t, err)
	var env struct {
		Version string `yaml:"version"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &env))
	return env.Version
}

// RULED (bucked-crate): in a repo with a VERSION file, sign derives the
// bundle's version from it, so no hand copy exists to drift.
func TestSignBundleFile_StampsTheRepoVersionIntoBundleYAML(t *testing.T) {
	cfg, root := versionedProject(t, config.SourceProject, "1.2.3")
	dir := authorKit(t, cfg, "version: 1.0.0\ndescription: keeps its other fields\n", "KEEPER\n")

	res, err := SignBundleFile(cfg, SignBundleRequest{Target: SignTarget{BundleName: "kit"}, Signer: testSigner(t)})
	require.NoError(t, err)

	assert.Equal(t, "1.2.3", authoredVersion(t, dir), "bundle.yaml carries VERSION")
	assert.Equal(t, "1.2.3", signedKitManifest(t, dir).Release().Version.String(), "the manifest header carries VERSION")
	raw, err := os.ReadFile(filepath.Join(dir, bundles.DirectoryFormManifest))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "keeps its other fields")
	require.NotNil(t, res.VersionStamp)
	assert.Equal(t, VersionStamp{From: "1.0.0", To: "1.2.3", File: filepath.Join(root, RepoVersionFile)}, *res.VersionStamp)
}

func TestSignBundleFile_WithoutAVersionFileKeepsTheHandSetVersion(t *testing.T) {
	cfg, _ := versionedProject(t, config.SourceProject, "")
	dir := authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")

	res, err := SignBundleFile(cfg, SignBundleRequest{Target: SignTarget{BundleName: "kit"}, Signer: testSigner(t)})
	require.NoError(t, err)
	assert.Nil(t, res.VersionStamp)
	raw, err := os.ReadFile(filepath.Join(dir, bundles.DirectoryFormManifest))
	require.NoError(t, err)
	assert.Equal(t, "version: 1.0.0\n", string(raw), "bundle.yaml is not rewritten")
	assert.Equal(t, "1.0.0", signedKitManifest(t, dir).Release().Version.String())
}

// A user-home config's root is $HOME: a VERSION there belongs to no repo.
func TestSignBundleFile_AHomeConfigIsNotStamped(t *testing.T) {
	cfg, _ := versionedProject(t, config.SourceHome, "9.9.9")
	dir := authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))
	assert.Equal(t, "1.0.0", authoredVersion(t, dir))
}

func TestSignBundleFile_AVersionBumpSignsChangedContentWithNoHandEdit(t *testing.T) {
	cfg, root := versionedProject(t, config.SourceProject, "1.0.0")
	dir := authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte("CHANGED\n"), 0o644))
	writeRepoVersion(t, root, "1.0.1")
	require.NoError(t, signKit(t, cfg, false))
	assert.Equal(t, "1.0.1", authoredVersion(t, dir))
	assert.Equal(t, "1.0.1", signedKitManifest(t, dir).Release().Version.String())
}

func TestSignBundleFile_ContentChangeWithoutAVersionBumpIsStillRefused(t *testing.T) {
	cfg, _ := versionedProject(t, config.SourceProject, "1.0.0")
	dir := authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	require.NoError(t, signKit(t, cfg, false))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte("CHANGED\n"), 0o644))
	err := signKit(t, cfg, false)
	assert.True(t, errors.Is(err, ErrVersionAlreadySigned), "got %v", err)
	require.NoError(t, signKit(t, cfg, true), "--force stays the explicit override")
}

func TestSignBundleFile_AVersionFileThatIsNotStrictSemverIsRefused(t *testing.T) {
	cfg, _ := versionedProject(t, config.SourceProject, "v1.2.3")
	authorKit(t, cfg, "version: 1.0.0\n", "KEEPER\n")
	err := signKit(t, cfg, false)
	assert.True(t, errors.Is(err, ErrUnsignableVersion), "got %v", err)
}
