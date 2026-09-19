package isolation

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport"
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

	// The auth axis: the degrade hint names claude's trigger var, and the wired
	// resolver IS the claude (ANTHROPIC_*) one — asserted behaviorally since a
	// func value is not directly comparable.
	assert.Contains(t, p.authHint, "ANTHROPIC_API_KEY", "the degrade hint names claude's trigger var")
	require.NotNil(t, p.resolveAuth, "the claude spec wires an auth resolver")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "with ANTHROPIC_API_KEY set the wired resolver authenticates")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_API_KEY", "the wired resolver is the claude (ANTHROPIC_*) resolver")
}

// TestEngineContainerSpecFor_UnknownIsDefault: a genuinely unknown/unregistered
// backend name keeps the generic image, NO local build (run if the image is
// present, degrade if not) — and fails CLOSED on credentials. Before the fix
// the default wired claude's resolver, so any unrecognized engine got the
// user's ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN passed through and ~/.claude
// credentials mounted into a foreign engine's container. resolveAuth always
// returns ok=false, and the hint names the missing declaration rather than
// Anthropic's env vars. Its overlay set is ctxloom's own cache dir alone: no
// engine declared anything, so nothing engine-shaped is shadowed.
func TestEngineContainerSpecFor_UnknownIsDefault(t *testing.T) {
	for _, name := range []string{"", "no-such-engine"} {
		p := engineContainerSpecFor(name)
		assert.Equal(t, defaultContainerImage, p.image, "backend %q", name)
		assert.Nil(t, p.engineInstall, "backend %q is not composable", name)
		assert.Equal(t, []string{ctxloomCacheOverlayDir}, p.overlayDirs, "backend %q", name)
		require.NotNil(t, p.resolveAuth, "backend %q must still wire a resolver, just one that fails closed", name)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-test")
		_, ok := p.resolveAuth("/root", t.TempDir())
		assert.False(t, ok, "backend %q must NOT authenticate as claude — no declaration is registered for it", name)
		assert.NotContains(t, p.authHint, "ANTHROPIC_API_KEY", "backend %q must not inherit claude's degrade hint", name)
	}
}

// vendorlessFixture is the shape of a test double's container declaration:
// composable (a non-nil fragment, so `container build` has a recipe) with
// NO vendor client, an auth plan that authenticates against nothing, its own
// overlay dir, and no transcript store. It is what internal/lm/backends'
// mock declares; this binary cannot link that package, so the shape is
// authored here and registered under a fixture name.
func registerVendorlessFixture(t *testing.T, name string, dist engine.Distribution) {
	t.Helper()
	stageEngineFacts(t, name, func(f *EngineFacts) {
		f.Container = agent.Provide(agent.EngineContainer{
			Install:            []byte("RUN command -v cat\n"),
			ValidateCommand:    "cat --version",
			Auth:               agent.Provide(agent.ContainerAuth{Vendorless: name + " authenticates against no vendor"}),
			OverlayDirs:        []string{".mock"},
			TranscriptStoreRel: "",
		})
		f.Distribution = dist
	})
}

// TestEngineContainerSpecFor_Vendorless pins how a vendorless declaration
// becomes a spec: composable, a validate command that proves the image
// without any vendor client, an auth resolver that ALWAYS succeeds (unlike
// every real engine's, which degrades on some path), an overlay set scoped
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
	assert.Nil(t, p.relocatedCredentialMounts, "no credential files, nothing to overlay on a relocated home")

	// The hint is read only when resolveAuth answers !ok, which a vendorless
	// resolver never does, so the ONE string the field can carry here is a
	// sentinel that says so. The declaration side already forbids a real
	// hint (agent.ContainerAuth.Validate: Vendorless excludes Hint); this
	// pins the spec side, so the error a broken invariant would print names
	// the invariant rather than a vendor credential that does not exist.
	assert.True(t, strings.HasPrefix(p.authHint, "unreachable:"), "authHint = %q", p.authHint)
	assert.Contains(t, p.authHint, "vendorless-fixture authenticates against no vendor",
		"the sentinel carries the declaration's own reason, so a reader of the message can see which engine claimed it")

	require.NotNil(t, p.resolveAuth)
	for _, home := range []string{"/root", ""} {
		auth, ok := p.resolveAuth(home, t.TempDir())
		require.True(t, ok, "a vendorless engine's auth resolves unconditionally")
		assert.Equal(t, authNone, auth.mode)
		assert.Empty(t, auth.envPassthrough)
		assert.Empty(t, auth.mounts)
	}
}

