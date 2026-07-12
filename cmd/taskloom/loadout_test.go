package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
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

func TestLoadout_UnknownFormatErrors(t *testing.T) {
	var buf bytes.Buffer
	err := companionloadout.Emit(&buf, "toml", loadoutYAML, loadoutSig)
	assert.Error(t, err)
	assert.Empty(t, buf.Bytes())
}
