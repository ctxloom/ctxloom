package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// newTestKey returns an ephemeral ed25519 public key and its authorized_keys
// line body ("ssh-ed25519 AAAA…"), for composing allowed_signers files.
func newTestKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return sshPub, string(ssh.MarshalAuthorizedKey(sshPub))
}

// A hand-edited project allowed_signers file must work with no CLI at all: the
// `ctxloom signer` porcelain is a later slice, but the file IS the trust root
// and a user (or a team, by committing it) must be able to write it today.
func TestTrustRoot_ProjectStoreIsParsedAndTrusted(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	line := "bundles@ctxloom.dev namespaces=\"" + signing.NamespacePublish + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(line), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	decision := cfg.TrustRoot().TrustedForNamespace(pub, signing.NamespacePublish, time.Now())
	assert.True(t, decision.Trusted, "a key listed in the project allowed_signers is trusted for the namespace it lists")
	assert.Equal(t, "bundles@ctxloom.dev", decision.Principal)
}

// The namespaces= option IS the role system (spec §7): a publish-only key must
// not be able to approve, and an approve-only key must not be able to publish.
func TestTrustRoot_NamespaceScopingIsEnforced(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	line := "lead@team.example namespaces=\"" + signing.NamespaceApprove + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(line), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}
	root := cfg.TrustRoot()

	assert.True(t, root.TrustedForNamespace(pub, signing.NamespaceApprove, time.Now()).Trusted,
		"the key is trusted for the namespace it lists")
	assert.False(t, root.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"an approve-only key must never authorize published content (trap #3)")
}

// No allowed_signers anywhere is the overwhelmingly common case: it must be a
// quiet empty trust root, never an error, and it must trust nothing.
func TestTrustRoot_AbsentStoreTrustsNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, _ := newTestKey(t)

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	root := cfg.TrustRoot()
	require.NotNil(t, root, "an absent trust root is an empty store, never nil")
	assert.False(t, root.TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted)
}

// One malformed line must not disarm the rest of the file (ssh-keygen's own
// behavior): the good keys still load.
func TestTrustRoot_MalformedLineSkippedRestStillLoads(t *testing.T) {
	fs := afero.NewMemMapFs()
	pub, keyLine := newTestKey(t)

	content := "this-line-is-garbage-with-no-key\n" +
		"bundles@ctxloom.dev namespaces=\"" + signing.NamespacePublish + "\" " + keyLine
	require.NoError(t, afero.WriteFile(fs, paths.AllowedSignersPath(".ctxloom"), []byte(content), 0o644))

	cfg := &Config{appPaths: []string{".ctxloom"}, fs: fs}

	assert.True(t, cfg.TrustRoot().TrustedForNamespace(pub, signing.NamespacePublish, time.Now()).Trusted,
		"a malformed line is skipped; the valid entries in the same file still load")
}