// TestEngineContainerSpecFor_DeclaredAbsentFailsClosed: an engine that is
// REGISTERED but declares no container story is indistinguishable from an
// unknown name at the spec — the fail-closed default — but not at the seam:
// it is a declaration, and reads as one.
func TestEngineContainerSpecFor_DeclaredAbsentFailsClosed(t *testing.T) {
	const name = "no-container-fixture"
	stageEngineFacts(t, name, func(f *EngineFacts) {
		f.Container = agent.Absent[agent.EngineContainer](name + " has no container story")
		f.Distribution = engine.DistributionDefault
	})

	assert.False(t, HasContainerAuth(name))
	assert.Equal(t, noContainerAuthHint, engineContainerSpecFor(name).authHint)
	assert.NotContains(t, composableEngines(), name)
	assert.NotContains(t, ContainerAuthEngines(), name)
	r, ok := engineContainerDeclared(name)
	require.True(t, ok, "a declared absence is still a registration")
	assert.NotEmpty(t, r.container.AbsentReason())
}

// TestRosters_ReadCapabilityAndPolicy pins the two roster filters: the default
// image set is installer AND DistributionDefault; the offered container-auth
// set is an auth plan AND not a test double. Capability comes from the
// declaration, policy from Distribution, and neither roster is a list.
func TestRosters_ReadCapabilityAndPolicy(t *testing.T) {
	registerVendorlessFixture(t, "roster-default", engine.DistributionDefault)
	registerVendorlessFixture(t, "roster-optin", engine.DistributionOptIn)
	registerVendorlessFixture(t, "roster-testonly", engine.DistributionTestOnly)

	assert.Contains(t, composableEngines(), "roster-default")
	assert.NotContains(t, composableEngines(), "roster-optin", "opt-in composes only when asked for")
	assert.NotContains(t, composableEngines(), "roster-testonly", "a double is never composed")

	assert.Contains(t, ContainerAuthEngines(), "roster-default")
	assert.Contains(t, ContainerAuthEngines(), "roster-optin", "an opt-in engine is a legitimate thing to bind to by name")
	assert.NotContains(t, ContainerAuthEngines(), "roster-testonly", "a double is never offered")
	for _, name := range []string{"roster-default", "roster-optin", "roster-testonly"} {
		assert.True(t, HasContainerAuth(name), "%s: capability is reported whatever the policy", name)
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
// The fail-closed default never reaches a mount at all — its auth gate is
// upstream — so it is not asserted here.
func TestEngineContainerSpecFor_EverySpecMapsATranscriptStore(t *testing.T) {
	require.NotEmpty(t, composableEngines())
	for _, name := range composableEngines() {
		p := engineContainerSpecFor(name)
		assert.NotEmpty(t, p.transcriptStoreRel,
			"backend %q must map a native transcript store root; an empty one silently drops the transcript mount in sessionStateMounts", name)
	}
}

// TestContainerAuthEngines_MatchesTheTable pins the two halves of the exported
// auth surface config validation refuses bindings with:
//
//  1. every engine ContainerAuthEngines() advertises really does have a
//     container-auth mapping (HasContainerAuth true), so a rejection message
//     can never name an engine the launch would then refuse; and
//  2. the engines with NO mapping — the generic "acp" backend, the empty
//     string the deleted image-only constructors used to pass, and any typo —
//     report false, which is what makes the refusal fire at all.
//
// HasContainerAuth and ContainerAuthEngines read the same declarations, so
// (1) is a real check of the registrations and not of a copy of them.
func TestContainerAuthEngines_MatchesTheTable(t *testing.T) {
	require.NotEmpty(t, ContainerAuthEngines(), "the supported set a refusal names must not be empty")
	for _, name := range ContainerAuthEngines() {
		assert.True(t, HasContainerAuth(name),
			"ContainerAuthEngines() advertises %q, so its declaration must carry a real auth plan", name)
		assert.NotEqual(t, noContainerAuthHint, engineContainerSpecFor(name).authHint,
			"backend %q must carry its OWN degrade hint, not the no-auth marker", name)
	}
	for _, name := range []string{"acp", "", "no-such-engine"} {
		assert.False(t, HasContainerAuth(name),
			"backend %q has no container-auth mapping, so a `runtime: container` binding for it must be refusable", name)
	}
	assert.Equal(t, noContainerAuthHint, engineContainerSpecFor("acp").authHint,
		"the generic acp backend reaches the fail-closed default arm — the case config validation exists to catch before launch")
}

// renamingCredentialFixtureHostRel is the one host file both halves of
// registerRenamingCredentialFixture's declaration name — the shared join key
// (agent.SeedFile.HostRelHome / agent.CredentialFile.HostRelHome) a real
// engine also shares between its two independent declarations.
const renamingCredentialFixtureHostRel = "fixture/creds.json"

// renamingCredentialFixtureDestName is the leaf the fixture's credential seed
// declares (agent.SeedFile.DestName) — DELIBERATELY not
// path.Base(renamingCredentialFixtureHostRel) ("creds.json"), because every
// SHIPPED engine's two declarations happen to agree on that leaf and so
// cannot exercise this divergence at all.
const renamingCredentialFixtureDestName = "renamed-creds.json"

// registerRenamingCredentialFixture registers an engine whose credential
// SEED (the copy path, agent.CredentialSeed.Files) and whose container AUTH
// (the mount path, agent.ContainerAuth.CredentialFiles) declare the SAME
// host file via the shared HostRelHome, but where the seed renames it on
// copy: DestName differs from ContainerRelHome's own leaf. This is the case
// relocatedCredentialMounts must resolve by reading the declared DestName —
// path.Base(ContainerRelHome) alone gives the WRONG answer here.
func registerRenamingCredentialFixture(t *testing.T, name string) {
	t.Helper()
	stageEngineFacts(t, name, func(f *EngineFacts) {
		f.Home = agent.Provide(agent.EngineHome{Credentials: agent.Provide(agent.CredentialSeed{
			Subdir:    "fixture-home",
			LoginHint: name + " login",
			Files: []agent.SeedFile{
				{HostRelHome: renamingCredentialFixtureHostRel, DestName: renamingCredentialFixtureDestName, Required: true},
			},
		})})
		f.Container = agent.Provide(agent.EngineContainer{
			Install:         []byte("RUN command -v cat\n"),
			ValidateCommand: "cat --version",
			Auth: agent.Provide(agent.ContainerAuth{
				EnvTriggers: []string{"CTXLOOM_TEST_NEVER_SET_" + name},
				CredentialFiles: []agent.CredentialFile{
					{HostRelHome: renamingCredentialFixtureHostRel, ContainerRelHome: renamingCredentialFixtureHostRel},
				},
				Hint: name + " has no credential to authenticate with",
			}),
		})
		f.Distribution = engine.DistributionTestOnly
	})
}

// TestRelocatedCredentialMounts_UsesDeclaredDestNameNotContainerRelHomeLeaf is
// the renaming fixture splendid-lasso asked for: with an engine whose seed
// RENAMES the file on copy, the relocated-home mount must land at the
// DECLARED DestName, not at a re-derived path.Base(ContainerRelHome). Every
// shipped engine's two declarations happen to agree on that leaf (claude's
// do), so only a fixture that deliberately disagrees can tell "read the
// declaration" apart from "re-derive it" — this is that fixture.
//
// Reverting the fix (path.Base(f.ContainerRelHome) instead of the
// seededLeafFor lookup) turns this red: it computes "creds.json" here, not
// the declared "renamed-creds.json".
func TestRelocatedCredentialMounts_UsesDeclaredDestNameNotContainerRelHomeLeaf(t *testing.T) {
	home := testsupport.Isolate(t)
	const name = "renaming-credential-fixture"
	registerRenamingCredentialFixture(t, name)

	hostFile := filepath.Join(home, filepath.FromSlash(renamingCredentialFixtureHostRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(hostFile), 0o755))
	require.NoError(t, os.WriteFile(hostFile, []byte("secret"), 0o600))

	spec := engineContainerSpecFor(name)
	require.NotNil(t, spec.relocatedCredentialMounts,
		"guard: the fixture must wire a relocated-mount func, or the assertions below are vacuous")

	const engineHome = "/relocated/fixture-home"
	mounts, ok := spec.relocatedCredentialMounts(engineHome)
	require.True(t, ok, "the host file exists, so the mount must resolve")
	require.Len(t, mounts, 1)

	derivedLeaf := path.Base(renamingCredentialFixtureHostRel)
	require.NotEqual(t, renamingCredentialFixtureDestName, derivedLeaf,
		"guard: the fixture's declared DestName must actually differ from Base(ContainerRelHome), or this test cannot observe the bug at all")

	assert.Equal(t, path.Join(engineHome, renamingCredentialFixtureDestName), mounts[0].Container,
		"the mount must land at the seeded copy's ACTUAL name (the declared SeedFile.DestName)")
	assert.NotEqual(t, path.Join(engineHome, derivedLeaf), mounts[0].Container,
		"the mount must NOT land at a re-derived Base(ContainerRelHome) leaf — that is the pre-fix bug: the mount would miss the seeded copy and the engine would authenticate from a stale credential")
	assert.Equal(t, hostFile, mounts[0].Host)
}
