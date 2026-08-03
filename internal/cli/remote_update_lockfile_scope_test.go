package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// `remote update --apply` READS the lockfile through projectAppDir(cfg) — the
// project root config.Load walked up to from cwd, which is what lets the
// command work from a subdirectory at all. It used to WRITE through
// remote.NewPuller's default, remote.NewLockfileManager(".ctxloom"): a RELATIVE
// path resolved against the process cwd.
//
// From the project root those are the same file and nothing is wrong. From any
// subdirectory they are two, and the update reported success while the pin it
// was supposed to advance never moved (scarce-strength).
//
// These tests run from a NESTED subdirectory on the real filesystem, because
// that is the only place the two paths differ — one that ran at the project
// root would pass against the bug.

// scopeBundleRef is the canonical reference under test.
const scopeBundleRef = "https://github.com/alice/ctxloom@bundles/security"

// scopeSHA is the commit the pull pins to.
const scopeSHA = "abc123def456"

// updateScopeFixture lays out a project with a .ctxloom dir and a registry
// holding one remote, then chdirs into a NESTED subdirectory of it — where the
// user is standing, and deep enough that a relative ".ctxloom" is unmistakably
// a different directory from the project's.
func updateScopeFixture(t *testing.T) (projectApp string, registry *remote.Registry) {
	t.Helper()

	project := t.TempDir()
	projectApp = filepath.Join(project, ".ctxloom")
	require.NoError(t, os.MkdirAll(projectApp, 0o755))

	sub := filepath.Join(project, "services", "api")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Chdir(sub)

	var err error
	registry, err = remote.NewRegistry(filepath.Join(projectApp, "remotes.yaml"))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	return projectApp, registry
}

// pullThroughUpdatePuller drives the puller `remote update --apply` builds,
// exactly as applyUpdateBatch does — notably WITHOUT PullOptions.LocalDir,
// because the command does not set it either. That omission is precisely what
// leaves the puller's own lockfile base dir deciding where the pin lands.
//
// Only the fetcher factory is substituted (the real one would reach the
// network); the lockfile manager under test is the one the command passes.
func pullThroughUpdatePuller(t *testing.T, projectApp string, registry *remote.Registry) {
	t.Helper()

	mf := remote.NewMockFetcher()
	mf.Refs["main"] = scopeSHA
	mf.Refs[scopeSHA] = scopeSHA
	mf.Files[".ctxloom/content/bundles/security.yaml"] =
		[]byte("description: Security bundle\nfragments:\n  tdd:\n    content: test\n")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{projectApp}})
	puller := newUpdatePuller(cfg, registry, remote.AuthConfig{},
		remote.NewLockfileManager(projectAppDir(cfg)),
		remote.WithFetcherFactory(func(string, remote.AuthConfig) (remote.Fetcher, error) { return mf, nil }),
	)

	var stdout bytes.Buffer
	constraint := ""
	_, err := puller.Pull(context.Background(), scopeBundleRef+"@"+scopeSHA, remote.PullOptions{
		ItemType:         remote.ItemTypeBundle,
		RequestedVersion: &constraint,
		Stdout:           &stdout,
		Stdin:            strings.NewReader(""),
	})
	require.NoError(t, err, "pull output:\n%s", stdout.String())
}

// TestUpdatePuller_WritesThePinIntoTheProjectLockfile is the characterization:
// run from a subdirectory, the pin must land in the PROJECT lockfile — the same
// file detect read it out of, reload reads it back from, and cleanup prunes.
func TestUpdatePuller_WritesThePinIntoTheProjectLockfile(t *testing.T) {
	projectApp, registry := updateScopeFixture(t)
	pullThroughUpdatePuller(t, projectApp, registry)

	lock, err := remote.NewLockfileManager(projectApp).Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(remote.ItemTypeBundle, scopeBundleRef)
	require.True(t, ok,
		"the project lockfile has no entry for the pulled bundle — apply wrote its pin somewhere else, "+
			"so the reload reads the OLD sha back and the update reports success over a pin that never moved")
	assert.Equal(t, scopeSHA, entry.SHA)
}

// TestUpdatePuller_CreatesNoLockfileBesideTheUser is the same defect from the
// other side, and the half that a "did the project lockfile change?" assertion
// alone would miss: the stray <cwd>/.ctxloom the buggy path CREATES is real
// debris, which a later ctxloom invocation from that directory would then walk
// up and find FIRST — adopting the wrong project root.
func TestUpdatePuller_CreatesNoLockfileBesideTheUser(t *testing.T) {
	projectApp, registry := updateScopeFixture(t)
	pullThroughUpdatePuller(t, projectApp, registry)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	stray := filepath.Join(cwd, ".ctxloom")
	_, statErr := os.Stat(stray)
	assert.True(t, os.IsNotExist(statErr),
		"apply created %s — a second .ctxloom in whichever directory the user happened to be standing in", stray)
}

// TestUpdatePuller_LandsDirectoryFormContentBesideThePin covers the half the
// lockfile assertions do not reach. Puller.installTree resolves its destination
// from lockfileManager.BaseDir(), so a directory-form bundle's CONTENT inherited
// the same wrong root — pin and content in different trees, which is the exact
// hazard BaseDir's own doc was written to warn about.
func TestUpdatePuller_LandsDirectoryFormContentBesideThePin(t *testing.T) {
	projectApp, registry := updateScopeFixture(t)

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{projectApp}})
	lockManager := remote.NewLockfileManager(projectAppDir(cfg))
	puller := newUpdatePuller(cfg, registry, remote.AuthConfig{}, lockManager,
		remote.WithFetcherFactory(func(string, remote.AuthConfig) (remote.Fetcher, error) {
			mf := remote.NewMockFetcher()
			mf.Refs[scopeSHA] = scopeSHA
			return mf, nil
		}),
		// A directory-form publish: the tree fetcher answers with the files
		// instead of a single bundle document.
		remote.WithTreeFetcher(func(context.Context, remote.Fetcher, string, string, string, string, string) (map[string]remote.TreeFile, error) {
			return map[string]remote.TreeFile{
				"bundle.yaml": {Data: []byte("name: security\nversion: \"1.0\"\n")},
			}, nil
		}),
	)

	var stdout bytes.Buffer
	constraint := ""
	_, err := puller.Pull(context.Background(), scopeBundleRef+"@"+scopeSHA, remote.PullOptions{
		ItemType:         remote.ItemTypeBundle,
		RequestedVersion: &constraint,
		Stdout:           &stdout,
		Stdin:            strings.NewReader(""),
	})
	require.NoError(t, err, "pull output:\n%s", stdout.String())

	cwd, err := os.Getwd()
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(cwd, ".ctxloom"))
	assert.True(t, os.IsNotExist(statErr),
		"the fetched directory-form tree landed under the cwd instead of the project root")
	assert.NotEmpty(t, findTreeFiles(t, projectApp),
		"no installed tree found anywhere under the project's .ctxloom — the content went to the wrong root")
}

// findTreeFiles lists every regular file under appDir, so the assertion above
// is "it landed in the project tree" rather than a guess at the exact cache
// layout (which LocalTreePath owns, not this test).
func findTreeFiles(t *testing.T, appDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(appDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && filepath.Base(path) == "bundle.yaml" {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}
