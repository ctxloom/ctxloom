package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// trustPublisher writes the project allowed_signers line that makes signer a
// key this machine trusts to PUBLISH. Without it, a stale signature is merely
// "unsigned to you" (signing.VerifyPublisher's quiet outcome) and the tamper
// case under test is never reached — the same trap the j001900 journey fell into.
func trustPublisher(t *testing.T, baseDir string, signer ssh.Signer) {
	t.Helper()
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	line := "publisher@example.com namespaces=\"" + signing.NamespacePublish + "\" " +
		string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "allowed_signers"), []byte(line), 0o644))
}

// demoTreeFiles composes a tree-form bundle named demo — bundle.yaml, one
// fragment file, its SHA256SUMS manifest and the .sigs/ entry signer made
// over it — and returns every file keyed by its path relative to the tree
// root, so a caller can commit the tree into a repository file by file.
func demoTreeFiles(t *testing.T, signer ssh.Signer, fragBody string) map[string]string {
	t.Helper()
	fsys := afero.NewMemMapFs()
	const root = "/stage"
	st, err := content.NewTreeStore(fsys, root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "keeper"},
		signing.FormRaw,
		content.Fragment{Name: "keeper", ItemMeta: content.ItemMeta{Body: fragBody}}))
	require.NoError(t, st.PutRootFile(context.Background(), "demo", bundles.DirectoryFormManifest, []byte("version: \"1.0.0\"\n")))
	tree, err := st.Open(context.Background(), "demo")
	require.NoError(t, err)
	require.NoError(t, attest.SignBundle(context.Background(), st, tree, treeRelease(t, tree), signer))

	files := map[string]string{}
	dir := filepath.Join(root, "demo")
	require.NoError(t, afero.Walk(fsys, dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, rerr := afero.ReadFile(fsys, p)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	}))
	require.NotEmpty(t, files)
	return files
}

// commitTree writes every file of a composed tree under demo's tree root and
// commits them as ONE commit (init creates the repository first), returning
// the commit. Old signature entries are removed first, so a re-signed tree
// carries exactly its own .sigs/ entries.
func commitTree(t *testing.T, src string, files map[string]string, init bool) string {
	t.Helper()
	var repo *git.Repository
	var err error
	if init {
		require.NoError(t, os.MkdirAll(src, 0o755))
		repo, err = git.PlainInit(src, false)
	} else {
		repo, err = git.PlainOpen(src)
	}
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	root := repoV2("demo")
	_ = os.RemoveAll(filepath.Join(src, root, content.SigDirName))
	for rel, data := range files {
		full := filepath.Join(src, root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(data), 0o644))
	}
	require.NoError(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	sha, err := wt.Commit("tree", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return sha.String()
}

// signedBundleRepo stages a repository holding the tree-form bundle demo,
// signed through its ONE signature (the SHA256SUMS manifest and its .sigs/
// entry) by a key the project trusts, and returns the project dir, the
// repository, the ref, the signer and the commit whose signature verifies.
func signedBundleRepo(t *testing.T, fragBody string) (baseDir, src, ref string, signer ssh.Signer, verified string) {
	t.Helper()
	tmp := t.TempDir()
	baseDir = filepath.Join(tmp, ".ctxloom")
	src = filepath.Join(tmp, "src")
	signer = testSigner(t)
	verified = commitTree(t, src, demoTreeFiles(t, signer, fragBody), true)
	trustPublisher(t, baseDir, signer)
	ref = "file://" + src + "@bundles/demo" // version-less → track the default branch
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")
	return baseDir, src, ref, signer, verified
}

// THE DECIDED BEHAVIOUR (taskloom unearned-cornea): a publisher edits a signed
// bundle and pushes without re-signing, so the newest commit carries bytes the
// signature beside them does not cover. `deps upgrade` must NOT move the pin
// onto it.
//
// What is at stake is not the new content — that was always going to be
// withheld as tampered. It is the OLD content: advancing the pin puts the last
// commit that actually verified out of reach, and the consumer is left with
// neither copy at exactly the moment a signature stopped verifying.
func TestUpgrade_RefusesAdvanceOntoUnverifiableSignature(t *testing.T) {
	baseDir, src, ref, _, verified := signedBundleRepo(t, "name: demo\n")
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)
	e0, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok)
	require.Equal(t, verified, e0.SHA, "the project starts pinned to the commit whose signature verifies")

	// The publisher edits and pushes; the stale .sig rides along unchanged.
	edited := addFileToLocalRepo(t, src, repoV2("demo")+"/fragments/keeper.md", "EDITED AFTER SIGNING\n")
	require.NotEqual(t, verified, edited)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err, "a refused advance is a reported outcome, not a command failure")
	assert.Equal(t, 0, res.Advanced, "nothing may be counted as advanced")

	require.Len(t, res.Refused, 1, "the refusal must be REPORTED — a silent non-advance reads as 'already up to date'")
	assert.Equal(t, ref, res.Refused[0].Identity)
	assert.Equal(t, verified, res.Refused[0].KeptSHA)
	assert.Equal(t, edited, res.Refused[0].ProposedSHA)
	assert.Contains(t, res.Refused[0].Detail, bundles.ErrTreeBundleWithheld.Error())

	// The payload assertion: the lockfile still holds the last verified pin,
	// whole. Nothing half-wrote.
	e1, ok := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	require.True(t, ok, "the entry must still be there — refusing must not erase the pin")
	assert.Equal(t, verified, e1.SHA, "the pin stays at the last commit whose signature verified")
	assert.Equal(t, e0.URL, e1.URL)
	assert.Equal(t, e0.RequestedVersion, e1.RequestedVersion)
}

// The refusal must be NARROW. A publisher who re-signs properly is not
// penalised, and this is what keeps the guard honest: the same fixture, one
// extra commit carrying a fresh signature, and the pin moves.
func TestUpgrade_AdvancesOntoReSignedContent(t *testing.T) {
	baseDir, src, ref, signer, verified := signedBundleRepo(t, "name: demo\n")
	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()

	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)

	reSigned := commitTree(t, src, demoTreeFiles(t, signer, "REVISED AND RE-SIGNED\n"), false)
	require.NotEqual(t, verified, reSigned)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Advanced, "a properly re-signed republish still advances")
	assert.Empty(t, res.Refused)

	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	assert.Equal(t, reSigned, e1.SHA)
}

// Unsigned content is NOT what this guard is about, and folding it in would be
// a different decision than the one taken. Unsigned content takes the review
// path — a human can act on it — so its advance still happens; only a signature
// that lies about its own bytes is refused.
func TestUpgrade_UnsignedContentStillAdvances(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")
	src := filepath.Join(tmp, "src")
	c1 := initLocalRepoWithFile(t, src, repoV2("demo")+"/bundle.yaml", "name: demo\n")
	ref := "file://" + src + "@bundles/demo"
	writeLocalProfile(t, baseDir, "default", "bundles:\n  - "+ref+"\n")

	cfg := testConfigWithSCMPath(baseDir)
	ctx := context.Background()
	_, err := LockDependencies(ctx, cfg, LockDependenciesRequest{FailOnConflict: true})
	require.NoError(t, err)

	c2 := addFileToLocalRepo(t, src, repoV2("demo")+"/bundle.yaml", "version: \"2.0.0\"\n")
	require.NotEqual(t, c1, c2)

	res, err := UpgradeDependencies(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Advanced, "unsigned content is ordinary and still advances")
	assert.Empty(t, res.Refused)

	e1, _ := mustLoadActive(t, baseDir).GetEntry(remote.ItemTypeBundle, ref)
	assert.Equal(t, c2, e1.SHA)
}
