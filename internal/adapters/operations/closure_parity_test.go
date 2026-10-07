package operations

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// shippedProfileProject is a project that composes a remote bundle `kit`
// which SHIPS a profile `extra`, and `extra` composes a second bundle,
// `containers`. The project never uses `extra` — it uses the bundle, not the
// profile — so `containers` is NOT in its dependency closure. A closure that
// took every profile a locked bundle ships as a root would say otherwise, and
// would say so only while kit's tree happened to be installed.
type shippedProfileProject struct {
	appDir, repoDir    string
	kitRef, containRef string
	kitKey, containKey string
	app                *App
}

func newShippedProfileProject(t *testing.T) *shippedProfileProject {
	t.Helper()
	testsupport.Isolate(t)

	repoDir := filepath.Join(t.TempDir(), "source")
	repoURL := "file://" + repoDir
	p := &shippedProfileProject{
		repoDir:    repoDir,
		kitRef:     repoURL + "@bundles/kit",
		containRef: repoURL + "@bundles/containers",
	}

	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	authored := authoredV2(filepath.Join(repoDir, paths.AppDirName))
	require.NoError(t, os.MkdirAll(authored, 0o755))
	bundletree.WriteOS(t, authored, "kit",
		"version: 1.0.0\ndescription: kit\nprofiles:\n  extra:\n    description: shipped, never composed\n    bundles:\n      - "+p.containRef+"\n")
	bundletree.WriteOS(t, authored, "containers", "version: 1.0.0\ndescription: containers\n")
	_, err = wt.Add(repoV2())
	require.NoError(t, err)
	_, err = wt.Commit("seed", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()}})
	require.NoError(t, err)

	p.appDir = filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, p.appDir), 0o755))
	registerTestRemote(t, p.appDir, repoURL)
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, p.appDir), "dev.yaml"),
		[]byte("bundles:\n  - "+p.kitRef+"\n"), 0o644))
	require.NoError(t, os.WriteFile(paths.ConfigPath(p.appDir),
		[]byte("schema_version: 7\ndefault_agent: default\nagents:\n  default:\n    profiles: [dev]\n"), 0o644))

	p.kitKey = string(lockKeyOf(t, p.kitRef))
	p.containKey = string(lockKeyOf(t, p.containRef))
	p.app = pulledApp(t, p.appDir)
	return p
}

func (p *shippedProfileProject) pull(t *testing.T) *SyncDependenciesResult {
	t.Helper()
	res, err := SyncDependencies(context.Background(), p.app, SyncDependenciesRequest{Lock: true})
	require.NoError(t, err)
	require.Empty(t, res.Failed, "the pull installs cleanly")
	return res
}

func (p *shippedProfileProject) cfg(t *testing.T) *config.Config {
	t.Helper()
	snap, err := p.app.Reload(context.Background())
	require.NoError(t, err)
	return snap.Config
}

func (p *shippedProfileProject) lock(t *testing.T) *remote.Lockfile {
	t.Helper()
	lf, err := remote.NewLockfileManager(p.appDir).Load()
	require.NoError(t, err)
	return lf
}

func (p *shippedProfileProject) lockedKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	for _, e := range p.lock(t).AllEntries() {
		keys = append(keys, string(e.Ref))
	}
	sort.Strings(keys)
	return keys
}

func (p *shippedProfileProject) removeTree(t *testing.T, ref string) {
	t.Helper()
	parsed, err := remote.ParseReference(ref)
	require.NoError(t, err)
	dir, err := parsed.LocalTreePath(p.appDir)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(dir))
}

func pinIdentities(pins []PinnedRef) []string {
	out := make([]string, 0, len(pins))
	for _, p := range pins {
		out = append(out, string(p.Identity))
	}
	sort.Strings(out)
	return out
}

// A pin that falls OUT of the closure: pull on an empty lock never pins it,
// check never offers it an update, and upgrade removes it AND says so.
func TestClosureParity_UnreferencedPinAgreedByPullCheckUpgrade(t *testing.T) {
	p := newShippedProfileProject(t)
	ctx := context.Background()

	p.pull(t)
	assert.Equal(t, []string{p.kitKey}, p.lockedKeys(t),
		"pull on an empty lock pins what the project composes — not what a profile SHIPPED inside kit composes")

	// A stale entry for the unreferenced bundle, as an earlier pull left it,
	// and an upstream commit that would give both bundles an update.
	lm := remote.NewLockfileManager(p.appDir)
	lf := p.lock(t)
	kitEntry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, p.containRef), remote.LockEntry{SHA: kitEntry.SHA, URL: kitEntry.URL})
	require.NoError(t, lm.Save(lf))
	addFileToLocalRepo(t, p.repoDir, "README.md", "moved on\n")

	check, err := CheckDependencies(ctx, p.app, CheckDependenciesRequest{})
	require.NoError(t, err)
	var offered []string
	for _, u := range check.Updates {
		offered = append(offered, u.Ref)
	}
	assert.Contains(t, offered, p.kitKey, "check offers the referenced bundle its update")
	assert.NotContains(t, offered, p.containKey, "check never offers an update for an entry outside the closure")

	res, err := UpgradeDependencies(ctx, p.cfg(t), UpgradeRequest{Apply: true})
	require.NoError(t, err)
	assert.Len(t, res.Changes, 1, "only the referenced bundle advances")
	assert.Equal(t, []string{p.containKey}, res.Removed, "upgrade reports the entry it dropped, by name")
	assert.Equal(t, []string{p.kitKey}, p.lockedKeys(t), "the unreferenced entry is gone from the lock")
}

