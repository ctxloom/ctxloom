package operations

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const pulledBundleBody = "version: 1.0.0\ndescription: remote tools bundle\nmcp:\n  tools-server:\n    command: tools\n    args: [serve]\n"

// pulledProject is a project whose default agent selects a profile naming a
// remote bundle that is NOT yet installed: the state before `deps pull`.
//
// The source repository's path is FORCED to carry a space and a literal '%'
// rather than left to whatever t.TempDir happens to return: those are the
// characters a lockfile key and a trust key have spelled differently (one
// raw, one percent-encoded), and "a%41b" is chosen so that any layer that
// DECODES a raw path names a different directory ("aAb") instead of failing.
// repoURL is the file URL a user has to type for that path — percent-encoded,
// because the reference grammar parses it as a URL.
func pulledProject(t *testing.T) (appDir, repoURL string) {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "has space", "a%41b", "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(authoredV2(filepath.Join(repoDir, paths.AppDirName)), 0o755))
	bundletree.WriteOS(t, authoredV2(filepath.Join(repoDir, paths.AppDirName)), "tools", pulledBundleBody)
	_, err = wt.Add(repoV2("tools"))
	require.NoError(t, err)
	_, err = wt.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()}})
	require.NoError(t, err)
	repoURL = (&url.URL{Scheme: "file", Path: repoDir}).String()
	require.Contains(t, repoURL, "%20", "precondition: the repo URL carries an escaped space")
	require.Contains(t, repoURL, "%25", "precondition: the repo URL carries an escaped '%'")
	bundleRef := repoURL + "@bundles/tools"

	appDir = filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0o755))
	registerTestRemote(t, appDir, repoURL)

	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "dev.yaml"),
		[]byte("bundles:\n  - "+bundleRef+"\n"), 0o644))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir),
		[]byte("schema_version: 7\ndefault_agent: default\nagents:\n  default:\n    profiles: [dev]\n"), 0o644))
	return appDir, repoURL
}

// pulledApp is the PRODUCTION composition over the project — the real
// reader, the real remote readers and the real executable trust gate — so
// the gate below is the gate a spawn decides with, not a fixture's.
func pulledApp(t *testing.T, appDir string) *App {
	t.Helper()
	src, err := ComposeSources(Compose{NoCompanions: true, Options: []configload.Option{configload.WithAppDir(appDir)}})
	require.NoError(t, err)
	return NewApp(src, Switches{NoCompanions: true}, nil, strictness.Mode{Prog: "ctxloom"}, Handed{Open: config.Open, Reporter: strictness.Sink("ctxloom"), Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims})
}

// pull runs `deps pull` for real: the production puller fetches the tree from
// the repository, installs it, and writes the lockfile entry.
func pull(t *testing.T, app *App) {
	t.Helper()
	res, err := SyncDependencies(context.Background(), app, SyncDependenciesRequest{})
	require.NoError(t, err)
	require.Empty(t, res.Failed, "the real pull installs the bundle")
	require.Equal(t, 1, res.Installed, "the real pull installs the bundle")
}

// canonicalRef renders raw, a hand-joined ctxloom+ reference, in the form
// trust.BundleRef.String produces. An expected reference is derived through
// the renderer rather than spelled: String percent-encodes every component,
// so a joined "ctxloom+file://"+dir matches it only while dir needs no
// escaping — a temp root containing a space is enough to break it.
func canonicalRef(t *testing.T, raw string) string {
	t.Helper()
	ref, err := trust.ParseBundleRef(raw)
	require.NoError(t, err)
	return ref.String()
}

// catalogHas reports whether the catalog read the tools bundle installed
// from repoURL: a remote read's source ref carries the repository it came
// from.
func catalogHas(t *testing.T, cat bundles.Catalog, repoURL string) bool {
	t.Helper()
	_, ok := pulledRead(t, cat, repoURL)
	return ok
}

func pulledRead(t *testing.T, cat bundles.Catalog, repoURL string) (bundles.BundleRead, bool) {
	t.Helper()
	want := canonicalRef(t, "ctxloom+"+repoURL+"//bundles/tools")
	for _, r := range cat.Reads() {
		if r.Provenance == bundles.ProvenanceRemote && r.SourceRef().String() == want {
			return r, true
		}
	}
	return bundles.BundleRead{}, false
}

// TestPullThenSpawn_NextGenerationHoldsThePulledBundle is the slice's
// "pull-then-spawn" gate: after a pull, the next spawn's generation holds the
// pulled bundle and delivers its executable, and the generation before the
// pull is unchanged.
func TestPullThenSpawn_NextGenerationHoldsThePulledBundle(t *testing.T) {
	appDir, repoURL := pulledProject(t)
	app := pulledApp(t, appDir)
	before, err := app.Snapshot(context.Background())
	require.NoError(t, err)
	require.False(t, catalogHas(t, before.Catalog(), repoURL), "precondition: nothing is installed before the pull")

	pull(t, app)

	// The spawn: exactly one Reload, then the plan reads from the generation.
	after, err := app.Reload(context.Background())
	require.NoError(t, err)

	assert.True(t, catalogHas(t, after.Catalog(), repoURL), "the next generation holds the pulled bundle")
	assert.False(t, catalogHas(t, before.Catalog(), repoURL), "the generation before the pull is unchanged")
	assert.Contains(t, after.Config.ResolveBundleMCPServers(nil), "tools-server", "and its executable surface delivers it")
}
