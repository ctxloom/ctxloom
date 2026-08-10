package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The directory-form pull path. What these tests pin is not "a tree can be
// fetched" — the acceptance journey proves that end to end — but the three
// decisions that are invisible from outside and would each fail silently:
// which shape is probed first, which errors are allowed to trigger the probe,
// and whether the install is a replace or a merge.

const treeTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// treePuller builds a Puller whose lockfile (and therefore whose tree install
// root) lives on an in-memory fs under baseDir.
func treePuller(t *testing.T, fs afero.Fs, baseDir string, tf TreeFetchFunc) *Puller {
	t.Helper()
	opts := []PullerOption{
		WithLockfileManager(NewLockfileManager(baseDir, WithLockfileFS(fs))),
	}
	if tf != nil {
		opts = append(opts, WithTreeFetcher(tf))
	}
	return NewPuller(nil, AuthConfig{}, opts...)
}

func treeRef(t *testing.T) *Reference {
	t.Helper()
	ref, err := ParseReference("https://github.com/trent/atelier@bundles/atelier")
	require.NoError(t, err)
	return ref
}

// TestFetchItemBytes_PrefersTheSingleFileAndNeverProbesTheTree pins the
// ordering. A tree probe in front would issue an extra listing on every pull in
// the world, and would let a stray directory beside a real bundle.yaml decide
// which of the two shapes got installed.
func TestFetchItemBytes_PrefersTheSingleFileAndNeverProbesTheTree(t *testing.T) {
	fetcher := NewMockFetcher().WithFile(".ctxloom/content/bundles/atelier.yaml", []byte("version: \"1.0.0\"\n"))
	probed := false
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", func(context.Context, Fetcher, string, string, string, string, string) (map[string]TreeFile, error) {
		probed = true
		return nil, nil
	})

	content, tree, _, err := p.fetchItemBytes(t.Context(), fetcher, "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/atelier.yaml", treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.NoError(t, err)
	assert.Equal(t, "version: \"1.0.0\"\n", string(content))
	assert.Nil(t, tree, "a single-file bundle must not report a tree")
	assert.False(t, probed, "the tree was probed even though the single file was present")
}

// TestFetchItemBytes_DoesNotProbeTheTreeOnANonNotFoundError: falling through on
// an auth or transport failure would convert one diagnosable error into a
// second, more confusing one about a directory nobody asked for.
func TestFetchItemBytes_DoesNotProbeTheTreeOnANonNotFoundError(t *testing.T) {
	boom := errors.New("tls handshake failed")
	fetcher := NewMockFetcher()
	fetcher.FetchFileErr = boom
	probed := false
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", func(context.Context, Fetcher, string, string, string, string, string) (map[string]TreeFile, error) {
		probed = true
		return nil, nil
	})

	_, _, _, err := p.fetchItemBytes(t.Context(), fetcher, "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/atelier.yaml", treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.Error(t, err)
	assert.ErrorIs(t, err, boom, "the transport error must reach the caller unchanged")
	assert.False(t, probed, "a non-not-found failure must not be reinterpreted as a missing directory")
}

// TestFetchItemBytes_FallsBackToTheTreeAndTakesItsManifestAsTheBundleBytes.
func TestFetchItemBytes_FallsBackToTheTreeAndTakesItsManifestAsTheBundleBytes(t *testing.T) {
	want := map[string]TreeFile{
		BundleManifestName:               {Data: []byte("version: \"2.0.0\"\n")},
		"skills/reviewer/scripts/run.sh": {Data: []byte("#!/bin/sh\n"), DeclaredExecutable: true},
	}
	var gotRoot string
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", func(_ context.Context, _ Fetcher, _, _, root, _, _ string) (map[string]TreeFile, error) {
		gotRoot = root
		return want, nil
	})

	content, tree, treeRoot, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/atelier.yaml", treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.NoError(t, err)
	assert.Equal(t, ".ctxloom/content/bundles/atelier", gotRoot, "the directory form is the file path minus its extension")
	assert.Equal(t, ".ctxloom/content/bundles/atelier", treeRoot)
	assert.Equal(t, "version: \"2.0.0\"\n", string(content), "a tree's bundle.yaml is what stands in for the single file's bytes")
	assert.Len(t, tree, 2)
}

// TestFetchItemBytes_RefusesATreeWithNoManifest. A tree with no bundle.yaml
// would install as a pile of files under a bundle's identity that nothing could
// ever load — the silent no-op this codebase is prone to.
func TestFetchItemBytes_RefusesATreeWithNoManifest(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", func(context.Context, Fetcher, string, string, string, string, string) (map[string]TreeFile, error) {
		return map[string]TreeFile{"fragments/x.md": {Data: []byte("hi")}}, nil
	})

	_, _, _, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/atelier.yaml", treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.Error(t, err)
	assert.Contains(t, err.Error(), BundleManifestName)
}

