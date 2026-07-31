package companionloadout

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
)

func TestEmit_YAML_WritesBytesVerbatim(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\nfragments:\n  x:\n    content: hi\n")
	var buf bytes.Buffer
	require.NoError(t, Emit(&buf, "yaml", bundle, nil))
	assert.Equal(t, bundle, buf.Bytes())
}

func TestEmit_JSON_RoundTripsThroughDecodeLoadoutEnvelope(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\nfragments:\n  x:\n    content: hi\n")
	var buf bytes.Buffer
	require.NoError(t, Emit(&buf, "json", bundle, nil))

	decoded, signer, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, bundle, decoded)
	assert.Empty(t, signer)
}

// TestEmit_JSON_CarriesTheDetachedSignatureThrough pins the invariant
// NewCommand's doc asserts: the sig parameter is a LIVE seam, not a
// placeholder for a signing pipeline that does not exist. A companion that
// embeds a committed loadout.yaml.sig must see those exact armored bytes
// land in the envelope's signature field — an Emit that dropped sig on the
// floor would still produce a well-formed, decodable envelope, so nothing
// else in this package would notice. cmd/ltk and cmd/taskloom each carry the
// end-to-end half (their committed .sig verifying against the real trust
// root); this is the seam-level half, so deleting the parameter as
// speculative breaks a test in the package that declares it.
func TestEmit_JSON_CarriesTheDetachedSignatureThrough(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\nfragments:\n  x:\n    content: hi\n")
	sig := []byte("-----BEGIN SSH SIGNATURE-----\nnot-a-real-signature\n-----END SSH SIGNATURE-----\n")

	var buf bytes.Buffer
	require.NoError(t, Emit(&buf, FormatJSON, bundle, sig))

	var env signing.LoadoutEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	assert.Equal(t, string(sig), env.Signature,
		"the embedded detached signature must reach the envelope verbatim")

	// The unsigned path stays distinguishable: no sig means no signature
	// field at all, which is what routes a companion to the review path.
	var unsigned bytes.Buffer
	require.NoError(t, Emit(&unsigned, FormatJSON, bundle, nil))
	var env2 signing.LoadoutEnvelope
	require.NoError(t, json.Unmarshal(unsigned.Bytes(), &env2))
	assert.Empty(t, env2.Signature)
}

func TestEmit_UnknownFormatErrorsWithoutWriting(t *testing.T) {
	var buf bytes.Buffer
	err := Emit(&buf, "toml", []byte("x"), nil)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}

// TestEmit_EmptyBundleErrorsInBothFormats (U107-F01) proves a companion
// embedding zero bytes (a build mistake — forgot the go:embed directive,
// wrong glob, empty loadout.yaml) fails loud instead of emitting a
// well-formed-looking envelope contributing nothing, in EITHER format.
// Previously "companion present but contributing nothing" was byte-for-byte
// indistinguishable from a healthy companion at every observable point.
func TestEmit_EmptyBundleErrorsInBothFormats(t *testing.T) {
	for _, format := range []string{"yaml", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			err := Emit(&buf, format, nil, nil)
			require.Error(t, err)
			assert.Empty(t, buf.Bytes(), "nothing must be written once the emptiness check fails")
		})
	}
}

func TestNewCommand_DefaultFormatIsYAML(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\n")
	cmd := NewCommand("acme", bundle, nil)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Equal(t, bundle, buf.Bytes())
}

func TestNewCommand_FormatFlagSelectsJSON(t *testing.T) {
	bundle := []byte("version: \"1.0.0\"\n")
	cmd := NewCommand("acme", bundle, nil)
	require.NoError(t, cmd.Flags().Set("format", "json"))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	require.NoError(t, cmd.RunE(cmd, nil))

	decoded, _, err := signing.DecodeLoadoutEnvelope(buf.Bytes(), nil, time.Now())
	require.NoError(t, err)
	assert.Equal(t, bundle, decoded)
}
