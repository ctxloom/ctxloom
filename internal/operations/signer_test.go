package operations

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

func testKeyLine(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	signer := testSigner(t)
	line := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	return signer, strings.TrimSpace(line)
}

// --- ResolveSignerKey ------------------------------------------------------

func TestResolveSignerKey_LiteralAuthorizedKeysLine(t *testing.T) {
	signer, line := testKeyLine(t)
	info, err := ResolveSignerKey(line+" my-comment", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), info.Fingerprint)
	assert.Equal(t, "my-comment", info.Comment)
}

func TestResolveSignerKey_FromFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	signer, line := testKeyLine(t)
	require.NoError(t, afero.WriteFile(fs, "/keys/org.pub", []byte(line+" org key\n"), 0o644))

	info, err := ResolveSignerKey("/keys/org.pub", fs, nil)
	require.NoError(t, err)
	assert.Equal(t, ssh.FingerprintSHA256(signer.PublicKey()), info.Fingerprint)
}

func TestResolveSignerKey_FromStdin(t *testing.T) {
	_, line := testKeyLine(t)
	info, err := ResolveSignerKey("-", nil, strings.NewReader(line+"\n"))
	require.NoError(t, err)
	assert.NotEmpty(t, info.Fingerprint)
}

func TestResolveSignerKey_EmptyIsError(t *testing.T) {
	_, err := ResolveSignerKey("", nil, nil)
	require.Error(t, err)
}

func TestResolveSignerKey_UnreadableIsError(t *testing.T) {
	_, err := ResolveSignerKey("/does/not/exist.pub", afero.NewMemMapFs(), nil)
	require.Error(t, err)
}

// --- ResolveSignerNamespaces -------------------------------------------

func TestResolveSignerNamespaces_DefaultsToPublish(t *testing.T) {
	ns, err := ResolveSignerNamespaces(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{signing.NamespacePublish}, ns)
}

func TestResolveSignerNamespaces_ExpandsAliases(t *testing.T) {
	ns, err := ResolveSignerNamespaces([]string{"approve", "reject"})
	require.NoError(t, err)
	assert.Equal(t, []string{signing.NamespaceApprove, signing.NamespaceReject}, ns)
}

func TestResolveSignerNamespaces_UnknownIsError(t *testing.T) {
	_, err := ResolveSignerNamespaces([]string{"bogus"})
	require.Error(t, err)
}

// --- AddSigner / ListSigners / ShowSigner / RemoveSigner -----------------

func TestAddSigner_ThenSignatureByThatKeyVerifiesAsTrusted(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// ListSigners/ShowSigner also read the USER store — isolate HOME so
	// this test can never see (or, via a Project:false slip, write) the
	// real developer's ~/.ctxloom/allowed_signers.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs() // signerStorePath resolves a real home dir path
	signer, line := testKeyLine(t)

	keyInfo, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)

	res, err := AddSigner(cfg, AddSignerRequest{
		Principal: "team@example.com",
		Key:       keyInfo,
		Project:   true,
		FS:        fs,
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Path)

	// Build the trust root from exactly the file AddSigner wrote (mirrors
	// config.Config.TrustRoot's read path) and verify a real publish
	// signature by that key is now trusted.
	f, err := fs.Open(res.Path)
	require.NoError(t, err)
	defer f.Close()
	store, perrs, err := allowedsigners.Parse(f)
	require.NoError(t, err)
	require.Empty(t, perrs)

	payload := []byte("bundle bytes")
	armored, err := signing.Sign(payload, signer, signing.NamespacePublish)
	require.NoError(t, err)

	principal, verr := signing.VerifyPublisher(payload, armored, store, time.Now())
	require.NoError(t, verr)
	assert.Equal(t, "team@example.com", principal)
}

