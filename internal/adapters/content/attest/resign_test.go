package attest

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
)

// A bundle's .sigs/ store holds ONE entry per (signing key, namespace): a
// re-sign by the same key REPLACES that key's earlier entry rather than
// leaving it beside the new one, and a second key adds a second entry. The
// read side stays tolerant of a directory that already carries more than one
// entry for a key — those directories verify today and they still verify.

// sigEntries lists the bundle's stored signature files.
func sigEntries(t *testing.T, fsys afero.Fs) []string {
	t.Helper()
	dir := filepath.Join(storeRoot, "code-quality", content.SigDirName)
	infos, err := afero.ReadDir(fsys, dir)
	require.NoError(t, err)
	var names []string
	for _, fi := range infos {
		names = append(names, fi.Name())
	}
	return names
}

func TestSignBundle_ReSignAfterAnEditReplacesTheSameKeysEntry(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))
	require.Len(t, sigEntries(t, fsys), 1)

	// The tree changes, so the next manifest — and the signature over it —
	// differ byte-for-byte from the first.
	write(t, fsys, solidFS, "an edited body\n")
	require.NoError(t, SignBundle(ctx, store, b, signer))

	assert.Len(t, sigEntries(t, fsys), 1, "a re-sign by the same key must replace its earlier entry, not sit beside it")
	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status)
	assert.NoError(t, v.Contents)
}

func TestSignBundle_ASecondSignerAddsASecondEntry(t *testing.T) {
	store, b, fsys := fixture(t)
	alice, alicePub := testSigner(t)
	bob, bobPub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, alice))
	require.NoError(t, SignBundle(ctx, store, b, bob))

	assert.Len(t, sigEntries(t, fsys), 2, "two keys are two entries")
	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("alice@example.test", alicePub)), now)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.test", v.Principal, "alice's entry verifies under a root that trusts only alice")
	v, err = VerifyBundle(ctx, b, rootTrusting(publisher("bob@example.test", bobPub)), now)
	require.NoError(t, err)
	assert.Equal(t, "bob@example.test", v.Principal, "bob's entry verifies under a root that trusts only bob")
}

// TestVerifyBundle_ADirectoryAlreadyHoldingTwoEntriesForOneKeyStillVerifies
// pins the read side's tolerance. Before entries were keyed by signing key,
// a re-sign after an edit left the stale entry beside the live one under a
// name derived from the signature's own bytes; such directories exist and
// must keep verifying — the live entry wins, the stale one cannot veto it.
func TestVerifyBundle_ADirectoryAlreadyHoldingTwoEntriesForOneKeyStillVerifies(t *testing.T) {
	store, b, fsys := fixture(t)
	signer, pub := testSigner(t)
	require.NoError(t, SignBundle(ctx, store, b, signer))
	sigDir := filepath.Join(storeRoot, "code-quality", content.SigDirName)
	first := sigEntries(t, fsys)
	require.Len(t, first, 1)
	stale, err := afero.ReadFile(fsys, filepath.Join(sigDir, first[0]))
	require.NoError(t, err)

	write(t, fsys, solidFS, "an edited body\n")
	require.NoError(t, SignBundle(ctx, store, b, signer))

	// Re-create the pre-ruling shape: the stale signature filed under a
	// signature-bytes-derived tag, beside the live entry.
	legacy := filepath.Join(sigDir, content.BundleSigKey+".publish.v1.ctxloom.dev.0123456789abcdef.sig")
	require.NoError(t, afero.WriteFile(fsys, legacy, stale, 0o644))
	require.Len(t, sigEntries(t, fsys), 2)

	v, err := VerifyBundle(ctx, b, rootTrusting(publisher("pub@example.test", pub)), now)
	require.NoError(t, err)
	assert.Equal(t, StatusManifestSigned, v.Status, "the live entry verifies; the stale one beside it cannot veto")
	assert.Equal(t, "pub@example.test", v.Principal)
	assert.NoError(t, v.Contents)
}
