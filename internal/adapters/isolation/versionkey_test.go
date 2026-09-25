package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVersionCommitKey_CutsTheBuildTimestamp is the trap this whole change had
// to avoid. version.Version embeds a build timestamp, so keying an image on the
// raw stamp would change it on every build — reproducing exactly the churn the
// key exists to remove. The key must be the stamp's STABLE prefix: semver plus
// short sha.
func TestVersionCommitKey_CutsTheBuildTimestamp(t *testing.T) {
	const want = "v0.7.0-abc1234"
	assert.Equal(t, want, versionCommitKey("v0.7.0-abc1234-20260904T031516"))

	// Same commit, built later — the ONLY difference is the timestamp.
	assert.Equal(t, want, versionCommitKey("v0.7.0-abc1234-20261225T121212"),
		"two builds of one commit must share a commit key")

	// A dirty build is still THAT commit as far as the tag is concerned; the
	// dirty marker is carried by versionProvenanceKey instead, so a dirty
	// rebuild reuses the tag rather than leaking a fresh image per build.
	assert.Equal(t, want, versionCommitKey("v0.7.0-abc1234-20260904T031516-dirty"))

	assert.NotEqual(t, want, versionCommitKey("v0.7.0-def5678-20260904T031516"), "a different sha")
	assert.NotEqual(t, want, versionCommitKey("v0.8.0-abc1234-20260904T031516"), "a different semver")

	// Anything short of a whole stamp is untrustable, and an untrustable key
	// disables the check rather than guessing.
	for _, bad := range []string{"", "dev", "v0.7.0-abc1234", "v0.7.0--20260904T031516", "garbage"} {
		assert.Empty(t, versionCommitKey(bad), "versionCommitKey(%q) must refuse", bad)
	}
}

// TestVersionProvenanceKey_DirtyForcesARebuild pins the human's ruling: a
// tracked-dirty tree has no stable identity — two builds of one commit can
// carry different code — so each dirty build must key DIFFERENTLY, which is
// what makes it rebuild. A clean build keys on the commit alone, which is what
// makes it reusable.
func TestVersionProvenanceKey_DirtyForcesARebuild(t *testing.T) {
	clean := versionProvenanceKey("v0.7.0-abc1234-20260904T031516")
	require.NotEmpty(t, clean)
	assert.Equal(t, clean, versionProvenanceKey("v0.7.0-abc1234-20261225T121212"),
		"a clean rebuild of one commit must be REUSED, not rebuilt")

	dirty := versionProvenanceKey("v0.7.0-abc1234-20260904T031516-dirty")
	assert.NotEqual(t, clean, dirty, "a dirty build must not pass as the clean build of its commit")

	assert.NotEqual(t, dirty, versionProvenanceKey("v0.7.0-abc1234-20260904T031517-dirty"),
		"two dirty builds one second apart must key differently, or a dirty rebuild is silently reused")

	for _, bad := range []string{"", "dev", "v0.7.0-abc1234"} {
		assert.Empty(t, versionProvenanceKey(bad), "versionProvenanceKey(%q) must refuse", bad)
	}
}

// TestComposedImageTagFor_VersionsCoexist is the tag half of the fix. Keyed on
// base content and engine alone, ONE tag was shared by every ctxloom version:
// a newer binary rebuilt over it, and switching back rebuilt again, so two
// versions could never both hold an image. Different versions must therefore
// resolve to DIFFERENT tags — while everything the tag already invalidated on
// (base content, engine) must keep invalidating.
func TestComposedImageTagFor_VersionsCoexist(t *testing.T) {
	base := []byte("FROM debian:13\n")
	const engine = "claude-code"

	v1 := composedImageTagFor(base, engine, "v0.7.0-abc1234")
	v2 := composedImageTagFor(base, engine, "v0.7.1-def5678")

	assert.NotEqual(t, v1, v2, "two ctxloom versions must be able to hold images at the same time")
	assert.Equal(t, "ctxloom-agent-claude-code:v0.7.0-abc1234-"+composedContentHash(base, engine), v1,
		"the version and the content hash are both IN the tag, visible in `docker images`")

	// Same version, twice: one tag, so the second build is a cache hit rather
	// than a rebuild. This is the assertion the whole task exists for.
	assert.Equal(t, v1, composedImageTagFor(base, engine, "v0.7.0-abc1234"),
		"one version must resolve to one tag, or nothing is ever reused")

	// The pre-existing invalidators must survive the change.
	assert.NotEqual(t, v1, composedImageTagFor([]byte("FROM debian:12\n"), engine, "v0.7.0-abc1234"),
		"changed base content must still invalidate")
	assert.NotEqual(t, v1, composedImageTagFor(base, "codex", "v0.7.0-abc1234"),
		"a different engine must still invalidate")

	// An unstamped binary must not mint an illegal tag: ":-<hash>" is not a
	// valid reference, so the empty key is OMITTED rather than interpolated.
	assert.Equal(t, "ctxloom-agent-claude-code:"+composedContentHash(base, engine),
		composedImageTagFor(base, engine, ""))
}