func TestAddSigner_WrongNamespaceKeyNotTrustedForPublish(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// ListSigners/ShowSigner also read the USER store — isolate HOME so
	// this test can never see (or, via a Project:false slip, write) the
	// real developer's ~/.ctxloom/allowed_signers.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	signer, line := testKeyLine(t)

	keyInfo, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)

	res, err := AddSigner(cfg, AddSignerRequest{
		Principal:  "reviewer@example.com",
		Key:        keyInfo,
		Namespaces: []string{signing.NamespaceApprove}, // approve only, NOT publish
		Project:    true,
		FS:         fs,
	})
	require.NoError(t, err)

	f, err := fs.Open(res.Path)
	require.NoError(t, err)
	defer f.Close()
	store, _, err := allowedsigners.Parse(f)
	require.NoError(t, err)

	payload := []byte("bundle bytes")
	armored, err := signing.Sign(payload, signer, signing.NamespacePublish)
	require.NoError(t, err)

	// Unsigned TO US: a key scoped to approve is not authorized to publish,
	// so this reports empty/no-error, not "trusted".
	principal, verr := signing.VerifyPublisher(payload, armored, store, time.Now())
	require.NoError(t, verr)
	assert.Empty(t, principal, "an approve-only key must not be trusted for publish")
}

// TestAddSigner_ProjectRequestedButNoneConfigured_FallsBackToUserStore is the
// edge `signer trust`'s new project-by-default posture must handle: run
// outside a project (cfg carries no AppPaths — nothing under .ctxloom/), the
// call must NOT fail. It falls back to the user store and says so via the
// result (res.Fallback/FallbackReason), which the CLI surfaces to the human —
// writing somewhere other than where the user expects, silently, is exactly
// the defect shape this project keeps removing.
func TestAddSigner_ProjectRequestedButNoneConfigured_FallsBackToUserStore(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{}) // no AppPaths: outside a project
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	_, line := testKeyLine(t)
	keyInfo, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)

	res, err := AddSigner(cfg, AddSignerRequest{
		Principal: "x@example.com",
		Key:       keyInfo,
		Project:   true,
		FS:        fs,
	})
	require.NoError(t, err, "no project configured must fall back, never fail")

	homePath, herr := paths.HomeAllowedSignersPath()
	require.NoError(t, herr)
	assert.Equal(t, homePath, res.Path, "the entry must land in the USER store when no project is configured")
	assert.True(t, res.Fallback, "the result must say a fallback happened")
	assert.NotEmpty(t, res.FallbackReason, "the result must say WHY")

	data, err := afero.ReadFile(fs, homePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "x@example.com", "the fallback must actually write the entry, not just report one")
}

// TestAddSigner_ProjectConfigured_NoFallbackAndNeverTouchesUserStore is the
// companion proof: WITH a project configured, requesting the project store
// must land there — Fallback false — and must NEVER touch the user store,
// even incidentally.
func TestAddSigner_ProjectConfigured_NoFallbackAndNeverTouchesUserStore(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	fs := afero.NewOsFs()
	_, line := testKeyLine(t)
	keyInfo, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)

	res, err := AddSigner(cfg, AddSignerRequest{
		Principal: "team@example.com",
		Key:       keyInfo,
		Project:   true,
		FS:        fs,
	})
	require.NoError(t, err)
	assert.False(t, res.Fallback, "a configured project must never report a fallback")

	homePath, herr := paths.HomeAllowedSignersPath()
	require.NoError(t, herr)
	exists, eerr := afero.Exists(fs, homePath)
	require.NoError(t, eerr)
	assert.False(t, exists, "trusting into the project store must never create/touch the user store")
}

