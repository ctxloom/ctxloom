package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/ctxloom/ctxloom/internal/core/config"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const (
	treeBase      = "/proj/.ctxloom"
	treeCanonical = "ctxloom+git://github.com/acme/ctx//bundles/atelier"
)

// stageInstalledTree writes a directory-form bundle where `deps pull` installs
// one, and returns the config reading it plus the tree store.
func stageInstalledTree(t *testing.T) (*config.Config, *content.TreeStore, content.Bundle, afero.Fs) {
	t.Helper()
	ctx := context.Background()
	fsys := afero.NewMemMapFs()

	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	require.NoError(t, fsys.MkdirAll(dir, 0o755))

	store, err := content.NewTreeStore(fsys, filepath.Dir(dir), content.Provenance{RepoURL: "https://github.com/acme/ctx"})
	require.NoError(t, err)

	src := &bundles.Bundle{
		Fragments: map[string]bundles.BundleFragment{"house-style": {
			ItemBody: bundles.ItemBody{
				Content: "FRAG-BODY",
			},
		}},
		Hooks: bundles.BundleHooks{
			PostFileEdit: []bundles.BundleHook{
				{Type: "command", Command: "echo stamp"},
				{Type: "command", Command: "echo audit"},
			},
		},
	}
	bundletree.WriteBundle(t, fsys, filepath.Dir(dir), filepath.Base(dir), src)
	testsupport.WriteFile(t, fsys,
		filepath.Join(dir, bundles.DirectoryFormManifest), []byte("version: 1.0.0\ndescription: atelier\n"), 0o644)

	tree, err := store.Open(ctx, content.BundleID(filepath.Base(dir)))
	require.NoError(t, err)

	c := config.NewFixture(config.Fixture{AppPaths: []string{treeBase}})
	c.SetRoot(safefs.NewMem(fsys))
	return c, store, tree, fsys
}

// readTreeBundle drives the reader the Config builds for one lockfile tree
// entry, and returns both halves a caller cares about: the bundle document, and
// the read.
func readTreeBundle(t *testing.T, c *config.Config, ctx context.Context, canonical ident.BundleKey, entry remote.LockEntry) (*bundles.Bundle, bundles.BundleRead, error) {
	t.Helper()
	reader, err := treeBundleReader(c, canonical, entry)
	if err != nil {
		return nil, bundles.BundleRead{}, err
	}
	reads, err := reader.Read(ctx)
	if err != nil {
		return nil, bundles.BundleRead{}, err
	}
	require.Len(t, reads, 1, "one lockfile entry is one bundle")
	return reads[0].Bundle, reads[0], nil
}

func treeEntry() remote.LockEntry {
	return remote.LockEntry{SHA: "0123456789abcdef", URL: "https://github.com/acme/ctx"}
}

// The point of the whole change: a pulled tree becomes a bundle document, with
// its hooks in DECLARED order rather than the directory walk's alphabetical one.
func TestLoadTreeBundle_ReadsTheInstalledTreeIntoABundle(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)

	b, read, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry())
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", b.Version)
	require.Contains(t, b.Fragments, "house-style")
	assert.Equal(t, "FRAG-BODY", b.Fragments["house-style"].Content)
	require.Len(t, b.Hooks.PostFileEdit, 2)
	assert.Equal(t, "echo stamp", b.Hooks.PostFileEdit[0].Command)

	// A DIRECTORY-form (tree-form) bundle's typed source ref. It is the only
	// shape a repoFSReader reads at all now — the single-document path it used
	// to fall back to is gone — and nothing else in the suite reaches this call
	// site (readTreeForm).
	wantTyped, err := ident.GitRef("github.com", "/acme/ctx", "atelier")
	require.NoError(t, err)
	assert.Equal(t, wantTyped, read.SourceRef(),
		"a tree-form bundle's typed source ref is GitRef(host, repo path, bundle) from its own canonical ref")
}

// Skills are the reason the INSTALLED tree is read rather than the clone at the
// pinned SHA: FSDir must resolve to a real directory or a skill package cannot
// be loaded at all.
func TestLoadTreeBundle_PathResolvesToTheInstalledDirectorySoSkillsCanLoad(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)

	b, _, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry())
	require.NoError(t, err)

	dir, err := b.FSDir()
	require.NoError(t, err, "a tree bundle must have a resolvable directory, unlike a single-file remote bundle")
	want, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	assert.Equal(t, want, dir)
}

// loadFailureFinding runs the startup report over one failed read and returns
// the single finding it raised — the fix line is what the user is told to do.
func loadFailureFinding(t *testing.T, err error) report.Finding {
	t.Helper()
	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	reportBundleLoadFailures(map[ident.BundleKey]error{treeCanonical: err})
	found := strictness.Since(mark)
	require.Len(t, found, 1)
	return found[0]
}

// A lockfile that records a tree which is not on disk must say THAT, not
// "the tree read path does not exist" — the fix is a pull, and the message has
// to point at it.
func TestLoadTreeBundle_MissingTreeNamesThePathAndTheFix(t *testing.T) {
	fsys := afero.NewMemMapFs()
	c := config.NewFixture(config.Fixture{AppPaths: []string{treeBase}})
	c.SetRoot(safefs.NewMem(fsys))

	_, _, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "atelier")
	assert.Contains(t, loadFailureFinding(t, err).Remedy, "deps pull")
}

