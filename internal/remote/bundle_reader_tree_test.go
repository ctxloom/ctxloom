package remote

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// The canonical lockfile key these tests read, and the tree root it must
// resolve to. The two are stated separately ON PURPOSE: the whole point of the
// tree-root probe is that a reader looks exactly where the installer wrote, and
// a test that derived the expected root with the production helper would agree
// with any derivation, including a wrong one.
const (
	treeReadCanonical = "https://github.com/trent/atelier@bundles/atelier"
	treeReadRoot      = ".ctxloom/content/bundles/v2/atelier"
)

// treeCapture records what the reader asked its tree fetcher for, and serves a
// canned tree back FROM ONE ROOT ONLY.
//
// Serving every root is what makes such a double blind to the thing these tests
// exist to pin: the reader probes each format's root in turn, so a stub that
// answers whatever it is handed reports that the reader looked in the right
// place no matter where it looked.
type treeCapture struct {
	owner, repo, root, sha, repoURL string
	calls                           int
	at                              string
	tree                            map[string]TreeFile
}

func (c *treeCapture) fetch(_ context.Context, _ Fetcher, owner, repo, root, sha, repoURL string) (map[string]TreeFile, error) {
	c.calls++
	if root != c.at {
		return nil, fmt.Errorf("no tree at %s", root)
	}
	c.owner, c.repo, c.root, c.sha, c.repoURL = owner, repo, root, sha, repoURL
	return c.tree, nil
}

// treeReaderOver builds a reader pinned to one directory-form entry, served by
// tcap. The fetcher factory hands back a mock the tree seam never consults —
// it exists because the read path resolves a transport before it knows the
// bundle's form, exactly as the single-file path does.
func treeReaderOver(t *testing.T, tcap *treeCapture, sha string) *BundleReader {
	t.Helper()
	return NewBundleReader(nil,
		func(string, AuthConfig) (Fetcher, error) { return NewMockFetcher(), nil },
		AuthConfig{},
		&Lockfile{Bundles: map[string]LockEntry{
			treeReadCanonical: {SHA: sha, URL: "https://github.com/trent/atelier"},
		}},
		WithReaderTreeFetcher(tcap.fetch),
	)
}

// The tree's bundle.yaml IS the bundle's bytes. A consumer that can read a
// single-file bundle must get the same kind of answer here, out of the same
// call, or nothing downstream can treat the two forms alike — which is what
// left every skill hand-copied into each project.
func TestBundleReader_TreeBundleServesItsManifestAsTheBundleBytes(t *testing.T) {
	manifest := []byte("version: 1.2.3\ndescription: atelier\n")
	tcap := &treeCapture{at: treeReadRoot, tree: map[string]TreeFile{
		BundleManifestName:           {Data: manifest},
		"skills/good-night/SKILL.md": {Data: []byte("---\nname: good-night\n---\n")},
		"skills/good-night/run.sh":   {Data: []byte("#!/bin/sh\n"), DeclaredExecutable: true},
	}}
	r := treeReaderOver(t, tcap, treeTestSHA)

	data, err := r.ReadBundleBytes(t.Context(), treeReadCanonical)

	require.NoError(t, err)
	assert.Equal(t, manifest, data, "the manifest bytes themselves, not some other file of the tree")

	// The reader must look where the INSTALLER wrote, at the pin the lockfile
	// holds. Either being wrong yields a plausible bundle read from the wrong
	// place or the wrong commit, with no error anywhere.
	assert.Equal(t, treeReadRoot, tcap.root, "the tree root is the single-file path minus .yaml")
	assert.Equal(t, treeTestSHA, tcap.sha, "a pinned reader must read the tree at the LOCKED sha")
	assert.Equal(t, "trent", tcap.owner)
	assert.Equal(t, "atelier", tcap.repo)
	assert.Equal(t, "https://github.com/trent/atelier", tcap.repoURL)
}