func TestAddSigner_AppendsWithoutClobberingExistingEntries(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// ListSigners/ShowSigner also read the USER store — isolate HOME so
	// this test can never see (or, via a Project:false slip, write) the
	// real developer's ~/.ctxloom/allowed_signers.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	_, line1 := testKeyLine(t)
	_, line2 := testKeyLine(t)

	key1, err := ResolveSignerKey(line1, fs, nil)
	require.NoError(t, err)
	key2, err := ResolveSignerKey(line2, fs, nil)
	require.NoError(t, err)

	_, err = AddSigner(cfg, AddSignerRequest{Principal: "a@example.com", Key: key1, Project: true, FS: fs})
	require.NoError(t, err)
	res, err := AddSigner(cfg, AddSignerRequest{Principal: "b@example.com", Key: key2, Project: true, FS: fs})
	require.NoError(t, err)

	entries, err := ListSigners(cfg, fs)
	require.NoError(t, err)
	var principals []string
	for _, e := range entries {
		principals = append(principals, e.Entry.Principals[0])
	}
	assert.Contains(t, principals, "a@example.com")
	assert.Contains(t, principals, "b@example.com")
	assert.Equal(t, res.Path, mustAllowedSignersProjectPath(t, cfg))
}

func TestShowSigner_FindsMatchingPrincipal(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// ListSigners/ShowSigner also read the USER store — isolate HOME so
	// this test can never see (or, via a Project:false slip, write) the
	// real developer's ~/.ctxloom/allowed_signers.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	key, line := testKeyLine(t)
	_ = key

	keyInfo, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)
	_, err = AddSigner(cfg, AddSignerRequest{Principal: "show-me@example.com", Key: keyInfo, Project: true, FS: fs})
	require.NoError(t, err)

	found, err := ShowSigner(cfg, "show-me@example.com", fs)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "project", found[0].Source)
}

func TestRemoveSigner_RemovesOnlyMatchingPrincipal(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// ListSigners/ShowSigner also read the USER store — isolate HOME so
	// this test can never see (or, via a Project:false slip, write) the
	// real developer's ~/.ctxloom/allowed_signers.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	key1, line1 := testKeyLine(t)
	key2, line2 := testKeyLine(t)
	_, _ = key1, key2

	k1, err := ResolveSignerKey(line1, fs, nil)
	require.NoError(t, err)
	k2, err := ResolveSignerKey(line2, fs, nil)
	require.NoError(t, err)
	_, err = AddSigner(cfg, AddSignerRequest{Principal: "keep@example.com", Key: k1, Project: true, FS: fs})
	require.NoError(t, err)
	_, err = AddSigner(cfg, AddSignerRequest{Principal: "drop@example.com", Key: k2, Project: true, FS: fs})
	require.NoError(t, err)

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "drop@example.com", Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Removed)

	entries, err := ListSigners(cfg, fs)
	require.NoError(t, err)
	var principals []string
	for _, e := range entries {
		principals = append(principals, e.Entry.Principals[0])
	}
	assert.Contains(t, principals, "keep@example.com")
	assert.NotContains(t, principals, "drop@example.com")
}

// TestRemoveSigner_RemovesLastPrincipal_LeavesEmptyFile pins a legitimate
// empty write: removing the only entry in the store empties the file, and
// that write must succeed rather than being caught by iox's empty-over-
// existing guard (removeFromAllowedSignersFile passes iox.AllowEmpty() for
// exactly this reason — see fs-consolidation plan C4's 8-caller audit).
func TestRemoveSigner_RemovesLastPrincipal_LeavesEmptyFile(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	_, line := testKeyLine(t)
	k, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)
	add, err := AddSigner(cfg, AddSignerRequest{Principal: "solo@example.com", Key: k, Project: true, FS: fs})
	require.NoError(t, err)

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "solo@example.com", Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Removed)

	got, err := afero.ReadFile(fs, add.Path)
	require.NoError(t, err)
	assert.Empty(t, string(got), "the store must be left genuinely empty, not refused")
}

func TestRemoveSigner_UnknownPrincipalIsNoopNotError(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	// ListSigners/ShowSigner also read the USER store — isolate HOME so
	// this test can never see (or, via a Project:false slip, write) the
	// real developer's ~/.ctxloom/allowed_signers.
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "nobody@example.com", Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Removed)
}