// TestFetchItemBytes_NonBundleItemsNeverProbeATree: only bundles have a
// directory form, so anything else that is missing must say so plainly. The
// item type is spelled as a literal rather than a named constant because
// ItemTypeBundle is currently the ONLY one — the guard exists so that adding a
// second type does not silently inherit the bundle's directory fallback.
func TestFetchItemBytes_NonBundleItemsNeverProbeATree(t *testing.T) {
	probed := false
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", func(context.Context, Fetcher, string, string, string, string, string) (map[string]TreeFile, error) {
		probed = true
		return nil, nil
	})

	_, _, _, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/profiles/x.yaml", treeTestSHA, PullOptions{ItemType: ItemType("profile")})

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound)
	assert.False(t, probed)
}

// TestFetchItemBytes_WithoutAWalkerSaysSoRatherThanReportingOnlyTheMissingFile.
// A bare "not found" against a repo that DOES publish the directory form is the
// diagnostic that cost this capability its first attempt.
func TestFetchItemBytes_WithoutAWalkerSaysSoRatherThanReportingOnlyTheMissingFile(t *testing.T) {
	p := treePuller(t, afero.NewMemMapFs(), ".ctxloom", nil)

	_, _, _, err := p.fetchItemBytes(t.Context(), NewMockFetcher(), "trent", "atelier", "https://github.com/trent/atelier",
		treeRef(t), ".ctxloom/content/bundles/atelier.yaml", treeTestSHA, PullOptions{ItemType: ItemTypeBundle})

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound)
	assert.Contains(t, err.Error(), ".ctxloom/content/bundles/atelier",
		"the error must name the directory form that could not be checked")
}

