package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// A read with a typed source seeds under its Key(); one without seeds under a
// bare project name verbatim, and is dropped when its name claims a source.
func TestSeedBundleRef(t *testing.T) {
	local, err := ident.LocalRef("kit")
	require.NoError(t, err)

	got, ok := seedBundleRef(bundletree.ProjectRead(t, "kit", &bundles.Bundle{}), local)
	assert.True(t, ok)
	assert.Equal(t, "ctxloom+local:kit", got)

	got, ok = seedBundleRef(bundletree.ProjectRead(t, "my-kit", &bundles.Bundle{}), ident.BundleRef{})
	assert.True(t, ok, "a project name with no typed source seeds verbatim")
	assert.Equal(t, "my-kit", got)

	_, ok = seedBundleRef(bundletree.RemoteRead(t, "https://example.test/repo@bundles/kit", &bundles.Bundle{
		Fragments: map[string]bundles.BundleFragment{"f": {ItemBody: bundles.ItemBody{Content: "x"}}},
	}), ident.BundleRef{})
	assert.False(t, ok, "a source-naming read with no typed source has no canonical identity")

	_, ok = seedBundleRef(bundles.BundleRead{}, ident.BundleRef{})
	assert.False(t, ok, "a read no reader established names nothing to seed under")
}

func TestBundleProfileSourceURL(t *testing.T) {
	git, err := ident.GitRef("example.test", "/owner/repo", "kit")
	require.NoError(t, err)
	file, err := ident.FileRef("/abs/repo", "kit")
	require.NoError(t, err)
	companion, err := ident.CompanionRef("ltk")
	require.NoError(t, err)
	local, err := ident.LocalRef("kit")
	require.NoError(t, err)

	assert.Equal(t, "https://example.test/owner/repo", bundleProfileSourceURL(git))
	assert.Equal(t, "file:///abs/repo", bundleProfileSourceURL(file))
	assert.Equal(t, "ctxloom:companion", bundleProfileSourceURL(companion))
	assert.Equal(t, "ctxloom:local", bundleProfileSourceURL(local))
	assert.Equal(t, "ctxloom:local", bundleProfileSourceURL(ident.BundleRef{}))
}
