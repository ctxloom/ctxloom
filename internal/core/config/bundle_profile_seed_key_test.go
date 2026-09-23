package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

func seedRead(display string) bundles.BundleRead {
	return bundles.NewRead(display, &bundles.Bundle{}, bundles.ProvenanceProject, bundles.TrustCtxLocal, bundles.SignatureFacts{})
}

// A read with a typed source seeds under its Key(); one without seeds under a
// bare project name verbatim, and is dropped when its name claims a source.
func TestSeedBundleRef(t *testing.T) {
	local, err := trust.LocalRef("kit")
	require.NoError(t, err)

	got, ok := seedBundleRef(seedRead("kit"), local)
	assert.True(t, ok)
	assert.Equal(t, "ctxloom+local:kit", got)

	got, ok = seedBundleRef(seedRead("my-kit"), trust.BundleRef{})
	assert.True(t, ok, "a project name with no typed source seeds verbatim")
	assert.Equal(t, "my-kit", got)

	_, ok = seedBundleRef(seedRead("https://example.test/repo@bundles/kit"), trust.BundleRef{})
	assert.False(t, ok, "a source-naming read with no typed source has no canonical identity")

	_, ok = seedBundleRef(seedRead(""), trust.BundleRef{})
	assert.False(t, ok)
}

func TestBundleProfileSourceURL(t *testing.T) {
	git, err := trust.GitRef("example.test", "/owner/repo", "kit")
	require.NoError(t, err)
	file, err := trust.FileRef("/abs/repo", "kit")
	require.NoError(t, err)
	companion, err := trust.CompanionRef("ltk")
	require.NoError(t, err)
	local, err := trust.LocalRef("kit")
	require.NoError(t, err)

	assert.Equal(t, "https://example.test/owner/repo", bundleProfileSourceURL(git))
	assert.Equal(t, "file:///abs/repo", bundleProfileSourceURL(file))
	assert.Equal(t, "ctxloom:companion", bundleProfileSourceURL(companion))
	assert.Equal(t, "ctxloom:local", bundleProfileSourceURL(local))
	assert.Equal(t, "ctxloom:local", bundleProfileSourceURL(trust.BundleRef{}))
}