// TestSignerWrites_LeaveNoLeftoverTempFile: both write sites
// (appendAllowedSignersLine via AddSigner, removeFromAllowedSignersFile via
// RemoveSigner) now go through iox.WriteFileAtomicFs (temp file + rename)
// instead of a direct afero.WriteFile, so the trust root is never observed
// half-written. A leftover `*.tmp` sibling in the store's directory would
// mean the rename never completed.
func TestSignerWrites_LeaveNoLeftoverTempFile(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	k1, line1 := testKeyLine(t)
	_ = k1
	key1, err := ResolveSignerKey(line1, fs, nil)
	require.NoError(t, err)

	res, err := AddSigner(cfg, AddSignerRequest{Principal: "atomic@example.com", Key: key1, Project: true, FS: fs})
	require.NoError(t, err)

	_, err = RemoveSigner(cfg, RemoveSignerRequest{Principal: "atomic@example.com", Project: true, FS: fs})
	require.NoError(t, err)

	dir := filepath.Dir(res.Path)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"), "leftover temp file %s: rename did not complete", e.Name())
	}
}

func mustAllowedSignersProjectPath(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path, err := signerStorePath(cfg, true)
	require.NoError(t, err)
	return path
}

// --- embedded-key visibility ------------------------------------------------

// testEmbeddedPrincipal is ctxloom's REAL compiled-in publisher principal
// (internal/config/embedded_signers.allowed_signers) — these tests target the
// actual production identity, not a stand-in, mirroring
// tests/acceptance/steps_j001700.go's j001700EmbeddedPrincipal.
const testEmbeddedPrincipal = "ben+ctxloom@abbitt.me"

func TestListSigners_IncludesEmbeddedEntry(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	listings, err := ListSigners(cfg, fs)
	require.NoError(t, err)

	var found *SignerListing
	for i, l := range listings {
		if l.Source == "embedded" && l.Entry.MatchesPrincipal(testEmbeddedPrincipal) {
			found = &listings[i]
		}
	}
	require.NotNil(t, found, "the embedded release key must be surfaced in `signer list` — the oozy-plod (a) visibility fix")
	assert.Equal(t, "(compiled-in)", found.Path)
	assert.False(t, found.Suppressed, "the embedded entry starts out NOT locally suppressed")
}

func TestShowSigner_ShowsEmbeddedPrincipal(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	found, err := ShowSigner(cfg, testEmbeddedPrincipal, fs)
	require.NoError(t, err)
	require.Len(t, found, 1, "signer show <embedded principal> must find exactly the embedded entry")
	assert.Equal(t, "embedded", found[0].Source)
}

// --- embedded-key local suppression -----------------------------------------

func TestRemoveSigner_EmbeddedPrincipal_SuppressesRatherThanNoEntry(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: testEmbeddedPrincipal, Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Removed, "the embedded key's compiled-in bytes are never deleted")
	assert.True(t, res.EmbeddedSuppressed, "removing the embedded principal must record a local suppression, not just report \"no entry\"")
	require.NotEmpty(t, res.SuppressionPath)

	data, rerr := afero.ReadFile(fs, res.SuppressionPath)
	require.NoError(t, rerr)
	assert.Contains(t, string(data), testEmbeddedPrincipal)
}

func TestRemoveSigner_EmbeddedPrincipal_IsIdempotent(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: testEmbeddedPrincipal, Project: true, FS: fs})
	require.NoError(t, err)
	res2, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: testEmbeddedPrincipal, Project: true, FS: fs})
	require.NoError(t, err)
	assert.True(t, res2.EmbeddedSuppressed)

	data, rerr := afero.ReadFile(fs, res2.SuppressionPath)
	require.NoError(t, rerr)
	// Exactly one line naming the principal — the second call must not
	// duplicate it.
	count := strings.Count(string(data), testEmbeddedPrincipal)
	assert.Equal(t, 1, count, "suppressing an already-suppressed principal must not duplicate the record")
}

