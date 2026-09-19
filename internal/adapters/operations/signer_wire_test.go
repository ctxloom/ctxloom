package operations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// These tests assert on the RAW JSON keys rather than unmarshalling back into
// the Go type. Round-tripping through the same struct tags would pass no
// matter what those tags said, which is the vacuous form this repo keeps
// finding: it would prove encoding/json works, not that the wire contract is
// what we promised. Off a terminal the resolved format is json, so this blob
// is what every script, CI job and agent actually receives.

// TestRemoveSignerResult_JSONNamesThePrincipal pins the SUBJECT of the
// operation. The text renderer used to name the principal from argv, which
// meant the machine-readable form could not say WHO had been untrusted --
// an audit surface that reports a removal without naming its target.
func TestRemoveSignerResult_JSONNamesThePrincipal(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	k, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)
	_, err = AddSigner(cfg, AddSignerRequest{Principal: "drop@example.com", Key: k, Project: true, FS: fs})
	require.NoError(t, err)

	res, err := RemoveSigner(cfg, RemoveSignerRequest{Principal: "drop@example.com", Project: true, FS: fs})
	require.NoError(t, err)
	require.Equal(t, 1, res.Removed)

	blob, err := json.Marshal(res)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(blob, &got))

	assert.Equal(t, "drop@example.com", got["principal"],
		"the removal payload must name who was untrusted:\n"+string(blob))
	// multi-word fields are snake_case, matching every other command
	assert.Contains(t, got, "embedded_suppressed", "keys are snake_case:\n"+string(blob))
	assert.Contains(t, got, "suppression_path", "keys are snake_case:\n"+string(blob))
	assert.NotContains(t, got, "EmbeddedSuppressed", "Go-cased keys must not reach the wire:\n"+string(blob))
}

// TestAddSignerResult_JSONNamesThePrincipal is the same gap on the trusting
// side: granting trust is the more dangerous half, so the record of it must
// say who was granted it.
func TestAddSignerResult_JSONNamesThePrincipal(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	_, line := testKeyLine(t)
	k, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)
	res, err := AddSigner(cfg, AddSignerRequest{Principal: "grant@example.com", Key: k, Project: true, FS: fs})
	require.NoError(t, err)

	blob, err := json.Marshal(res)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(blob, &got))

	assert.Equal(t, "grant@example.com", got["principal"],
		"the trust-grant payload must name who was trusted:\n"+string(blob))
	assert.Contains(t, got, "fallback_reason", "keys are snake_case:\n"+string(blob))
}

// TestSignerListing_JSONCarriesFingerprintNeverTheKeyInterface pins the fix
// for a schema that varied by key type. Entry.PublicKey is an ssh.PublicKey
// INTERFACE; serializing it structurally exposed whatever fields the concrete
// implementation happened to have, and gave no fingerprint -- the one form of
// the key a human or a script can actually compare.
func TestSignerListing_JSONCarriesFingerprintNeverTheKeyInterface(t *testing.T) {
	_, cfg := setupBundleTestDir(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()

	signer, line := testKeyLine(t)
	k, err := ResolveSignerKey(line, fs, nil)
	require.NoError(t, err)
	_, err = AddSigner(cfg, AddSignerRequest{Principal: "listed@example.com", Key: k, Project: true, FS: fs})
	require.NoError(t, err)

	listings, err := ListSigners(cfg, fs)
	require.NoError(t, err)
	require.NotEmpty(t, listings, "the entry just added must be listed")

	blob, err := json.Marshal(listings)
	require.NoError(t, err)
	payload := string(blob)

	assert.NotContains(t, payload, `"public_key"`,
		"the ssh.PublicKey interface must never be serialized structurally:\n"+payload)
	assert.NotContains(t, payload, `"PublicKey"`,
		"nor under its Go name:\n"+payload)

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(blob, &rows))

	var found bool
	for _, row := range rows {
		fp, ok := row["fingerprint"].(string)
		require.True(t, ok, "every listing row carries a fingerprint key:\n"+payload)
		if fp == "" {
			// unreadable rows legitimately carry no key
			require.NotEmpty(t, row["unreadable"],
				"only an unreadable row may lack a fingerprint:\n"+payload)
			continue
		}
		assert.True(t, strings.HasPrefix(fp, "SHA256:"),
			"the fingerprint is the SHA256 form:\n"+payload)
		if fp == ssh.FingerprintSHA256(signer.PublicKey()) {
			found = true
		}
	}
	assert.True(t, found, "the added key's own fingerprint must appear:\n"+payload)
}
