package bundles

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// signBundleFile signs path's exact on-disk bytes under the publish namespace
// and writes the detached `<path>.sig` sibling — what `ctxloom sign` does. Real
// crypto, not a fake blob: the whole point of these tests is that the pair
// (bytes, signature) either verifies or does not.
func signBundleFile(t *testing.T, path string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	armored, err := signing.Sign(data, signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".sig", armored, 0o644))
}

// A signed bundle that is then EDITED (fragment add / bundle edit / distill —
// every one of them lands here, in Store.Save) must not be left beside a
// signature that covers the bytes it no longer has. That pair is what a
// consumer reads as tampering: the key is trusted, the bytes do not match, so
// the bundle is withheld and the user is told to "investigate the source". The
// publisher must be told, at the moment they break it, that their signature is
// gone.
func TestFSStore_Save_DropsStaleSignatureAndWarns(t *testing.T) {
	dir := t.TempDir()
	var warnings bytes.Buffer
	store := NewFSStore([]string{dir}, false, WithWarnWriter(&warnings))

	path := filepath.Join(dir, "my-tools.yaml")
	b := &Bundle{Path: path, Version: "1.0", Fragments: map[string]BundleFragment{"a": {Content: "before"}}}
	require.NoError(t, store.Save(b))
	signBundleFile(t, path)

	// The edit.
	b.Fragments["a"] = BundleFragment{Content: "after"}
	require.NoError(t, store.Save(b))

	_, err := os.Stat(path + ".sig")
	assert.True(t, os.IsNotExist(err), "the stale signature must be removed, not left to accuse the publisher of tampering")
	assert.Contains(t, warnings.String(), "ctxloom: warning:")
	assert.Contains(t, warnings.String(), "my-tools.yaml.sig")
	assert.Contains(t, warnings.String(), "ctxloom sign my-tools")
}

// Removing a signature is fine; removing one QUIETLY is not. A save that does
// not change the bytes leaves the signature exactly where it is — re-saving an
// unchanged bundle must not cost the publisher their signature.
func TestFSStore_Save_KeepsSignatureWhenBytesUnchanged(t *testing.T) {
	dir := t.TempDir()
	var warnings bytes.Buffer
	store := NewFSStore([]string{dir}, false, WithWarnWriter(&warnings))

	path := filepath.Join(dir, "steady.yaml")
	b := &Bundle{Path: path, Version: "1.0", Fragments: map[string]BundleFragment{"a": {Content: "same"}}}
	require.NoError(t, store.Save(b))
	signBundleFile(t, path)
	sigBefore, err := os.ReadFile(path + ".sig")
	require.NoError(t, err)

	// Same bundle, same bytes.
	require.NoError(t, store.Save(b))

	sigAfter, err := os.ReadFile(path + ".sig")
	require.NoError(t, err, "an unchanged save must not drop a signature that still covers the bytes")
	assert.Equal(t, sigBefore, sigAfter, "the signature must be carried verbatim — never re-serialized")
	assert.Empty(t, warnings.String(), "nothing was invalidated, so nothing to warn about")
}

// Absence of a .sig is the normal case and must stay silent.
func TestFSStore_Save_UnsignedBundleSavesQuietly(t *testing.T) {
	dir := t.TempDir()
	var warnings bytes.Buffer
	store := NewFSStore([]string{dir}, false, WithWarnWriter(&warnings))

	path := filepath.Join(dir, "plain.yaml")
	b := &Bundle{Path: path, Version: "1.0", Fragments: map[string]BundleFragment{"a": {Content: "one"}}}
	require.NoError(t, store.Save(b))
	b.Fragments["a"] = BundleFragment{Content: "two"}
	require.NoError(t, store.Save(b))

	assert.Empty(t, warnings.String())
	got, err := store.Load("plain")
	require.NoError(t, err)
	assert.Equal(t, "two", got.Fragments["a"].Content)
}
