package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// TestCompanionsSurviveSaveRoundTrip pins the registration key end to end: a
// registered NAME is written, marshalled, parsed back and read through the
// accessor. Only names are persisted — the key holds no path.
func TestCompanionsSurviveSaveRoundTrip(t *testing.T) {
	cfg := NewFixture(Fixture{
		SchemaVersion: CurrentConfigVersion,
		Companions:    []string{"acme", "ltk"},
	})

	data, err := yamlx.Marshal(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(data), "companions:\n  - acme\n  - ltk\n")

	reloaded, err := ParseConfig(data)
	require.NoError(t, err)
	assert.Equal(t, []string{"acme", "ltk"}, reloaded.GetCompanions())
}

// TestGetCompanions_ReturnsACopy: a caller mutating what it was handed must
// not reach back into the generation's Config.
func TestGetCompanions_ReturnsACopy(t *testing.T) {
	cfg := NewFixture(Fixture{Companions: []string{"acme"}})
	got := cfg.GetCompanions()
	got[0] = "evil"
	assert.Equal(t, []string{"acme"}, cfg.GetCompanions())
}
