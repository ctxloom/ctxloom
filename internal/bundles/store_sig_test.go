package bundles

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
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
// newWarningStore builds a filesystem store whose diagnostics land in buf, so a
// test can read what the publisher would have been told. It reaches through the
// Store interface to the concrete store's embedded loader, which is where the
// warn sink lives.
func newWarningStore(fsys afero.Fs, dirs []string, buf *bytes.Buffer) Store {
	store := NewFSStore(fsys, dirs)
	store.(*fsStore).WithWarnWriter(buf)
	return store
}

func TestFSStore_Save_DropsStaleSignatureAndWarns(t *testing.T) {
	dir := t.TempDir()
	var warnings bytes.Buffer
	store := newWarningStore(nil, []string{dir}, &warnings)

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
	store := newWarningStore(nil, []string{dir}, &warnings)

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
	store := newWarningStore(nil, []string{dir}, &warnings)

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

// TestFSStore_Save_UnreadableSignatureIsLoudNotAssumedAbsent pins the fix below.
//
// invalidateStaleSignature read the sibling `.sig` and treated EVERY error as
// "no signature, nothing to invalidate". A `.sig` that exists but cannot be
// read — permissions, an I/O error, a directory sitting at that path — is not
// the same as no signature at all: the save then returns success having left a
// broken pair on disk, which is precisely the outcome this function's own doc
// comment says is a hard error ("we will not return success having left a
// broken pair on disk").
//
// Downstream, that stale pair is indistinguishable from tampering: a trusted
// key over non-matching bytes, so every consumer withholds the bundle and the
// publisher hears about it from a stranger.
func TestFSStore_Save_UnreadableSignatureIsLoudNotAssumedAbsent(t *testing.T) {
	mem := afero.NewMemMapFs()
	dir := "/bundles"
	path := filepath.Join(dir, "opaque.yaml")
	sigPath := path + SigSuffix

	// A signature exists on disk — it just cannot be read.
	require.NoError(t, mem.MkdirAll(dir, 0o755))
	require.NoError(t, afero.WriteFile(mem, sigPath, []byte("armored-signature-bytes"), 0o644))

	var warnings bytes.Buffer
	store := newWarningStore(&openFailFs{Fs: mem, failPath: sigPath}, []string{dir}, &warnings)

	b := &Bundle{Path: path, Version: "1.0", Fragments: map[string]BundleFragment{"a": {Content: "one"}}}
	err := store.Save(b)

	require.Error(t, err, "an unreadable signature must not be silently assumed absent")
	assert.Contains(t, err.Error(), "opaque.yaml.sig")

	// The signature is still there — nothing was invalidated, so the pair on
	// disk is exactly as broken as the error says it is.
	still, rerr := afero.Exists(mem, sigPath)
	require.NoError(t, rerr)
	assert.True(t, still)
}

// TestFSStore_Save_MissingSignatureStaysSilent pins the other side of the fix:
// a genuinely absent `.sig` is the common case and must stay silent, so the fix
// discriminates not-exist from every other read failure rather than turning the
// common case loud.
func TestFSStore_Save_MissingSignatureStaysSilent(t *testing.T) {
	mem := afero.NewMemMapFs()
	dir := "/bundles"
	var warnings bytes.Buffer
	store := newWarningStore(mem, []string{dir}, &warnings)

	b := &Bundle{Path: filepath.Join(dir, "plain.yaml"), Version: "1.0", Fragments: map[string]BundleFragment{"a": {Content: "one"}}}
	require.NoError(t, store.Save(b), "no signature at all is the common case and must not be an error")
	assert.Empty(t, warnings.String())
}
