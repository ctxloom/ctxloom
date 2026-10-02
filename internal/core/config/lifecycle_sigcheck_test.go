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

// A delegated agent never inherits the waiver (owner ruling 2026-10-02), and
// the coordinator that resolves it lives in the waived session's own MCP
// server — so the generation a delegation decides with is built without the
// owner's waiver, and is NOT published: the session itself stays waived.
func TestReloadForDelegation_AWaivedOwnerBuildsAnEnforcedUnpublishedGeneration(t *testing.T) {
	src := sequenceSources(fixtureWithDefault("first"), fixtureWithDefault("second"))
	owner, err := config.Open(context.Background(), src, config.WithoutSignatureCheck())
	require.NoError(t, err)
	published := owner.Current()

	child, err := owner.ReloadForDelegation(context.Background())

	require.NoError(t, err)
	assert.False(t, child.Trust.SignatureCheckDisabled(), "the child decides enforced")
	assert.False(t, child.Config.Trust().SignatureCheckDisabled(), "through its Config too")
	assert.Equal(t, "second", child.Config.GetDefaultAgent(), "it is a fresh read, like any spawn's")
	assert.Same(t, published, owner.Current(), "the session's own generation is untouched")
	assert.True(t, owner.Current().Trust.SignatureCheckDisabled())
}

// An enforced owner has nothing to withhold from a child: the delegation
// generation is the ordinary per-spawn reload, published as before.
func TestReloadForDelegation_AnEnforcedOwnerReloadsAsBefore(t *testing.T) {
	src := sequenceSources(fixtureWithDefault("first"), fixtureWithDefault("second"))
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)

	child, err := owner.ReloadForDelegation(context.Background())

	require.NoError(t, err)
	assert.False(t, child.Trust.SignatureCheckDisabled())
	assert.Same(t, child, owner.Current(), "published, as a spawn's reload always was")
}