// TestInstallTree_WritesEveryFileAtItsDeclaredMode. The exec bit is the whole
// reason TreeFile carries a mode at all: a skill script delivered 0644 is
// content the agent cannot use, with nothing reporting a failure.
func TestInstallTree_WritesEveryFileAtItsDeclaredMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	p := treePuller(t, fs, ".ctxloom", nil)
	ref := treeRef(t)

	dir, err := p.installTree(ref, PullOptions{}, &fetchedItem{
		localName: ref.CanonicalString(),
		tree: map[string]TreeFile{
			BundleManifestName:               {Data: []byte("version: \"1.0.0\"\n")},
			"skills/reviewer/SKILL.md":       {Data: []byte("skill\n")},
			"skills/reviewer/scripts/run.sh": {Data: []byte("#!/bin/sh\n"), DeclaredExecutable: true, CommittedExecutable: true},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, ref.LocalTreePath(".ctxloom"), dir)

	body, rerr := afero.ReadFile(fs, filepath.Join(dir, "skills", "reviewer", "SKILL.md"))
	require.NoError(t, rerr)
	assert.Equal(t, "skill\n", string(body))

	script, serr := fs.Stat(filepath.Join(dir, "skills", "reviewer", "scripts", "run.sh"))
	require.NoError(t, serr)
	assert.Equal(t, os.FileMode(0o755), script.Mode().Perm(), "the declared exec bit did not survive the install")

	plain, perr := fs.Stat(filepath.Join(dir, "skills", "reviewer", "SKILL.md"))
	require.NoError(t, perr)
	assert.Equal(t, os.FileMode(0o644), plain.Mode().Perm(), "a non-executable file must not be installed executable")
}

// TestInstallTree_TheDeclarationWinsOverTheCommittedMode is the divergence this
// whole split exists for, in both directions.
//
// A publisher who commits scripts/run.sh 0755 without declaring it produces a
// tree whose generated manifest says 0644 (bundles.ReadTree builds it from the
// sidecar, not from a filesystem). Installing at git's mode put a 0755 file
// next to a 0644 claim, and the consumer refused the whole package as an
// integrity mismatch. The declaration reaching disk is what makes the two
// agree — and the file that is DECLARED executable gets its bit even when git
// never recorded one, which is how a Windows-authored package still ships a
// runnable script.
func TestInstallTree_TheDeclarationWinsOverTheCommittedMode(t *testing.T) {
	fs := afero.NewMemMapFs()
	p := treePuller(t, fs, ".ctxloom", nil)
	ref := treeRef(t)

	dir, err := p.installTree(ref, PullOptions{}, &fetchedItem{
		localName: ref.CanonicalString(),
		tree: map[string]TreeFile{
			BundleManifestName: {Data: []byte("version: \"1.0.0\"\n")},
			// Committed 0755 upstream, never declared: lands non-executable.
			"skills/reviewer/scripts/undeclared.sh": {Data: []byte("#!/bin/sh\n"), CommittedExecutable: true},
			// Declared, committed 0644 (an authoring filesystem with no exec
			// bit): lands executable.
			"skills/reviewer/scripts/declared.sh": {Data: []byte("#!/bin/sh\n"), DeclaredExecutable: true},
		},
	})
	require.NoError(t, err)

	undeclared, err := fs.Stat(filepath.Join(dir, "skills", "reviewer", "scripts", "undeclared.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), undeclared.Mode().Perm(),
		"an undeclared file installed at git's mode is what makes the manifest and the tree disagree")

	declared, err := fs.Stat(filepath.Join(dir, "skills", "reviewer", "scripts", "declared.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), declared.Mode().Perm(),
		"the declaration is the publisher's whole statement about executability; git's blob mode is not")
}

// TestInstallTree_SaysWhenACommittedExecutableIsUndeclared. Applying the
// declaration makes everything downstream CONSISTENT — the file is 0644, the
// manifest says 0644, verification passes — and therefore silent: the model is
// handed a script it cannot run and nothing reports a failure. The install is
// the last point at which both facts are in scope, so it is the only place the
// divergence can be named.
func TestInstallTree_SaysWhenACommittedExecutableIsUndeclared(t *testing.T) {
	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	fs := afero.NewMemMapFs()
	p := treePuller(t, fs, ".ctxloom", nil)
	ref := treeRef(t)

	_, err := p.installTree(ref, PullOptions{}, &fetchedItem{
		localName: ref.CanonicalString(),
		treeRoot:  ".ctxloom/content/bundles/atelier",
		tree: map[string]TreeFile{
			BundleManifestName:                    {Data: []byte("version: \"1.0.0\"\n")},
			"skills/reviewer/scripts/declared.sh": {Data: []byte("#!/bin/sh\n"), DeclaredExecutable: true, CommittedExecutable: true},
			"skills/reviewer/scripts/orphan.sh":   {Data: []byte("#!/bin/sh\n"), CommittedExecutable: true},
		},
	})
	require.NoError(t, err)

	got := warnings.String()
	assert.Contains(t, got, ".ctxloom/content/bundles/atelier/skills/reviewer/scripts/orphan.sh",
		"the warning must name the file as the PUBLISHER sees it, not as a cache path they have never heard of")
	assert.Contains(t, got, "DECLARED NON-EXECUTABLE")
	assert.Contains(t, got, "executable:", "and the declaration to add")
	assert.NotContains(t, got, "declared.sh",
		"a file whose declaration and committed mode agree has nothing to report")
}

// TestInstallTree_ReplacesRatherThanMerges. A merge would leave a file the
// publisher DELETED upstream sitting in the consumer's tree forever, still
// enumerated by every walk that reads the bundle — and, for hooks and MCP
// servers, still applied.
func TestInstallTree_ReplacesRatherThanMerges(t *testing.T) {
	fs := afero.NewMemMapFs()
	p := treePuller(t, fs, ".ctxloom", nil)
	ref := treeRef(t)
	item := &fetchedItem{localName: ref.CanonicalString()}

	item.tree = map[string]TreeFile{
		BundleManifestName:    {Data: []byte("version: \"1.0.0\"\n")},
		"hooks/old/gone.yaml": {Data: []byte("type: command\n")},
	}
	dir, err := p.installTree(ref, PullOptions{}, item)
	require.NoError(t, err)
	stale := filepath.Join(dir, "hooks", "old", "gone.yaml")
	exists, _ := afero.Exists(fs, stale)
	require.True(t, exists)

	item.tree = map[string]TreeFile{BundleManifestName: {Data: []byte("version: \"2.0.0\"\n")}}
	_, err = p.installTree(ref, PullOptions{}, item)
	require.NoError(t, err)

	exists, _ = afero.Exists(fs, stale)
	assert.False(t, exists, "a file removed upstream outlived the re-pull that removed it")
}

// TestReadableEntry_RefusesATreeBundleWithAnActionableSentinel. Collapsing this
// into a not-found prints a fix ("run deps pull") that cannot fix anything:
// the pull already succeeded and the bytes are on disk.
func TestReadableEntry_RefusesATreeBundleWithAnActionableSentinel(t *testing.T) {
	name := "https://github.com/trent/atelier@bundles/atelier"
	r := NewBundleReader(nil, nil, AuthConfig{}, &Lockfile{
		Bundles: map[string]LockEntry{name: {SHA: treeTestSHA, Tree: true}},
	})

	_, err := r.ReadBundleBytes(t.Context(), name)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTreeBundleUnreadable)
	assert.NotErrorIs(t, err, errs.ErrRemoteContentNotFound,
		"a tree bundle that pulled fine must never read as missing remote content")
	assert.Contains(t, err.Error(), treeTestSHA)
}
