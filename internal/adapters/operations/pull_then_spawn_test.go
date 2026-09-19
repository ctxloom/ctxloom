package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/convert"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const pulledBundleBody = "version: 1.0.0\ndescription: remote tools bundle\nmcp:\n  tools-server:\n    command: tools\n    args: [serve]\n"

// pulledProject is a project whose default agent selects a profile naming a
// remote bundle that is NOT yet installed: the state before `deps pull`.
func pulledProject(t *testing.T) (appDir, repoURL, bundleRef string) {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(authoredV2(filepath.Join(repoDir, paths.AppDirName)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(authoredV2(filepath.Join(repoDir, paths.AppDirName)), "tools.yaml"), []byte(pulledBundleBody), 0o644))
	_, err = wt.Add(repoV2("tools.yaml"))
	require.NoError(t, err)
	_, err = wt.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()}})
	require.NoError(t, err)
	repoURL = "file://" + repoDir
	bundleRef = repoURL + "@bundles/tools"

	appDir = filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.ProfilesPath(appDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(paths.ProfilesPath(appDir), "dev.yaml"),
		[]byte("bundles:\n  - "+bundleRef+"\n"), 0o644))
	require.NoError(t, os.WriteFile(paths.ConfigPath(appDir),
		[]byte("version: 6\ndefault_agent: default\nagents:\n  default:\n    profiles: [dev]\n"), 0o644))
	return appDir, repoURL, bundleRef
}

// pulledApp is the PRODUCTION composition over the project — the real
// reader, the real remote readers and the real executable trust gate — so
// the gate below is the gate a spawn decides with, not a fixture's.
func pulledApp(t *testing.T, appDir string) *App {
	t.Helper()
	src, err := ComposeSources(Compose{NoCompanions: true, Options: []configload.Option{configload.WithAppDir(appDir)}})
	require.NoError(t, err)
	return NewApp(src, true)
}

// installPulled writes what a pull writes: the installed tree and the
// lockfile entry pinning it, carrying the publisher's retraction record when
// retracted.
func installPulled(t *testing.T, appDir, repoURL, bundleRef string, retracted bool) {
	t.Helper()
	repo, err := git.PlainOpen(repoURL[len("file://"):])
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)

	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	entry := remote.LockEntry{SHA: head.Hash().String(), URL: repoURL, FetchedAt: time.Now().UTC()}
	if retracted {
		entry.Retracted = true
		entry.RetractedReason = "compromised release"
		entry.RetractionCheckedAt = time.Now().UTC()
	}
	lock.AddEntry(remote.ItemTypeBundle, bundleRef, entry)
	require.NoError(t, lm.Save(lock))

	ref, err := remote.ParseReference(bundleRef)
	require.NoError(t, err)
	installDir := ref.LocalTreePath(appDir)
	require.NoError(t, os.MkdirAll(installDir, 0o755))
	src, err := bundles.ParseBundle([]byte(pulledBundleBody))
	require.NoError(t, err)
	store, err := content.NewTreeStore(afero.NewOsFs(), filepath.Dir(installDir), content.Provenance{RepoURL: repoURL})
	require.NoError(t, err)
	require.NoError(t, convert.Convert(context.Background(), store, content.BundleID("tools"), src, convert.Options{}))
	require.NoError(t, os.WriteFile(filepath.Join(installDir, "bundle.yaml"), []byte("version: 1.0.0\ndescription: remote tools bundle\n"), 0o644))
}

// catalogHas reports whether the catalog read the tools bundle installed
// from repoURL: a remote read's source ref carries the repository it came
// from.
func catalogHas(cat bundles.Catalog, repoURL string) bool {
	_, ok := pulledRead(cat, repoURL)
	return ok
}

func pulledRead(cat bundles.Catalog, repoURL string) (bundles.BundleRead, bool) {
	for _, r := range cat.Reads() {
		if r.Provenance == bundles.ProvenanceRemote && strings.HasSuffix(r.SourceRef().String(), strings.TrimPrefix(repoURL, "file://")+"//bundles/tools") {
			return r, true
		}
	}
	return bundles.BundleRead{}, false
}

// gateVerdict asks the generation's Trust about the pulled bundle's MCP
// server — the decision every executable surface is made with.
func gateVerdict(t *testing.T, snap *config.Snapshot, repoURL string) bundles.Verdict {
	t.Helper()
	read, ok := pulledRead(snap.Catalog(), repoURL)
	require.True(t, ok)
	ref, err := bundles.ItemRefFor(read.SourceRef(), trust.KindMCP, "tools-server")
	require.NoError(t, err)
	return bundles.Decide(snap.Trust.Authorizer(), read, ref, []byte("tools-server"), bundles.FormRaw)
}

// TestPullThenSpawn_NextGenerationHoldsThePulledBundleAndItsRetraction is
// the slice's "pull-then-spawn" gate: after a pull, the next spawn's
// generation holds the pulled bundle AND the lockfile's retraction record —
// the generation's Trust is built from the lockfile the pull wrote, so a
// retracted item is withheld on that spawn and never admitted because an
// earlier generation's gate predates the record.
func TestPullThenSpawn_NextGenerationHoldsThePulledBundleAndItsRetraction(t *testing.T) {
	appDir, repoURL, bundleRef := pulledProject(t)
	app := pulledApp(t, appDir)
	before, err := app.Snapshot(context.Background())
	require.NoError(t, err)
	require.False(t, catalogHas(before.Catalog(), repoURL), "precondition: nothing is installed before the pull")

	// The pull: the tree lands and the lockfile pins it, carrying the
	// publisher's retraction of this release.
	installPulled(t, appDir, repoURL, bundleRef, true)

	// The spawn: exactly one Reload, then the plan reads from the generation.
	after, err := app.Reload(context.Background())
	require.NoError(t, err)

	assert.True(t, catalogHas(after.Catalog(), repoURL), "the next generation holds the pulled bundle")
	assert.False(t, catalogHas(before.Catalog(), repoURL), "the generation before the pull is unchanged")
	assert.True(t, after.Trust.Gates(), "the generation's Trust is the real gate, never ungated")
	verdict := gateVerdict(t, after, repoURL)
	assert.False(t, verdict.Allow)
	assert.Equal(t, bundles.ReasonRetracted, verdict.Reason,
		"the retraction the pull recorded is the reason the generation's gate withholds the executable — on the very next spawn")
	servers := after.Config.ResolveBundleMCPServers(nil)
	assert.NotContains(t, servers, "tools-server", "and the executable surface reflects it")
}

// The control: the same pull without a retraction is decided on its review
// state (an unreviewed remote item is pending), so the reason above is the
// record's doing and not a gate that calls every pulled bundle retracted.
func TestPullThenSpawn_NextGenerationAdmitsAnUnretractedPull(t *testing.T) {
	appDir, repoURL, bundleRef := pulledProject(t)
	app := pulledApp(t, appDir)
	_, err := app.Snapshot(context.Background())
	require.NoError(t, err)

	installPulled(t, appDir, repoURL, bundleRef, false)

	after, err := app.Reload(context.Background())
	require.NoError(t, err)
	require.True(t, catalogHas(after.Catalog(), repoURL))
	verdict := gateVerdict(t, after, repoURL)
	assert.NotEqual(t, bundles.ReasonRetracted, verdict.Reason,
		"without a retraction record the gate's reason is the review state, never a retraction")
}
