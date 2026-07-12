package companionloadout

import (
	"bytes"
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

func TestEmit_UnknownFormatErrorsWithoutWriting(t *testing.T) {
	var buf bytes.Buffer
	err := Emit(&buf, "toml", []byte("x"), nil)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
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
