package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/companionloadout"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// TestLoadout_YAML_IsAValidBundle proves the embedded loadout.yaml parses as
// a well-formed bundles.Bundle carrying the taskloom fragment, both hooks,
// and the MCP server registration — the content that used to live in
// ctxloom's now-deleted resources/builtin_bundles/taskloom.yaml.
func TestLoadout_YAML_IsAValidBundle(t *testing.T) {
	b, err := bundles.ParseBundle(loadoutYAML)
	require.NoError(t, err, "taskloom's loadout.yaml must be a well-formed bundle")

	require.Contains(t, b.Fragments, "taskloom")
	assert.NotEmpty(t, b.Fragments["taskloom"].Content)

	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Contains(t, b.Hooks.SessionStart[0].Command, "session-bind")
	require.Len(t, b.Hooks.PostFileEdit, 1)
	assert.Contains(t, b.Hooks.PostFileEdit[0].Command, "stamp-plan")

	require.Contains(t, b.MCP, "taskloom")
	assert.Equal(t, "taskloom", b.MCP["taskloom"].Command)
}

// TestLoadout_YAMLFormat_EmitsRawBytesVerbatim proves --format yaml writes
// the exact embedded bytes, unmodified.
func TestLoadout_YAMLFormat_EmitsRawBytesVerbatim(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "yaml", loadoutYAML, loadoutSig))
	assert.Equal(t, loadoutYAML, buf.Bytes())
}

// TestLoadout_JSONFormat_DecodesToIdenticalBundle proves the round trip a
// real companion-discovery probe depends on.
func TestLoadout_JSONFormat_DecodesToIdenticalBundle(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Empty(t, signer, "an unsigned loadout must decode with an empty verified signer, not an error")

	b, err := bundles.ParseBundle(decoded)
	require.NoError(t, err)
	assert.Contains(t, b.Fragments, "taskloom")
}

// TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher is the end-to-end proof
// (S8 loadoutSig seam, filled) that taskloom's loadout is trusted-by-
// construction, not review-pending: the envelope
// `taskloom loadout --format json` actually emits, verified through
// signing.VerifyPublisher against the REAL trust root ctxloom ships
// (config.Config.TrustRoot(), which includes the compiled-in ctxloom release
// key), resolves to that key's principal. It also DOUBLES as the drift gate
// item 2 requires — if loadout.yaml is ever edited without regenerating
// loadout.yaml.sig (`just sign-loadouts`), the committed .sig no longer
// covers the new bytes and this test starts failing loudly, pure-Go and
// offline, no private key required.
func TestLoadout_SignedLoadoutVerifiesAsTrustedPublisher(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "taskloom's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", loadoutYAML, loadoutSig))

	cfg := &config.Config{}
	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), cfg.TrustRoot(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, loadoutYAML, decoded)
	assert.Equal(t, "ben+ctxloom@abbitt.me", signer, "taskloom's loadout must verify as published by the ctxloom release key")
}

// TestLoadout_TamperedLoadoutBodyFailsVerification proves the drift gate
// actually fires: the real committed loadoutSig, presented against loadout
// bytes that differ from what it covers (simulating loadout.yaml having
// changed without a re-sign), is withheld — never silently downgraded to
// "unsigned, please review" (spec §10.2).
func TestLoadout_TamperedLoadoutBodyFailsVerification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NotEmpty(t, loadoutSig, "taskloom's committed loadout.yaml.sig is missing or not embedded — run `just sign-loadouts` and commit it")

	tampered := append(append([]byte{}, loadoutYAML...), []byte("\n# drift: this byte was never signed\n")...)
	var buf bytes.Buffer
	require.NoError(t, companionloadout.Emit(&buf, "json", tampered, loadoutSig))

	cfg := &config.Config{}
	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), cfg.TrustRoot(), time.Now())
	require.Error(t, err, "a loadout body that drifted from its signature must be withheld, not degraded to unsigned")
	assert.Nil(t, decoded)
	assert.Empty(t, signer)
}

func TestLoadout_UnknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := companionloadout.Emit(&buf, "toml", loadoutYAML, loadoutSig)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}