// A tree bundle's detached signature is bundle.yaml.sig INSIDE the tree — the
// sibling of the document just read, the same convention the single-file form
// uses. Serving anything else here would check a signature over bytes nobody
// read.
func TestBundleReader_TreeBundleSignatureIsTheManifestSibling(t *testing.T) {
	sig := []byte("-----BEGIN SSH SIGNATURE-----\natelier\n")
	tcap := &treeCapture{at: treeReadRoot, tree: map[string]TreeFile{
		BundleManifestName:                   {Data: []byte("version: 1.2.3\n")},
		BundleManifestName + SignatureSuffix: {Data: sig},
		"skills/good-night/SKILL.md":         {Data: []byte("x")},
	}}
	r := treeReaderOver(t, tcap, treeTestSHA)

	data, err := r.ReadBundleSignature(t.Context(), treeReadCanonical)

	require.NoError(t, err)
	assert.Equal(t, sig, data, "the signature bytes, not the manifest they cover")
}

// An ABSENT signature means UNSIGNED, and it has to mean that identically in
// both bundle forms: a tree bundle reported as broken where a single-file one
// is reported as unsigned would withhold content for a reason that is not true.
func TestBundleReader_TreeBundleWithNoSignatureReadsAsUnsigned(t *testing.T) {
	tcap := &treeCapture{at: treeReadRoot, tree: map[string]TreeFile{
		BundleManifestName: {Data: []byte("version: 1.2.3\n")},
	}}
	r := treeReaderOver(t, tcap, treeTestSHA)

	_, err := r.ReadBundleSignature(t.Context(), treeReadCanonical)

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound,
		"an unsigned tree bundle must signal absence the way an unsigned single-file bundle does")
	assert.NotErrorIs(t, err, ErrTreeBundleUnreadable,
		"nothing about the reader is missing — only the signature is")
}

// A tree with no bundle.yaml is not a bundle. Reporting that — and naming the
// file — is the difference between a diagnosable publisher mistake and a bundle
// that silently resolves to nothing.
func TestBundleReader_TreeWithNoManifestIsNotABundle(t *testing.T) {
	tcap := &treeCapture{at: treeReadRoot, tree: map[string]TreeFile{
		"skills/good-night/SKILL.md": {Data: []byte("x")},
	}}
	r := treeReaderOver(t, tcap, treeTestSHA)

	_, err := r.ReadBundleBytes(t.Context(), treeReadCanonical)

	require.Error(t, err)
	assert.Contains(t, err.Error(), BundleManifestName, "the refusal must name the file the publisher owes")
	assert.NotErrorIs(t, err, ErrTreeBundleUnreadable,
		"this is the publisher's gap, not a missing read surface in ctxloom")
}

// The pin is the security control on this path too. An entry with no SHA must
// be refused BEFORE any tree is walked, or a blank ref resolves to the default
// branch tip and a pinned read silently becomes a latest read.
func TestBundleReader_TreeBundleWithNoPinIsRefusedBeforeAnyWalk(t *testing.T) {
	tcap := &treeCapture{at: treeReadRoot, tree: map[string]TreeFile{BundleManifestName: {Data: []byte("version: 1.2.3\n")}}}
	r := treeReaderOver(t, tcap, "")

	_, err := r.ReadBundleBytes(t.Context(), treeReadCanonical)

	require.Error(t, err)
	assert.Zero(t, tcap.calls, "an unpinned entry must never reach the tree walker at all")
}

// TestBundleReader_ReadsATreePublishedUnderTheFormatV2Root. A bundles root's
// `v` is the FORMAT VERSION, so a migrated tree sits under the next format's
// root while the reader's lockfile key still names the single-file path. A
// reader that probed only the path-derived root would report a bundle it had
// already pulled as unreadable.
func TestBundleReader_ReadsATreePublishedUnderTheFormatV2Root(t *testing.T) {
	manifest := []byte("version: 1.2.3\ndescription: migrated\n")
	tcap := &treeCapture{at: formatV2TreeRoot(), tree: map[string]TreeFile{
		BundleManifestName: {Data: manifest},
	}}
	r := treeReaderOver(t, tcap, treeTestSHA)

	data, err := r.ReadBundleBytes(t.Context(), treeReadCanonical)

	require.NoError(t, err)
	assert.Equal(t, manifest, data)
	assert.Equal(t, formatV2TreeRoot(), tcap.root, "the reader must have read from the root that answered")
	assert.Equal(t, treeTestSHA, tcap.sha, "a probe across roots must not lose the pin")
}