// TestRemoveSigner_EmbeddedPrincipal_TakesEffectOnTrustRoot proves the
// suppression is a REAL effect, not just a message: after `signer remove
// <embedded-principal> --project`, a FRESH config pointed at the same project
// no longer trusts that principal's key for publish — content signed only by
// it is withheld (operations.EffectiveTrust step 5 no longer allows, falling
// through to step 7, pending review).
func TestRemoveSigner_EmbeddedPrincipal_TakesEffectOnTrustRoot(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	key := embeddedTestPublicKey(t)
	now := time.Now()
	before := cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now)
	require.True(t, before.Trusted, "sanity: the embedded key starts out trusted for publish")

	_, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: testEmbeddedPrincipal, Project: true, FS: fs})
	require.NoError(t, err)

	after := cfg.TrustRoot().TrustedForNamespace(key, signing.NamespacePublish, now)
	assert.False(t, after.Trusted, "after `signer remove` suppresses the embedded principal, TrustRoot() must no longer trust its key")
}

// TestRemoveSigner_BothOnDiskAndEmbedded_EffectsAreAdditive: a principal
// that is BOTH an on-disk allowed_signers line AND an embedded
// entry used to get only the on-disk line deleted (the `if removed > 0
// {return}` early return skipped the embedded check entirely), leaving the
// embedded key still trusted after `signer remove` reported success. Both
// effects must now land from a single call: the on-disk line is deleted
// AND the embedded principal is locally suppressed, so TrustRoot() no
// longer trusts EITHER key (the on-disk one, deleted outright; the
// embedded one, suppressed) for publish.
func TestRemoveSigner_BothOnDiskAndEmbedded_EffectsAreAdditive(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	// Add an ON-DISK entry using the SAME principal as ctxloom's real
	// embedded key, but a DIFFERENT (freshly generated) key — so this
	// principal is now trusted via two independent paths at once.
	_, line := testKeyLine(t)
	onDiskKey, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)
	_, err = AddSigner(cfg, AddSignerRequest{Principal: testEmbeddedPrincipal, Key: onDiskKey, Project: true, FS: fs})
	require.NoError(t, err)

	embeddedKey := embeddedTestPublicKey(t)
	now := time.Now()
	require.True(t, cfg.TrustRoot().TrustedForNamespace(onDiskKey.PublicKey, signing.NamespacePublish, now).Trusted,
		"sanity: the on-disk key starts out trusted")
	require.True(t, cfg.TrustRoot().TrustedForNamespace(embeddedKey, signing.NamespacePublish, now).Trusted,
		"sanity: the embedded key starts out trusted")

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: testEmbeddedPrincipal, Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Removed, "the on-disk line must still be deleted")
	assert.True(t, res.EmbeddedSuppressed, "the embedded principal must ALSO be suppressed, not skipped because Removed>0")

	assert.False(t, cfg.TrustRoot().TrustedForNamespace(onDiskKey.PublicKey, signing.NamespacePublish, now).Trusted,
		"the deleted on-disk key must no longer be trusted")
	assert.False(t, cfg.TrustRoot().TrustedForNamespace(embeddedKey, signing.NamespacePublish, now).Trusted,
		"the suppressed embedded key must no longer be trusted — this is the effect F17(a)'s early return used to skip")
}

