package isolation

import (
	"context"
	"path/filepath"
	"regexp"
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

	baseOwner := assertOwnershipTagged(t, base, baseImageTagFor(baseContent), "ctxloom-agent-base", baseContentHash(baseContent))
	agentOwner := assertOwnershipTagged(t, agent, "ctxloom-agent-mock:v1-cX-abc", "ctxloom-agent-mock", "abc")
	assert.Contains(t, agent, "--label ctxloom.image=composed")
	assert.Contains(t, agent, "--label ctxloom.slot=abc")
	assert.Contains(t, agent, "--label ctxloom.companions=cX")
	assert.Contains(t, agent, "--label ctxloom.from="+baseOwner, "the parent is named by the tag that never moves")
	assert.Contains(t, agent, "--build-arg BASE_IMAGE="+baseOwner, "the agent stage FROMs exactly the base its own flight built")

	require.NoError(t, buildFromSource(context.Background(), rt, id, src, selfExe, false, nil))
	again := buildInvocations(t, logFile)[3]
	assert.NotContains(t, again, agentOwner, "a rebuild mints a NEW ownership tag; one is never reused")
}

// assertOwnershipTagged checks one logged build line tags primary FIRST (the
// fake runtime keys the built image on it), then a per-build ownership tag on
// the same repository, and stamps that ownership tag — not the primary, which
// a rebuild moves — as ctxloom.tag. It returns the ownership tag.
func assertOwnershipTagged(t *testing.T, line, primary, repo, slot string) string {
	t.Helper()
	var tags []string
	for _, m := range regexp.MustCompile(`-t (\S+)`).FindAllStringSubmatch(line, -1) {
		tags = append(tags, m[1])
	}
	require.Len(t, tags, 2, "primary and ownership tag: %s", line)
	assert.Equal(t, primary, tags[0])
	owner := tags[1]
	assert.Regexp(t, `^`+regexp.QuoteMeta(repo)+`:own-(v[0-9.]+-[0-9a-f]+-)?`+slot+`-[0-9]{8}T[0-9]{6}-[0-9a-f]{8}$`, owner)
	_, tag, _ := strings.Cut(owner, ":")
	assert.Regexp(t, `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`, tag, "a valid docker/podman tag")
	assert.Contains(t, line, "--label ctxloom.tag="+owner+" ", "ownership is proved by the tag that never moves")
	return owner
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
