package operations

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer
}

// --- ResolveSignTarget ------------------------------------------------------

func TestResolveSignTarget_BareLocalName(t *testing.T) {
	target, err := ResolveSignTarget("my-tools")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", target.BundleName)
	assert.Empty(t, target.ItemNote)
}

func TestResolveSignTarget_ItemRefResolvesToContainingBundle(t *testing.T) {
	target, err := ResolveSignTarget("my-tools#fragments/go-testing")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", target.BundleName)
	assert.Equal(t, "fragments/go-testing", target.ItemNote)
}

func TestResolveSignTarget_LocalCanonicalRef(t *testing.T) {
	target, err := ResolveSignTarget("ctxloom+local:my-tools")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", target.BundleName)
	assert.Empty(t, target.ItemNote)

	item, err := ResolveSignTarget("ctxloom+local:my-tools#fragments/go-testing")
	require.NoError(t, err)
	assert.Equal(t, "my-tools", item.BundleName)
	assert.Equal(t, "fragments/go-testing", item.ItemNote)
}

// TestResolveSignTarget_ResolvesWithoutACatalog is the property `ctxloom sign`
// cannot do without: a publishing repo signs the bundles IT authors, and those
// need not be members of any resolved set on the machine doing the signing.
// ResolveSignTarget takes no catalog at all, so this is asserted by the
// signature it keeps — and by a bare name nothing on this machine publishes
// still resolving.
func TestResolveSignTarget_ResolvesWithoutACatalog(t *testing.T) {
	target, err := ResolveSignTarget("a-bundle-no-catalog-here-holds")
	require.NoError(t, err)
	assert.Equal(t, "a-bundle-no-catalog-here-holds", target.BundleName)
}

func TestResolveSignTarget_RemoteRefRejected(t *testing.T) {
	_, err := ResolveSignTarget("ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/go-tools#fragments/go-testing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not resolve to a bundle you author locally")
}

func TestResolveSignTarget_RemoteBundleOnlyRefRejected(t *testing.T) {
	_, err := ResolveSignTarget("ctxloom+git://github.com/ctxloom/ctxloom-default//bundles/go-tools")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not resolve to a bundle you author locally")
}

// TestResolveSignTarget_RetiredSpellingsRefusedWithAHint: a spelling the
// grammar no longer accepts is refused AS SUCH — never resolved (a hint is not
// a shim), and never reported as some other fault. A refusal a user cannot act
// on is a dead end, so the message must name where the current grammar is
// written down.
func TestResolveSignTarget_RetiredSpellingsRefusedWithAHint(t *testing.T) {
	for _, ref := range []string{
		"ctxloom:local@bundles/my-tools",
		"ctxloom:local@bundles/my-tools#fragments/go-testing",
		"https://github.com/ctxloom/ctxloom-default@bundles/go-tools",
		"git@github.com:ctxloom/ctxloom-default",
	} {
		_, err := ResolveSignTarget(ref)
		require.Error(t, err, ref)
		assert.ErrorIs(t, err, errs.ErrRetiredRefSpelling, ref)
		assert.Contains(t, err.Error(), "ctxloom bundle sign --help", ref)
	}
}

// TestResolveSignTarget_CompanionRefRejected: a companion loadout is signed
// where it is built, never here — and the retired "builtin:" spelling names
// nothing, so it is refused as a retired spelling rather than resolving to a
// local bundle of that name.
func TestResolveSignTarget_CompanionRefRejected(t *testing.T) {
	for _, ref := range []string{
		"ctxloom+companion:ltk#fragments/x",
		"ctxloom+companion:ltk",
	} {
		_, err := ResolveSignTarget(ref)
		require.Error(t, err, ref)
		assert.Contains(t, err.Error(), "does not resolve to a bundle you author locally", ref)
	}
	_, err := ResolveSignTarget("builtin:ltk#fragments/x")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrRetiredRefSpelling)
}

func TestResolveSignTarget_EmptyRefErrors(t *testing.T) {
	_, err := ResolveSignTarget("")
	require.Error(t, err)
}

// --- SignBundleFile -----------------------------------------------------

// TestSignBundleFile_NoSignerIsHardError is the "failing to sign is a hard
// error, never a silent unsigned publish" red line, at the operations
// layer: no signer supplied must never produce a bundle silently left
// without a .sig — it must error outright.
func TestSignBundleFile_NoSignerIsHardError(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	_, err = SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "my-tools"},
		Signer: nil,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signer")
}