// TestSuppressEmbeddedPrincipal_RecordsEntrysLiteralPrincipals: the WRITE
// side (suppressEmbeddedPrincipal) decides what
// entry matched by GLOB (Entry.MatchesPrincipal, ssh_config PATTERNS), but
// the READ side (config's filterSuppressedPrincipals, via
// Entry.MatchesAnyPrincipal) subtracts by LITERAL membership of the
// matched entry's OWN Principals strings — never a glob match. Recording
// the identity the user typed (which may only match the entry via glob
// expansion, e.g. typing "bob@example.com" against an entry whose
// Principals is "*@example.com") would write a suppression line the read
// side's literal check can never find among that entry's actual Principals,
// so `signer remove` would report success while trust is never revoked.
//
// ctxloom's real embedded store carries only a literal principal today (see
// testEmbeddedPrincipal), so this exercises the fixed function directly with
// a SYNTHETIC glob entry — config.EmbeddedSigners() has no test seam to
// inject a glob principal into the production compiled-in store (see this
// package's report for why that path is code-only, not end-to-end tested).
func TestSuppressEmbeddedPrincipal_RecordsEntrysLiteralPrincipals(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	fs := afero.NewMemMapFs()

	globEntry := allowedsigners.Entry{Principals: []string{"*@example.com"}}
	typedIdentity := "bob@example.com" // matches globEntry only via glob expansion

	path, err := suppressEmbeddedPrincipal(fs, cfg, globEntry, true)
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	recorded := strings.TrimSpace(string(data))

	assert.Equal(t, "*@example.com", recorded,
		"must record the entry's own literal Principals string, not the user-typed identity that matched it via glob")
	assert.NotContains(t, recorded, typedIdentity,
		"the typed identity must never be what gets written — the read side would never find it there")

	// Prove this is what the read side actually needs: Entry.MatchesAnyPrincipal
	// (the primitive config.filterSuppressedPrincipals subtracts with) finds the
	// entry when given what we recorded, but would NOT have found it given the
	// buggy old behavior of recording the typed identity instead.
	suppressedCorrect := map[string]bool{recorded: true}
	assert.True(t, globEntry.MatchesAnyPrincipal(suppressedCorrect),
		"the read side's literal check must find the entry using what suppressEmbeddedPrincipal now records")

	suppressedBuggy := map[string]bool{typedIdentity: true}
	assert.False(t, globEntry.MatchesAnyPrincipal(suppressedBuggy),
		"sanity: the OLD behavior (recording the typed identity) would never have been found by the read side's literal check")
}

// embeddedTestPublicKey parses ctxloom's real embedded public key straight out
// of the compiled-in trust root, for a test that needs the actual key object
// (not just its principal string) to query TrustRoot() directly.
func embeddedTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	for _, e := range config.EmbeddedSigners().Entries() {
		if e.MatchesPrincipal(testEmbeddedPrincipal) {
			return e.PublicKey
		}
	}
	t.Fatalf("embedded trust root carries no entry for %s", testEmbeddedPrincipal)
	return nil
}

// strictMkdirFs models the ONE way a real filesystem is stricter than afero's
// MemMapFs, measured directly: os.MkdirAll("") returns "mkdir : no such file
// or directory", while MemMapFs.MkdirAll("") silently returns nil. Without
// this wrapper an in-memory test cannot see an empty mkdir target at all.
type strictMkdirFs struct{ afero.Fs }

func (f strictMkdirFs) MkdirAll(path string, perm os.FileMode) error {
	if path == "" {
		return fmt.Errorf("mkdir %s: no such file or directory", path)
	}
	return f.Fs.MkdirAll(path, perm)
}

// TestAppendAllowedSignersLine_UsesDurableWrite pins the ruled site (taskloom
// unbounded-bacon): the trust root's append must pass iox.Durable() so a
// crash cannot silently revert who is trusted back to "nobody was just
// added". Durable is a no-op on afero.MemMapFs (see iox.Durable's doc), so
// this needs a real OS filesystem for the seam to have anything to fire on.
func TestAppendAllowedSignersLine_UsesDurableWrite(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "allowed_signers")

	var synced []string
	restore := iox.SetSyncDirForTesting(func(d string) error {
		synced = append(synced, d)
		return nil
	})
	defer restore()

	require.NoError(t, appendAllowedSignersLine(fs, path, "first@example.com ssh-ed25519 AAAA"))
	assert.NotEmpty(t, synced, "appendAllowedSignersLine must pass iox.Durable(): the trust root is a human decision, unrecoverable if the rename silently reverts")
}