// treeBundleReaders must claim exactly the entries the byte reader refused for
// being tree-shaped, and leave every other failure for the ordinary report.
func TestTreeBundleReaders_ClaimsTreeRefusalsAndLeavesOtherFailuresAlone(t *testing.T) {
	c, _, _, _ := stageInstalledTree(t)

	lock := &remote.Lockfile{Bundles: map[ident.BundleKey]remote.LockEntry{treeCanonical: treeEntry()}}
	other := assert.AnError
	failures := map[ident.BundleKey]error{
		treeCanonical: remote.ErrTreeBundleUnreadable,
		"https://github.com/acme/ctx@bundles/other": other,
	}

	readers := treeBundleReaders(c, lock, failures)

	require.Len(t, readers, 1, "the tree entry must get a reader")
	reads, err := readers[0].Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1)
	assert.Equal(t, treeCanonical, reads[0].DisplayName(), "the canonical ref is the resolution identity")
	assert.Equal(t, treeCanonical, reads[0].Bundle.Name)
	assert.NotContains(t, failures, treeCanonical, "a claimed tree is no longer a failure")
	assert.Equal(t, other, failures["https://github.com/acme/ctx@bundles/other"],
		"a failure that is not a tree refusal must be left untouched")
}

// TestTreeBundleReaders_MalformedEntryIsSkippedGoodOneStillLoads is the
// aggregate-level counterpart of TestLoadTreeBundle_MissingTreeNamesThePathAndTheFix:
// with format v2 publishing only trees, this is the ONLY read path a real
// lockfile entry resolves through (see the removal note on
// TestLoadRemoteBundleSeed_FullLoad in loadremotebundleseed_test.go), so it
// must tolerate one entry's manifest failing to parse without losing every
// other entry — the same fault-tolerance the retired single-file seed path
// pinned.
func TestTreeBundleReaders_MalformedEntryIsSkippedGoodOneStillLoads(t *testing.T) {
	const brokenCanonical = "ctxloom+git://github.com/acme/ctx//bundles/broken"
	c, _, _, fsys := stageInstalledTree(t)

	brokenDir, err := treeBundleDir(treeBase, brokenCanonical)
	require.NoError(t, err)
	require.NoError(t, fsys.MkdirAll(brokenDir, 0o755))
	// A leading tab is invalid YAML, so ParseBundle rejects this one.
	testsupport.WriteFileString(t, fsys, filepath.Join(brokenDir, bundles.DirectoryFormManifest),
		"\tnot: valid yaml\n", 0o644)

	lock := &remote.Lockfile{Bundles: map[ident.BundleKey]remote.LockEntry{
		treeCanonical:   treeEntry(),
		brokenCanonical: {SHA: "0123456789abcdef", URL: "https://github.com/acme/ctx"},
	}}
	failures := map[ident.BundleKey]error{}

	readers := treeBundleReaders(c, lock, failures)
	require.Len(t, readers, 2, "both entries have an installed directory, so both get a reader — "+
		"the manifest is only parsed on Read")
	assert.Empty(t, failures, "treeBundleReaders itself does not parse manifests, so neither entry fails yet")

	reads := bundles.NewLoader(readers...).Reads()

	names := make([]string, 0, len(reads))
	for _, r := range reads {
		names = append(names, r.DisplayName())
	}
	assert.Equal(t, []string{treeCanonical}, names,
		"the malformed tree contributes nothing to the aggregate; the well-formed one still loads")
}

// stageLoaderFormTree writes the RETIRED loader directory form: bundle.yaml
// declares `skills:` inline and the skill's files live in skills/<name>/.
//
// It is staged here only so the REFUSAL can be asserted. This shape is being
// removed, not supported — see TestLoadTreeBundle_RetiredLoaderDirectoryFormIsRefused.
func stageLoaderFormTree(t *testing.T) (*config.Config, afero.Fs, string) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	require.NoError(t, fsys.MkdirAll(filepath.Join(dir, "skills", "good-night"), 0o755))
	testsupport.WriteFile(t, fsys, filepath.Join(dir, "bundle.yaml"), []byte(
		"version: 1.0.0\ndescription: unattended\nskills:\n  good-night:\n    notes: overnight\n"), 0o644)
	testsupport.WriteFile(t, fsys, filepath.Join(dir, "skills", "good-night", "SKILL.md"),
		[]byte("---\nname: good-night\ndescription: d\n---\n\nGOOD-NIGHT-BODY\n"), 0o644)

	c := config.NewFixture(config.Fixture{AppPaths: []string{treeBase}})
	c.SetRoot(safefs.NewMem(fsys))
	return c, fsys, dir
}

// The loader directory form — bundle.yaml carrying inline items beside a
// skills/ subtree — is RETIRED. A published bundle in that shape is refused
// rather than accommodated, because accommodating it is a backward-compat shim
// and this project's documented upgrade path is to migrate the content.
//
// The refusal has to NAME the migration, or a publisher who has never heard of
// the tree form reads it as a bug in ctxloom rather than as work they owe.
func TestLoadTreeBundle_RetiredLoaderDirectoryFormIsRefused(t *testing.T) {
	c, _, _ := stageLoaderFormTree(t)

	_, _, err := readTreeBundle(t, c, context.Background(), treeCanonical, treeEntry())
	require.Error(t, err, "the retired shape must not load silently")
	assert.Contains(t, err.Error(), "skills",
		"the refusal must name the inline key that makes it the retired shape")
}
