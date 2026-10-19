package bundles

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const requiresDoc = `version: "1.0.0"
requires:
  commands: [cargo, rustc]
fragments:
  f:
    content: c
`

func TestParseBundle_RequiresRoundTrips(t *testing.T) {
	b, err := ParseBundle([]byte(requiresDoc))
	require.NoError(t, err)
	assert.Equal(t, []string{"cargo", "rustc"}, b.Requires.Commands)

	raw, err := TreeEnvelope(b)
	require.NoError(t, err)
	back, err := ParseBundle(raw)
	require.NoError(t, err, "what a writer emits must read back:\n%s", raw)
	assert.Equal(t, []string{"cargo", "rustc"}, back.Requires.Commands)
}

func TestParseBundle_RequiresRefusesAMisspelledKey(t *testing.T) {
	_, err := ParseBundle([]byte("version: \"1.0.0\"\nrequires:\n  comands: [cargo]\nfragments:\n  f:\n    content: c\n"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "comands")
}

func TestEnvelopeAt_InvalidRequiredCommandIsRefusedNamingTheBundle(t *testing.T) {
	fsys := afero.NewMemMapFs()
	const path = "/bundles/rust-development/bundle.yaml"
	testsupport.WriteFileString(t, fsys, path, "version: \"1.0.0\"\nrequires:\n  commands: [a/b]\nfragments:\n  f:\n    content: c\n", 0o644)

	_, _, err := EnvelopeAt(fsys, path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rust-development", "the bundle is named")
	assert.Contains(t, err.Error(), `"a/b"`, "the offending command is named")
}

// An older binary's envelope check: a generation past this binary's is refused
// as newer rather than reaching the strict decode.
func TestParseBundle_ARequiresEnvelopeOfALaterGenerationIsRefusedAsNewer(t *testing.T) {
	doc := fmt.Sprintf("%s: %d\nversion: \"1.0.0\"\nrequires:\n  commands: [cargo]\n", schemaver.Key, envelopeKind.Current()+1)

	_, err := ParseBundle([]byte(doc))

	require.Error(t, err)
	assert.True(t, errors.Is(err, schemaver.ErrNewer))
}

func TestRequiresToolchainsGeneration_IsWhereTheStepLands(t *testing.T) {
	steps := envelopeKind.StepsAbove(requiresToolchainsGeneration - 1)
	require.Len(t, steps, 1)
	_, ok := steps[0].(requiresToolchainsStep)
	assert.True(t, ok)
	assert.Equal(t, requiresToolchainsGeneration, envelopeKind.Current())
}
