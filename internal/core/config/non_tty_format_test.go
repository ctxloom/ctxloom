package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// non_tty_format reads back as written, and unset reads as empty: the CLI,
// not config, owns what an empty value means (json), so config does not
// invent a default it would then have to keep in step with clifmt.
func TestNonTTYFormatSurvivesSaveRoundTrip(t *testing.T) {
	data, err := yamlx.Marshal(NewFixture(Fixture{SchemaVersion: CurrentConfigVersion, NonTTYFormat: "text"}))
	require.NoError(t, err)
	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, "text", reloaded.NonTTYFormat())

	data, err = yamlx.Marshal(NewFixture(Fixture{SchemaVersion: CurrentConfigVersion}))
	require.NoError(t, err)
	reloaded, err = ParseConfig(data)
	require.NoError(t, err)
	assert.Empty(t, reloaded.NonTTYFormat(), "unset reads as empty")
}