// TestRemoveFromAllowedSignersFile_UsesDurableWrite is the append side's
// twin: distrusting a principal is just as unrecoverable a decision as
// trusting one, and must carry the same durability guarantee.
func TestRemoveFromAllowedSignersFile_UsesDurableWrite(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "allowed_signers")

	signer := testSigner(t)
	line, err := allowedsigners.FormatEntry(allowedsigners.Entry{
		Principals: []string{"drop@example.com"},
		Namespaces: []string{signing.NamespaceApprove},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, path, []byte(line+"\n"), 0o600))

	var synced []string
	restore := iox.SetSyncDirForTesting(func(d string) error {
		synced = append(synced, d)
		return nil
	})
	defer restore()

	removed, err := removeFromAllowedSignersFile(fs, path, "drop@example.com")
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	assert.NotEmpty(t, synced, "removeFromAllowedSignersFile must pass iox.Durable(): a reverted distrust after a crash re-opens a door a human closed")
}

// TestAppendAllowedSignersLine_ParentDirs: the parent directory
// of the allowed_signers path is created before the write, INCLUDING the
// depth-1 case ("/allowed_signers"), whose parent is the root.
//
// The hand-rolled parentDir this replaced sliced at the last separator with no
// special case for index 0, so it returned "" for "/allowed_signers" where
// filepath.Dir returns "/". That reached fs.MkdirAll(""), which a real
// filesystem rejects — the append then failed outright rather than writing.
func TestAppendAllowedSignersLine_ParentDirs(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"nested path", "/home/u/.ctxloom/allowed_signers"},
		{"depth-1 absolute path (parent is the root)", "/allowed_signers"},
		{"bare relative name (parent is the cwd)", "allowed_signers"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := strictMkdirFs{afero.NewMemMapFs()}
			require.NoError(t, appendAllowedSignersLine(fs, tc.path, "first@example.com ssh-ed25519 AAAA"),
				"the parent dir of %s must be a path a real filesystem can create", tc.path)
			require.NoError(t, appendAllowedSignersLine(fs, tc.path, "second@example.com ssh-ed25519 BBBB"))

			got, err := afero.ReadFile(fs, tc.path)
			require.NoError(t, err, "the line must actually land on disk at %s", tc.path)
			assert.Equal(t,
				"first@example.com ssh-ed25519 AAAA\nsecond@example.com ssh-ed25519 BBBB\n",
				string(got), "appends must preserve what was already there")
		})
	}
}

// --- a line the parser dropped must not read as "not there" -----------------

// writeAllowedSignersLines replaces the project allowed_signers file with
// exactly these lines — the seam for exercising a store that has content
// AddSigner would never have produced (a hand-edited or externally-managed
// file, which is the normal case for a team's committed store).
func writeAllowedSignersLines(t *testing.T, cfg *config.Config, fs afero.Fs, lines ...string) string {
	t.Helper()
	path := mustAllowedSignersProjectPath(t, cfg)
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return path
}

// `signer remove` parses the store to find the principal's line. A line the
// parser DROPS contributes no entry, so the principal appears absent and the
// command reports "no entry for X" — telling an operator the key is not
// trusted when the file still holds a line they cannot see and did not
// remove. Nothing removed BECAUSE THE FILE COULD NOT BE READ is not the same
// as nothing to remove.
func TestRemoveSigner_UnparseableLine_IsReportedNotSilentlyNothingToRemove(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	writeAllowedSignersLines(t, cfg,
		fs,
		"this-line-is-not-an-allowed-signers-entry",
		"keep@example.com "+strings.TrimSpace(line),
	)

	_, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "unreadable@example.com", Project: true, FS: fs})
	require.Error(t, err, "removing nothing from a store with unreadable lines must not report a clean no-op")
	assert.Contains(t, err.Error(), "this-line-is-not-an-allowed-signers-entry")
}