// TestComposedIdentity_ReusesWithinAVersionAndSeparatesAcross drives the two
// derivations through the REAL entry point the container path calls, rather
// than through the helpers directly — so a rewiring that left composedIdentity
// reading something else cannot pass.
func TestComposedIdentity_ReusesWithinAVersionAndSeparatesAcross(t *testing.T) {
	spec := engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")}
	identity := func(stamp string) (string, string) {
		orig := binaryVersion
		SetBinaryVersion(stamp)
		defer SetBinaryVersion(orig)
		id, ok := composedIdentity(spec, "", nil, "claude-code")
		image, provenance := id.ref, id.provenance
		require.True(t, ok, "a composable spec always resolves an identity")
		return image, provenance
	}

	img1, prov1 := identity("v0.7.0-abc1234-20260904T031516")
	require.NotEmpty(t, prov1)

	// A SECOND BUILD OF THE SAME VERSION: same tag, same provenance, so
	// imageRunsAsIs reuses instead of rebuilding. Only the stamp's timestamp
	// moved, which is precisely what used to force the rebuild.
	img2, prov2 := identity("v0.7.0-abc1234-20261225T121212")
	assert.Equal(t, img1, img2, "a rebuild of one version must resolve the same tag")
	assert.Equal(t, prov1, prov2, "a rebuild of one version must resolve the same provenance")

	// A DIFFERENT VERSION: different tag, so both images can exist at once.
	img3, prov3 := identity("v0.7.1-def5678-20260904T031516")
	assert.NotEqual(t, img1, img3, "different versions must hold different tags")
	assert.NotEqual(t, prov1, prov3, "different versions must carry different provenance")

	// A TRACKED-DIRTY build of the first version: same tag (no image leak),
	// different provenance (forced rebuild over that tag).
	img4, prov4 := identity("v0.7.0-abc1234-20260904T031516-dirty")
	assert.Equal(t, img1, img4, "a dirty build must reuse the tag rather than leak a new image")
	assert.NotEqual(t, prov1, prov4, "a dirty build must force a rebuild via the provenance")

	// The degrade contract: an unusable stamp yields NO provenance, which
	// imageRunsAsIs turns into "cannot verify" rather than a wrong rebuild.
	_, provNone := identity("")
	assert.Empty(t, provNone, "an unstamped binary disables the staleness check")
}

// TestComposedIdentity_BuildAndLaunchAgreeAndCompanionSetsCoexist forces the
// comparison the staleness gate makes: the identity `ctxloom container build`
// stamps (BuildAgentImage -> composedIdentity) against the one a launch
// resolves (containerFor's tag, provenanceFor's label).
//
// The companion half is what makes the second arm necessary. Which companions
// are ADMITTED depends on the invoking HOME's trust, so two environments at one
// commit can legitimately stage different binaries. When only the provenance
// saw that difference, both resolved ONE tag and each rebuilt over the other's
// image: a `just container-build-claude` in the real HOME was judged stale by
// the very next acceptance cell in its sandboxed HOME, and back again.
func TestComposedIdentity_BuildAndLaunchAgreeAndCompanionSetsCoexist(t *testing.T) {
	spec := engineContainerSpecFor("claude-code")
	require.NotNil(t, spec.engineInstall, "claude-code must be composable, or this test proves nothing")
	rt := fakeRuntime{name: "docker", available: true}
	img := ImageConfig{NoDevcontainerBase: true}

	withCompanions(t, map[string]string{"taskloom": "v1.0.0", "ltk": "v2.0.0"})
	id, ok := composedIdentity(spec, "", nil, "claude-code")
	builtTag, builtLabel := id.ref, id.provenance
	require.True(t, ok)
	require.NotEmpty(t, builtLabel, "the staleness gate must be live, or the assertions below prove nothing")

	launch := containerFor(rt, "claude-code", img)
	assert.Equal(t, builtTag, launch.image, "a launch must look for the tag the build wrote")
	assert.False(t, imageStale(map[string]string{provenanceLabel: builtLabel}, launch.identityFor(nil).provenance),
		"a launch in the environment that built the image must find it current")

	withCompanions(t, map[string]string{})
	elsewhere := containerFor(rt, "claude-code", img)
	assert.NotEqual(t, builtTag, elsewhere.image,
		"an environment admitting different companions stages a different image, so it must not share the tag and rebuild over it")
	assert.True(t, imageStale(map[string]string{provenanceLabel: builtLabel}, elsewhere.identityFor(nil).provenance),
		"the provenance must still tell the two companion sets apart")
}