func TestSignBundleFile_UnknownBundleErrors(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "does-not-exist"},
		Signer: signer,
	})
	require.Error(t, err)
}

// --- ListLocalBundleNames -------------------------------------------------

func TestListLocalBundleNames_ListsOnlyLocalBundles(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)
	_, err = CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "beta"})
	require.NoError(t, err)

	names, err := ListLocalBundleNames(cfg, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "beta"}, names)
}

func TestListLocalBundleNames_EmptyWhenNoLocalDir(t *testing.T) {
	names, err := ListLocalBundleNames(&config.Config{}, nil)
	require.NoError(t, err)
	assert.Empty(t, names)
}

// SignBundleFile read the bundle file and handed the bytes to
// signing.Sign with no length check, so a truncated or zero-byte bundle got a
// .sig and a "Signed ..." line at exit 0 — a valid publish signature covering
// nothing. bundles.ParseBundle yaml.Unmarshals empty input without error, so the
// truncated file survives loadBundleForUpdate and reaches the signer.
func TestSignBundleFile_RefusesAZeroByteBundle(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "truncated"})
	require.NoError(t, err)

	fs := afero.NewOsFs()
	path := paths.BundlesLayoutRoot(cfg.GetBundleDirs()[0], paths.LayoutV2) + "/truncated.yaml"
	require.NoError(t, afero.WriteFile(fs, path, nil, 0o644))

	_, err = SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "truncated"},
		Signer: testSigner(t),
		FS:     fs,
	})
	require.Error(t, err, "signing zero bytes must not report success")
	assert.Contains(t, err.Error(), "empty")

	_, statErr := fs.Stat(path + ".sig")
	assert.Error(t, statErr, "a refused sign must not leave a signature behind")
}

// ListLocalBundleNames swallowed EVERY ReadDir error with a bare
// `continue`, so a bundle dir it could not read was indistinguishable from one
// that simply is not there, and `sign --all` reported "no local bundles to
// sign" at exit 0. An absent dir is legitimately nothing; one that exists and
// cannot be read is a failure to find out.
//
// Note on reach: cfg.GetBundleDirs() already Stats each candidate and drops
// anything that is not a directory, so the surviving way to hit this is a
// directory whose contents cannot be listed -- mode 0000 here. Root ignores
// permission bits, so the test skips there rather than passing vacuously.
func TestListLocalBundleNames_UnreadableDirIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; the unreadable case cannot be staged")
	}
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)

	bundleDir := cfg.GetBundleDirs()[0]
	require.NoError(t, os.Chmod(bundleDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(bundleDir, 0o755) })

	_, err = ListLocalBundleNames(cfg, afero.NewOsFs())
	require.Error(t, err, "a bundle dir that cannot be read must not look like an empty one")
}

