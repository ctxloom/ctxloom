package isolation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildFromSource_StampsOwnershipLabels: every build ctxloom slots carries
// the ownership/slot labels pruning reads — the base with its content key, the
// composed stage with its slot, companion key and the base ref it FROMs — as
// --label flags, so they land even on a base built from a user's Containerfile.
func TestBuildFromSource_StampsOwnershipLabels(t *testing.T) {
	selfExe := withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, logFile, dir, "{}")
	rt := fakeRuntime{name: "docker", binary: script, available: true}

	baseContent := []byte("FROM debian:13\n")
	src := buildSource{
		desc:          "test recipe",
		containerfile: []byte("ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n"),
		base:          &baseStage{desc: "test base", containerfile: baseContent},
	}
	id := agentImageID{ref: "ctxloom-agent-mock:v1-cX-abc", provenance: "p", slot: "abc", companions: "cX"}
	require.NoError(t, buildFromSource(context.Background(), rt, id, src, selfExe, false, nil))

	lines := buildInvocations(t, logFile)
	require.Len(t, lines, 2, "a base build then the agent build")
	base, agent := lines[0], lines[1]
	assert.Contains(t, base, "--label ctxloom.image=base")
	assert.Contains(t, base, "--label ctxloom.slot="+baseContentHash(baseContent))
	assert.NotContains(t, base, "ctxloom.companions", "a base does not depend on the companion set")

	assert.Contains(t, base, "--label ctxloom.tag="+baseImageTagFor(baseContent), "the base proves ownership by its own tag")
	assert.Contains(t, agent, "--label ctxloom.tag=ctxloom-agent-mock:v1-cX-abc", "the composed image proves ownership by its own tag")
	assert.Contains(t, agent, "--label ctxloom.image=composed")
	assert.Contains(t, agent, "--label ctxloom.slot=abc")
	assert.Contains(t, agent, "--label ctxloom.companions=cX")
	assert.Contains(t, agent, "--label ctxloom.from="+baseImageTagFor(baseContent))
}

// TestBuildFromSource_UnslottedIdentityStampsNothing: a legacy fixed-tag
// identity (no slot) carries no ctxloom.image label, so pruning never owns it.
func TestBuildFromSource_UnslottedIdentityStampsNothing(t *testing.T) {
	selfExe := withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, logFile, dir, "{}")
	rt := fakeRuntime{name: "docker", binary: script, available: true}

	src := buildSource{desc: "overlay", containerfile: []byte("FROM x\n")}
	require.NoError(t, buildFromSource(context.Background(), rt, agentImageID{ref: "legacy:latest"}, src, selfExe, false, nil))
	lines := buildInvocations(t, logFile)
	require.Len(t, lines, 1)
	assert.NotContains(t, lines[0], "ctxloom.image")
}

// TestIdentityFor_ComposableCarriesSlot: the on-the-fly build (ensureImage)
// gets the same slot labels as the explicit one — identityFor hands
// buildFromSource composedIdentity's slot and companion key, under the
// container's own tag.
func TestIdentityFor_ComposableCarriesSlot(t *testing.T) {
	c := containerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "claude-code", ImageConfig{})
	id := c.identityFor(nil)
	assert.Equal(t, c.image, id.ref)
	assert.NotEmpty(t, id.slot)
	assert.True(t, strings.HasSuffix(c.image, id.slot), "the slot is the content key the tag ends in: %s vs %s", c.image, id.slot)
	assert.Equal(t, hostImageKeys().companions, id.companions)
}
