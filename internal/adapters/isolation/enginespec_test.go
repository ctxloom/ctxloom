package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestEngineContainerSpecFor_Claude pins the claude-code spec: the generic agent
// image (compat with the container-build-claude tagging), a local-build recipe,
// and the .claude overlay set.
func TestEngineContainerSpecFor_Claude(t *testing.T) {
	p := engineContainerSpecFor("claude-code")
	assert.Equal(t, defaultContainerImage, p.image)
	assert.NotEmpty(t, p.engineInstall, "claude is composable (official npm installer fragment)")
	assert.Contains(t, string(p.engineInstall), "npm install -g @anthropic-ai/claude-code")
	assert.Equal(t, "claude --version", p.validate)
	assert.Contains(t, p.overlayDirs, ".claude")
	assert.NotContains(t, p.overlayDirs, ".mock",
		"another REGISTERED engine's overlay dir — naming a deleted engine here would assert an absence nothing could ever violate")

	assert.True(t, p.declared, "claude declares a container story")
}

// TestEngineContainerSpecFor_UnknownIsDefault: a genuinely unknown/unregistered
// backend name keeps the generic image, NO local build (run if the image is
// present, degrade if not) — and fails CLOSED: it is undeclared, so the
// container gate refuses it. Its
// overlay set is ctxloom's own cache dir alone: no
// engine declared anything, so nothing engine-shaped is shadowed.
func TestEngineContainerSpecFor_UnknownIsDefault(t *testing.T) {
	for _, name := range []string{"", "no-such-engine"} {
		p := engineContainerSpecFor(name)
		assert.Equal(t, defaultContainerImage, p.image, "backend %q", name)
		assert.Nil(t, p.engineInstall, "backend %q is not composable", name)
		assert.Equal(t, []string{ctxloomCacheOverlayDir}, p.overlayDirs, "backend %q", name)
		assert.False(t, p.declared, "backend %q has no declaration, so the gate fails closed", name)
	}
}

// vendorlessFixture is the shape of a test double's container declaration:
// composable (a non-nil fragment, so `container build` has a recipe) with
// NO vendor client, no Auth (nothing to authenticate against), its own
// overlay dir, and no transcript store. It is what the mock kind's
// mock declares; this binary cannot link that package, so the shape is
// authored here and registered under a fixture name.
func registerVendorlessFixture(t *testing.T, name string, dist engine.Distribution) {
	t.Helper()
	stageEngineFacts(t, name, func(f *EngineFacts) {
		f.Container = engine.Provide(engine.ContainerSpec{
			Install:            []byte("RUN command -v cat\n"),
			ValidateCommand:    "cat --version",
			OverlayDirs:        []string{".mock"},
			TranscriptStoreRel: "",
		})
		f.Distribution = dist
	})
}

// TestEngineContainerSpecFor_Vendorless pins how a vendorless declaration
// becomes a spec: composable, a validate command that proves the image
// without any vendor client, a declared story, an overlay set scoped
// to the engine's own managed-config directory plus the shared cache — never
// another engine's — and an empty transcript root, which sessionStateMounts
// reads as "nothing to mount", the correct value for an engine that keeps
// none.
func TestEngineContainerSpecFor_Vendorless(t *testing.T) {
	registerVendorlessFixture(t, "vendorless-fixture", engine.DistributionTestOnly)
	p := engineContainerSpecFor("vendorless-fixture")
	assert.Equal(t, defaultContainerImage, p.image)
	assert.NotNil(t, p.engineInstall, "a declared fragment makes the spec composable")
	assert.Equal(t, "cat --version", p.validate)
	assert.Equal(t, []string{".mock", ctxloomCacheOverlayDir}, p.overlayDirs)
	assert.Empty(t, p.transcriptStoreRel)

	assert.True(t, p.declared)
}

// TestEngineContainerSpecFor_DeclaredAbsentFailsClosed: an engine that is
// REGISTERED but declares no container story is indistinguishable from an
// unknown name at the spec — the fail-closed default — but not at the seam:
// it is a declaration, and reads as one.
func TestEngineContainerSpecFor_DeclaredAbsentFailsClosed(t *testing.T) {
	const name = "no-container-fixture"
	stageEngineFacts(t, name, func(f *EngineFacts) {
		f.Container = engine.Absent[engine.ContainerSpec](name + " has no container story")
		f.Distribution = engine.DistributionDefault
	})

	assert.False(t, HasContainerStory(name))
	assert.False(t, engineContainerSpecFor(name).declared)
	assert.NotContains(t, composableEngines(), name)
	assert.NotContains(t, ContainerStoryEngines(), name)
	r, ok := engineContainerDeclared(name)
	require.True(t, ok, "a declared absence is still a registration")
	assert.NotEmpty(t, r.container.AbsentReason())
}

