package bundletree

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// Each read helper must produce the read it names through the production
// readers — a helper that silently produced a different provenance would make
// every test built on it assert against the wrong case.
func TestRemoteRead_YieldsARemoteRead(t *testing.T) {
	read := RemoteRead(t, "https://example.test/repo@bundles/tools", oneFragment())
	assert.True(t, read.Claimed())
	assert.Equal(t, bundles.ProvenanceRemote, read.Provenance)
	assert.Equal(t, bundles.LocalityRemote, read.Locality())
}

func TestProjectRead_YieldsAProjectRead(t *testing.T) {
	read := ProjectRead(t, "kit", &bundles.Bundle{})
	assert.True(t, read.Claimed())
	assert.Equal(t, bundles.ProvenanceProject, read.Provenance)
	assert.Equal(t, bundles.LocalityLocal, read.Locality())
}

// oneFragment is the smallest bundle a remote tree may hold: the repofs reader
// refuses a tree that declares no items.
func oneFragment() *bundles.Bundle {
	return &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"f": {ItemBody: bundles.ItemBody{Content: "x"}}}}
}