// The genuinely-nothing-to-do case must stay a clean no-op: a store that
// parses fully and simply has no such principal is not a failure.
func TestRemoveSigner_ParseableStoreWithoutThePrincipal_StaysAQuietNoop(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	writeAllowedSignersLines(t, cfg, fs, "keep@example.com "+strings.TrimSpace(line))

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "nobody@example.com", Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Removed)
}

// A removal that SUCCEEDS alongside an unrelated malformed line still
// succeeds — the guard must not make the store unmanageable.
func TestRemoveSigner_SucceedsDespiteAnUnrelatedUnparseableLine(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	writeAllowedSignersLines(t, cfg, fs, "garbage-line", "drop@example.com "+strings.TrimSpace(line))

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "drop@example.com", Project: true, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Removed)
}

// `signer list` is an AUDIT surface, so it must keep listing the entries it
// can read (a half-authored store must not blank the listing — trap: a hard
// error here breaks enumeration) while the dropped lines are still counted
// rather than erased.
func TestListSigners_UnparseableLine_DoesNotBlankTheListingAndIsCounted(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	writeAllowedSignersLines(t, cfg, fs, "garbage-line", "keep@example.com "+strings.TrimSpace(line))

	entries, err := ListSigners(cfg, fs)
	require.NoError(t, err)

	var principals []string
	unreadable := 0
	for _, e := range entries {
		if e.Unreadable != "" {
			unreadable++
			continue
		}
		principals = append(principals, e.Entry.Principals[0])
	}
	assert.Contains(t, principals, "keep@example.com", "the readable entries must still be listed")
	assert.Equal(t, 1, unreadable, "the dropped line must appear in the audit listing, not vanish from it")
}

// --- an unreadable trust store must not read as an empty one ----------------

// requireNonRoot skips when the test process can read a 0000 file anyway.
// Root bypasses DAC permission checks, so an EACCES test silently becomes a
// no-op there — the same class of blind gate this programme keeps finding.
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not produce EACCES, so this case cannot be exercised")
	}
}

// The headline case: `ctxloom signer remove alice` against a store the
// process CANNOT OPEN used to print "no entry for alice in <path>" and exit
// 0 — a false statement about the trust root. Absent, unreadable, and
// never-asked are three different states; a bare `return 0, nil` collapsed
// the first two.
func TestRemoveSigner_UnreadableStore_IsAnErrorNotNoEntry(t *testing.T) {
	requireNonRoot(t)
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	path := writeAllowedSignersLines(t, cfg, fs, "alice@example.com "+strings.TrimSpace(line))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "alice@example.com", Project: true, FS: fs})
	require.Error(t, err, "an unreadable store must not report a clean no-op")
	assert.Nil(t, res, "no result may be returned alongside a failure to read the trust root")
	assert.Contains(t, err.Error(), path, "the error must name the store it could not read")
}

// `signer list` — the "whom do I trust?" audit surface — must not omit a
// whole store it could not open. It stays TOLERANT (a hard error would blank
// the listing, breaking the very enumeration an operator needs to diagnose
// the problem) but never silent: the store appears as an explicit unreadable
// row.
func TestListSigners_UnreadableStore_AppearsAsAnUnreadableRow(t *testing.T) {
	requireNonRoot(t)
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	path := writeAllowedSignersLines(t, cfg, fs, "alice@example.com "+strings.TrimSpace(line))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	entries, err := ListSigners(cfg, fs)
	require.NoError(t, err, "listing must survive an unreadable store — a hard error here blanks the audit surface")

	var unreadable []SignerListing
	for _, e := range entries {
		if e.Unreadable != "" {
			unreadable = append(unreadable, e)
		}
	}
	require.Len(t, unreadable, 1, "the unreadable store must appear in the listing, not vanish from it")
	assert.Equal(t, path, unreadable[0].Path)
}

// An ABSENT store is genuinely nothing to remove — the third state must stay
// distinct from the other two, or the fix would just invert the lie.
func TestRemoveSigner_AbsentStore_StaysAQuietNoop(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "nobody@example.com", Project: true, FS: afero.NewOsFs()})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Removed)
}
