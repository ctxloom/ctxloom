package operations

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
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

func mustAllowedSignersProjectPath(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path, err := signerStorePath(cfg, true)
	require.NoError(t, err)
	return path
}