// The closure is a property of what the project composes, not of which bundle
// trees happen to be installed.
func TestClosureParity_IndependentOfInstalledTrees(t *testing.T) {
	p := newShippedProfileProject(t)
	ctx := context.Background()
	p.pull(t)

	withTrees, _, unexpanded, err := FlattenDependencies(ctx, p.cfg(t), nil)
	require.NoError(t, err)
	require.Empty(t, unexpanded)

	p.removeTree(t, p.kitRef)
	withoutTrees, _, unexpanded, err := FlattenDependencies(ctx, p.cfg(t), nil)
	require.NoError(t, err)
	require.Empty(t, unexpanded)

	assert.Equal(t, pinIdentities(withoutTrees), pinIdentities(withTrees),
		"the closure must not change with the set of cached bundle trees")
	assert.Equal(t, []string{p.kitKey}, pinIdentities(withTrees))
}

// A locked bundle whose installed tree is missing is reinstalled AT ITS PIN:
// pull never moves an existing pin, whatever upstream has done since.
func TestPull_MissingTreeReinstallsAtLockedSHA(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)

	before, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)

	moved := addFileToLocalRepo(t, p.repoDir, "README.md", "moved on\n")
	require.NotEqual(t, before.SHA, moved)
	p.removeTree(t, p.kitRef)
	p.cfg(t)

	p.pull(t)

	after, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	assert.Equal(t, before.SHA, after.SHA, "a reinstall keeps the locked commit")

	parsed, err := remote.ParseReference(p.kitRef)
	require.NoError(t, err)
	dir, err := parsed.LocalTreePath(p.appDir)
	require.NoError(t, err)
	_, statErr := os.Stat(dir)
	assert.NoError(t, statErr, "the missing tree is reinstalled")
}

// moveKitPinUnderneath advances kit upstream with a file INSIDE its tree and
// rewrites kit's lock entry to that commit — what a git fast-forward of a
// committed lock.yaml does — leaving the installed tree at the old commit.
// It returns the new pin and the path of the file only that pin carries.
func (p *shippedProfileProject) moveKitPinUnderneath(t *testing.T) (newPin, onlyAtNewPin string) {
	t.Helper()
	lm := remote.NewLockfileManager(p.appDir)
	lf := p.lock(t)
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	newPin = addFileToLocalRepo(t, p.repoDir, repoV2("kit", "NOTES.md"), "only at the new pin\n")
	require.NotEqual(t, entry.SHA, newPin)
	entry.SHA = newPin
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef), entry)
	require.NoError(t, lm.Save(lf))
	p.cfg(t)
	return newPin, filepath.Join(p.kitTree(t), "NOTES.md")
}

func (p *shippedProfileProject) kitTree(t *testing.T) string {
	t.Helper()
	parsed, err := remote.ParseReference(p.kitRef)
	require.NoError(t, err)
	dir, err := parsed.LocalTreePath(p.appDir)
	require.NoError(t, err)
	return dir
}

// kitTreeCommit is the commit kit's installed worktree has checked out.
func (p *shippedProfileProject) kitTreeCommit(t *testing.T) string {
	t.Helper()
	return taskstest.Git(t, p.kitTree(t), nil, "rev-parse", "HEAD")
}

// A pin moved underneath an installed tree (the lock arrived through git) is
// not installed: pull reinstalls the tree AT the pin and reports it as
// reinstalled, rather than calling it skipped while the tree serves the old
// commit.
func TestPull_TreeAtAnotherCommitIsReinstalledAtThePin(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	newPin, onlyAtNewPin := p.moveKitPinUnderneath(t)

	res := p.pull(t)

	assert.Empty(t, res.Skipped, "a tree off its pin is not kept")
	assert.Equal(t, 1, res.Reinstalled, "it is reinstalled at its pin")
	assert.Equal(t, newPin, p.kitTreeCommit(t), "the installed tree is at the pin")
	assert.FileExists(t, onlyAtNewPin)
	after, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	assert.Equal(t, newPin, after.SHA, "the reinstall never moves the pin")

	again := p.pull(t)
	assert.Len(t, again.Skipped, 1, "a tree at its pin is skipped, not refetched")
	assert.Zero(t, again.Reinstalled)
}

// Startup applies the same rule through the same probe: a tree off its pin is
// missing, and the launch reinstalls it at the pin.
func TestSyncOnStartup_TreeAtAnotherCommitIsReinstalledAtThePin(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	newPin, onlyAtNewPin := p.moveKitPinUnderneath(t)

	res, err := SyncOnStartup(context.Background(), p.app)
	require.NoError(t, err)

	assert.Equal(t, 1, res.Reinstalled)
	assert.Equal(t, newPin, p.kitTreeCommit(t))
	assert.FileExists(t, onlyAtNewPin)
}

