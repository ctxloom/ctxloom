package config_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

func TestWithoutSignatureCheck_EveryGenerationsTrustWaivesTheCheck(t *testing.T) {
	src := sequenceSources(fixtureWithDefault("first"), fixtureWithDefault("second"))

	owner, err := config.Open(context.Background(), src, config.WithoutSignatureCheck())
	require.NoError(t, err)
	assert.True(t, owner.Current().Trust.SignatureCheckDisabled(), "the first generation is built without the check")
	assert.True(t, owner.Current().Config.Trust().SignatureCheckDisabled(), "and the Config carries the same Trust")

	next, err := owner.Reload(context.Background())
	require.NoError(t, err)
	assert.True(t, next.Trust.SignatureCheckDisabled(), "a reload cannot quietly restore the check mid-invocation")
}

func TestOpen_WithoutTheOptionEnforcesTheCheck(t *testing.T) {
	owner, err := config.Open(context.Background(), sequenceSources(fixtureWithDefault("first")))
	require.NoError(t, err)
	assert.False(t, owner.Current().Trust.SignatureCheckDisabled())
}