// The absent case stays green: a project with no authored bundle dir has
// legitimately nothing to list.
func TestListLocalBundleNames_AbsentDirIsNotAnError(t *testing.T) {
	names, err := ListLocalBundleNames(&config.Config{}, nil)
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestListLocalBundleNames_MatchesTheLoadersEnumeration is a gate, and it is
// deliberately a CROSS-CHECK against the real loader rather than a
// hand-written expectation: `sign --all` must sign every bundle the rest of
// ctxloom can load out of the authored dirs, and the only trustworthy
// statement of "every bundle" is the enumeration bundles.Loader actually
// performs. A hand-listed expectation would have gone stale the moment the
// loader learned a new bundle shape; this cannot.
//
// The defect it pins: ListLocalBundleNames dropped `e.IsDir()` outright, so
// every DIRECTORY-form bundle — i.e. exactly the bundles that can ship skills
// (skills.go requires directory form) — was unsignable via --all, and the
// command reported success having signed a subset.
func TestListLocalBundleNames_MatchesTheLoadersEnumeration(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := cfg.GetBundleDirs()[0]
	fs := afero.NewOsFs()

	// File-form bundle.
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "alpha"})
	require.NoError(t, err)
	// Directory-form bundles — the shape that can carry skills. Their
	// envelopes declare no inline items, so they are format v2 and go in the
	// v2 root; the bundles root itself holds no bundles.
	v2 := paths.BundlesLayoutRoot(dir, paths.LayoutV2)
	require.NoError(t, fs.MkdirAll(filepath.Join(v2, "gamma"), 0o755))
	require.NoError(t, fs.MkdirAll(filepath.Join(v2, "nested", "delta"), 0o755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(v2, "gamma", "bundle.yaml"),
		[]byte("version: 0.1.0\n"), 0o644))
	// Nested directory-form bundle — the loader walks recursively, so --all
	// must reach this too.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(v2, "nested", "delta", "bundle.yaml"),
		[]byte("version: 0.1.0\n"), 0o644))

	names, err := ListLocalBundleNames(cfg, fs)
	require.NoError(t, err)

	infos, err := bundles.NewLoader(bundles.NewProjectReader(fs, cfg.GetBundleDirs())).List()
	require.NoError(t, err)
	var want []string
	for _, b := range infos {
		want = append(want, b.Name)
	}
	sort.Strings(want)

	assert.Equal(t, want, names,
		"sign --all must sign exactly the bundles the loader can load from the authored dirs")
	assert.Contains(t, names, "gamma", "a directory-form bundle must be signable via --all")

	// `sign --all` HANDS these names to SignBundleFile. Every directory-form
	// bundle among them is signed through its manifest entry; a single-file
	// bundle is refused by sentinel, never silently skipped — the refusal is
	// what tells the author to move it to the tree form.
	signer := testSigner(t)
	for _, name := range names {
		res, serr := SignBundleFile(cfg, SignBundleRequest{
			Target: SignTarget{BundleName: name},
			Signer: signer,
			FS:     fs,
		})
		if name == "alpha" {
			require.ErrorIs(t, serr, ErrSingleFileBundleUnsignable, "a single-file bundle is refused, by name")
			continue
		}
		require.NoError(t, serr, "sign --all must be able to sign %q", name)
		ok, _ := afero.DirExists(fs, res.SigPath)
		assert.True(t, ok, "no signature landed for %q at %s", name, res.SigPath)
	}
}

// --- directory-form bundles: signing the tree, not just its manifest ---------

// signDirBundle stages a DIRECTORY-form bundle carrying real content beside its
// manifest — the shape a publisher uses, and the only shape that can ship skills.
func signDirBundle(t *testing.T) (cfg *config.Config, dir string) {
	t.Helper()
	_, cfg = setupBundleTestDir(t)
	dir = filepath.Join(authoredV1(cfg.GetAppPaths()[0]), "atelier")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundles.DirectoryFormManifest),
		[]byte("version: 1.0.0\ndescription: atelier\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "house-style.md"),
		[]byte("---\ndescription: d\n---\n\nHOUSE-STYLE-BODY\n"), 0o644))
	return cfg, dir
}

// signTrustRoot is a trust root that authorises this signer to publish.
func signTrustRoot(signer ssh.Signer) trust.TrustRoot {
	return allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		Namespaces: []string{signing.NamespacePublish},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
}

// openSignedTree reads the signed bundle back through the CONSUMER's surface —
// the same content.TreeStore a pulled bundle is read through.
func openSignedTree(t *testing.T, dir string) content.Bundle {
	t.Helper()
	store, err := content.NewTreeStore(afero.NewOsFs(), filepath.Dir(dir), content.Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := store.Open(context.Background(), content.BundleID(filepath.Base(dir)))
	require.NoError(t, err)
	return b
}

// THE WIRING ASSERTION. `ctxloom bundle sign` must produce an attestation the
// CONSUMER's verification path actually recognises.
//
// A directory-form bundle is verified by attest.VerifyBundle, which reads a
// SHA256SUMS manifest and the signatures filed against it. Signing only
// bundle.yaml's bytes writes a sibling .sig that path never looks at: the author
// sees exit 0 and a .sig on disk, and every consumer reads the bundle as
// UNATTESTED. That is a signature that attests nothing anyone checks.
func TestSignBundleFile_DirectoryFormProducesAnAttestationTheConsumerAccepts(t *testing.T) {
	cfg, dir := signDirBundle(t)
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier"},
		Signer: signer,
	})
	require.NoError(t, err)

	verdict, verr := attest.VerifyBundle(context.Background(), openSignedTree(t, dir), signTrustRoot(signer), time.Now())
	require.NoError(t, verr)
	assert.True(t, verdict.OK(),
		"a signed directory-form bundle must verify for a consumer; got status %q (%s)", verdict.Status, verdict.Detail)
	assert.Equal(t, "me@example.com", verdict.Principal)
	assert.NoError(t, verdict.Contents, "the signed manifest must cover the tree as published")
}