// The lock rebuild a pull runs after installing drops entries outside the
// closure like upgrade does — and says which.
func TestPull_LockRebuildNamesDroppedEntries(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)

	lf := p.lock(t)
	kitEntry, ok := lf.GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	lf.AddEntry(remote.ItemTypeBundle, lockKeyOf(t, p.containRef), remote.LockEntry{SHA: kitEntry.SHA, URL: kitEntry.URL})
	require.NoError(t, remote.NewLockfileManager(p.appDir).Save(lf))
	// Something must install for the rebuild to run.
	p.removeTree(t, p.kitRef)
	p.cfg(t)

	res := p.pull(t)
	assert.Equal(t, []string{p.containKey}, res.Removed)
	assert.Equal(t, []string{p.kitKey}, p.lockedKeys(t))
}

// `deps pull --force` reinstalls at the pin: only `deps upgrade` moves one.
func TestPull_ForceReinstallsAtLockedSHA(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	before, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)

	moved := addFileToLocalRepo(t, p.repoDir, "README.md", "moved on\n")
	res, err := SyncDependencies(context.Background(), p.app, SyncDependenciesRequest{Lock: true, Force: true})
	require.NoError(t, err)
	require.Empty(t, res.Failed)

	after, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	assert.NotEqual(t, moved, after.SHA)
	assert.Equal(t, before.SHA, after.SHA, "a forced pull reinstalls at the locked commit")
}

// withKitConstraint rewrites the project's profile to ask for kit at expr.
func (p *shippedProfileProject) withKitConstraint(t *testing.T, expr string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, p.appDir), "dev.yaml"),
		[]byte("bundles:\n  - "+p.kitRef+"@"+expr+"\n"), 0o644))
	p.cfg(t)
}

// A changed constraint does not move the pin on a pull — not even when the
// pull reinstalls the item — and the pull says `deps upgrade` applies it.
func TestPull_ChangedConstraintKeepsThePinAndNamesUpgrade(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	before, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)

	moved := addFileToLocalRepo(t, p.repoDir, "README.md", "moved on\n")
	p.withKitConstraint(t, moved)
	p.removeTree(t, p.kitRef) // forces a real Pull of kit, not a skip

	res := p.pull(t)

	after, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)
	assert.Equal(t, before.SHA, after.SHA, "pull never moves an existing pin")
	assert.Equal(t, before.RequestedVersion, after.RequestedVersion, "the pin still records the constraint it was resolved from")
	require.Len(t, res.ConstraintChanges, 1)
	assert.Equal(t, ConstraintChange{Identity: p.kitKey, Pinned: before.RequestedVersion, Declared: moved, SHA: before.SHA}, res.ConstraintChanges[0])
}

// Startup sync is the same rule: a changed constraint is reported, never applied.
func TestSyncOnStartup_ChangedConstraintIsReportedNotApplied(t *testing.T) {
	p := newShippedProfileProject(t)
	p.pull(t)
	before, ok := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	require.True(t, ok)

	moved := addFileToLocalRepo(t, p.repoDir, "README.md", "moved on\n")
	p.withKitConstraint(t, moved)

	res, err := SyncOnStartup(context.Background(), p.app)
	require.NoError(t, err)

	after, _ := p.lock(t).GetEntry(remote.ItemTypeBundle, lockKeyOf(t, p.kitRef))
	assert.Equal(t, before.SHA, after.SHA, "startup never moves an existing pin")
	require.Len(t, res.ConstraintChanges, 1)
	assert.Equal(t, moved, res.ConstraintChanges[0].Declared)
}

// A pull discloses each FIRST pin it creates — everything the bundle brings
// in — and nothing for a pin it kept.
func TestPull_DisclosesFirstPinsOnly(t *testing.T) {
	p := newShippedProfileProject(t)

	first := p.pull(t)
	require.Len(t, first.Changes, 1)
	pc := first.Changes[0]
	assert.Equal(t, p.kitKey, pc.Identity)
	assert.Empty(t, pc.FromSHA, "a first pin")
	assert.Contains(t, pc.Items, ItemChange{Kind: "profile", Name: "extra", Change: ChangeAdded})
	assert.NotEmpty(t, pc.Files)

	p.removeTree(t, p.kitRef) // a reinstall at the same pin
	p.cfg(t)
	again := p.pull(t)
	assert.Empty(t, again.Changes, "a kept pin brings in nothing new")
}

// Startup sync creates first pins with the same disclosure.
func TestSyncOnStartup_DisclosesFirstPins(t *testing.T) {
	p := newShippedProfileProject(t)

	res, err := SyncOnStartup(context.Background(), p.app)
	require.NoError(t, err)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, p.kitKey, res.Changes[0].Identity)
	assert.Empty(t, res.Changes[0].FromSHA)
}