// TestRosters_ReadCapabilityAndPolicy pins the two roster filters: the default
// image set is installer AND DistributionDefault; the offered container set
// is a declared container story AND not a test double. Capability comes from the
// declaration, policy from Distribution, and neither roster is a list.
func TestRosters_ReadCapabilityAndPolicy(t *testing.T) {
	registerVendorlessFixture(t, "roster-default", engine.DistributionDefault)
	registerVendorlessFixture(t, "roster-optin", engine.DistributionOptIn)
	registerVendorlessFixture(t, "roster-testonly", engine.DistributionTestOnly)

	assert.Contains(t, composableEngines(), "roster-default")
	assert.NotContains(t, composableEngines(), "roster-optin", "opt-in composes only when asked for")
	assert.NotContains(t, composableEngines(), "roster-testonly", "a double is never composed")

	assert.Contains(t, ContainerStoryEngines(), "roster-default")
	assert.Contains(t, ContainerStoryEngines(), "roster-optin", "an opt-in engine is a legitimate thing to bind to by name")
	assert.NotContains(t, ContainerStoryEngines(), "roster-testonly", "a double is never offered")
	for _, name := range []string{"roster-default", "roster-optin", "roster-testonly"} {
		assert.True(t, HasContainerStory(name), "%s: capability is reported whatever the policy", name)
	}
	assert.Equal(t, composableEngines(), ComposableEngines())
}

// TestNewContainerFor_UsesSpecImage / TestNewContainer_ExplicitImageWins pin
// the two constructors: For resolves the spec's image; the legacy explicit
// image overrides it over the default spec.
func TestNewContainerFor_UsesSpecImage(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	assert.Equal(t, defaultContainerImage, c.image)

	explicit := NewContainerFor(fakeRuntime{name: "docker", available: true}, "no-such-engine").WithImage("custom:tag")
	assert.Equal(t, "custom:tag", explicit.image)
	assert.Nil(t, explicit.engineSpec.engineInstall, "an explicit image is never locally built")
}

// TestEngineContainerSpecFor_EverySpecMapsATranscriptStore pins that a
// review row claimed an empty spec.transcriptStoreRel silently
// skips the transcript mount, so a containerized run writes a transcript that
// dies at --rm teardown with nothing said. sessionStateMounts' `if
// c.engineSpec.transcriptStoreRel != ""` guard is real, but the empty case is
// unreachable for every backend HERE COVERED: Container.spec is only ever
// assigned from engineContainerSpecFor (NewContainerFor), and every branch of
// that lookup checked below — each composable engine — sets a non-empty
// store root. This pins that
// reachability argument so the row's premise cannot become true unnoticed: a
// new engine spec that forgets its store root turns this red rather than
// silently losing that engine's transcripts.
//
// A test double keeps no transcripts and declares "" deliberately (see
// TestEngineContainerSpecFor_Vendorless); it is never in the default image
// set, so the loop is over the engines whose transcripts a user would lose.
// The fail-closed default never reaches a mount at all — its gate is
// upstream — so it is not asserted here.
func TestEngineContainerSpecFor_EverySpecMapsATranscriptStore(t *testing.T) {
	require.NotEmpty(t, composableEngines())
	for _, name := range composableEngines() {
		p := engineContainerSpecFor(name)
		assert.NotEmpty(t, p.transcriptStoreRel,
			"backend %q must map a native transcript store root; an empty one silently drops the transcript mount in sessionStateMounts", name)
	}
}

// TestContainerStoryEngines_MatchesTheTable pins the two halves of the exported
// auth surface config validation refuses bindings with:
//
//  1. every engine ContainerStoryEngines() advertises really does declare a
//     container story (HasContainerStory true), so a rejection message
//     can never name an engine the launch would then refuse; and
//  2. the engines with NO mapping — the generic "acp" backend, the empty
//     string the deleted image-only constructors used to pass, and any typo —
//     report false, which is what makes the refusal fire at all.
//
// HasContainerStory and ContainerStoryEngines read the same declarations, so
// (1) is a real check of the registrations and not of a copy of them.
func TestContainerStoryEngines_MatchesTheTable(t *testing.T) {
	require.NotEmpty(t, ContainerStoryEngines(), "the supported set a refusal names must not be empty")
	for _, name := range ContainerStoryEngines() {
		assert.True(t, HasContainerStory(name),
			"ContainerStoryEngines() advertises %q, so it must declare a container story", name)
		assert.True(t, engineContainerSpecFor(name).declared,
			"backend %q must reach its OWN declaration, not the fail-closed default", name)
	}
	for _, name := range []string{"acp", "", "no-such-engine"} {
		assert.False(t, HasContainerStory(name),
			"backend %q declares no container story, so a `runtime: container` binding for it must be refusable", name)
	}
	assert.False(t, engineContainerSpecFor("acp").declared,
		"the generic acp backend reaches the fail-closed default arm — the case config validation exists to catch before launch")
}