// The attestation must cover the CONTENT, not just the manifest file. Editing a
// fragment after signing has to break verification — otherwise the signature
// says nothing about the thing that reaches a model.
func TestSignBundleFile_DirectoryFormAttestationCoversContentNotJustTheManifest(t *testing.T) {
	cfg, dir := signDirBundle(t)
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier"},
		Signer: signer,
	})
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "house-style.md"),
		[]byte("---\ndescription: d\n---\n\nSUBSTITUTED\n"), 0o644))

	verdict, verr := attest.VerifyBundle(context.Background(), openSignedTree(t, dir), signTrustRoot(signer), time.Now())
	require.NoError(t, verr)
	assert.False(t, verdict.OK(),
		"editing a fragment after signing must break the bundle's attestation")
}

// --- directory-form bundles: refusing to sign over a stale skill manifest ---

// signSkillBundle stages a directory-form bundle carrying one or more skills
// whose bundle.yaml `files:` manifest is freshly SYNCED with the on-disk
// tree — the baseline every staleness test below edits away from. Built via
// CreateSkill + SyncSkill (the real `ctxloom skill create`/`ctxloom skill
// sync` operations), not a hand-assembled bundle.yaml, so the fixture is
// exactly what those commands actually produce. Returns the bundle's root
// directory, for checking that no signature artifact lands anywhere under it
// when signing is refused.
func signSkillBundle(t *testing.T, bundleName string, skillNames ...string) (cfg *config.Config, bundleDir string) {
	t.Helper()
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, bundleName)
	for _, name := range skillNames {
		_, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{
			Bundle: bundleName, Name: name, Description: "d",
		})
		require.NoError(t, err)
	}
	_, err := SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: bundleName})
	require.NoError(t, err)
	return cfg, filepath.Join(authoredV1(appDir), bundleName)
}

// editSkillWithoutSyncing edits a skill's SKILL.md on disk WITHOUT re-running
// `ctxloom skill sync` — the exact drift `bundle sign` must catch: bundle.yaml
// still records the pre-edit manifest, the tree no longer matches it.
func editSkillWithoutSyncing(t *testing.T, bundleDir, skillName string) {
	t.Helper()
	skillMD := filepath.Join(bundleDir, "skills", skillName, "SKILL.md")
	data, err := os.ReadFile(skillMD)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(skillMD, append(data, []byte("\nEdited without syncing.\n")...), 0o644))
}

// A skill manifest that MATCHES the tree must still sign — the staleness
// check must not become a blanket refusal for every bundle carrying a skill.
func TestSignBundleFile_MatchingSkillManifestStillSigns(t *testing.T) {
	cfg, _ := signSkillBundle(t, "atelier-skills", "reviewer")
	signer := testSigner(t)

	res, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier-skills"},
		Signer: signer,
	})
	require.NoError(t, err, "a bundle whose skill manifest matches the tree must still sign")

	ok, existsErr := afero.Exists(afero.NewOsFs(), res.SigPath)
	require.NoError(t, existsErr)
	assert.True(t, ok, "signing a matching manifest must write the sibling signature at %s", res.SigPath)
}

// THE RULING. A stale skill manifest (bundle.yaml's skills.<name>.files no
// longer matching the source tree) must refuse to sign — nonzero error,
// nothing written — naming the skill and the remedy (`ctxloom skill sync`).
// `--degraded` is deliberately not exercised here: this refusal has no
// degraded arm (see StaleSkillManifests/errStaleSkillManifests) because
// signing itself is the harm.
func TestSignBundleFile_StaleSkillManifestRefusesToSign(t *testing.T) {
	cfg, bundleDir := signSkillBundle(t, "atelier-skills", "reviewer")
	editSkillWithoutSyncing(t, bundleDir, "reviewer")
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier-skills"},
		Signer: signer,
	})
	require.Error(t, err, "signing over a stale skill manifest must be refused")
	assert.Contains(t, err.Error(), "reviewer", "the refusal must name the stale skill")
	assert.Contains(t, err.Error(), "ctxloom skill sync", "the refusal must name the remedy")

	sibling := filepath.Join(bundleDir, "bundle.yaml.sig")
	ok, existsErr := afero.Exists(afero.NewOsFs(), sibling)
	require.NoError(t, existsErr)
	assert.False(t, ok, "a refused sign must not write the detached bundle.yaml.sig sibling")

	sigsDir := filepath.Join(bundleDir, content.SigDirName)
	dirOK, dirErr := afero.DirExists(afero.NewOsFs(), sigsDir)
	require.NoError(t, dirErr)
	assert.False(t, dirOK, "a refused sign must not create the tree's .sigs/ store")
}

// SEVERAL stale skills must all be named in one refusal, not just the first —
// a bundle carrying multiple skills should not need one sign attempt per
// skill to discover the whole stale set.
func TestSignBundleFile_ReportsEveryStaleSkillNotJustTheFirst(t *testing.T) {
	cfg, bundleDir := signSkillBundle(t, "atelier-skills", "reviewer", "editor", "curator")
	editSkillWithoutSyncing(t, bundleDir, "reviewer")
	editSkillWithoutSyncing(t, bundleDir, "curator")
	signer := testSigner(t)

	_, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "atelier-skills"},
		Signer: signer,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reviewer", "the untouched-then-edited skill must be named")
	assert.Contains(t, err.Error(), "curator", "the second edited skill must be named too")
	assert.NotContains(t, err.Error(), "editor", "the skill that stayed in sync must not be named as stale")
}

// TestSignBundleFile_SingleFileBundleIsRefused: a bundle's ONE signature is
// the SHA256SUMS manifest and its .sigs/ entry, which only a directory-form
// bundle carries. A single-file bundle is refused, by sentinel, and nothing
// is written beside it.
func TestSignBundleFile_SingleFileBundleIsRefused(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{Name: "my-tools"})
	require.NoError(t, err)

	_, err = SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "my-tools"},
		Signer: testSigner(t),
	})

	require.ErrorIs(t, err, ErrSingleFileBundleUnsignable)
	path := paths.BundlesLayoutRoot(cfg.GetBundleDirs()[0], paths.LayoutV2) + "/my-tools.yaml"
	_, statErr := os.Stat(path + ".sig")
	assert.Error(t, statErr, "a refused sign must not leave a sibling behind")
}

// TestSignBundleFile_TreeSignsThroughItsManifestAndClearsTheSibling: a
// directory-form bundle is signed through attest.SignBundle — the manifest
// and its .sigs/ entry — and a retired sibling bundle.yaml.sig beside it is
// removed on the way, so the reader that refuses a sibling
// (bundles.ErrSiblingSignatureRetired) reads the re-signed bundle as VALID.
func TestSignBundleFile_TreeSignsThroughItsManifestAndClearsTheSibling(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	dir := filepath.Join(paths.BundlesLayoutRoot(cfg.GetBundleDirs()[0], paths.LayoutV2), "kit")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundles.DirectoryFormManifest), []byte("version: 1.0.0\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "keeper.md"), []byte("KEEPER\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundles.DirectoryFormManifest+".sig"), []byte("retired sibling\n"), 0o644))
	signer := testSigner(t)

	res, err := SignBundleFile(cfg, SignBundleRequest{
		Target: SignTarget{BundleName: "kit"},
		Signer: signer,
	})

	require.NoError(t, err)
	assert.True(t, res.Tree)
	assert.Equal(t, filepath.Join(dir, content.SigDirName), res.SigPath)
	_, statErr := os.Stat(filepath.Join(dir, bundles.DirectoryFormManifest+".sig"))
	assert.Error(t, statErr, "the retired sibling is removed by re-signing")
	entries, err := os.ReadDir(filepath.Join(dir, content.SigDirName))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "one manifest entry is the signature")

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"me@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	reads, err := bundles.NewProjectReader(afero.NewOsFs(), cfg.GetBundleDirs(), bundles.WithTrustRoot(root)).Read(context.Background())
	require.NoError(t, err)
	var read bundles.BundleRead
	for _, r := range reads {
		if r.Bundle.Name == "kit" {
			read = r
		}
	}
	require.NotNil(t, read.Bundle, "the re-signed bundle reads")
	assert.Equal(t, bundles.SignatureValid, read.Signature())
	assert.Equal(t, "me@example.com", read.Bundle.Signer())
}
