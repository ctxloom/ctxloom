package isolation

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.NotContains(t, p.overlayDirs, ".codex")

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
// backend name keeps the pre-spec semantics for image/overlay/build shape
// — the generic image, NO local build (run if the image is present, degrade
// if not) — but no longer fails OPEN on credentials. Before
// the fix the default wired resolveClaudeContainerAuth, so any unrecognized
// engine (registry.go's generic "acp" backend) got the user's
// ANTHROPIC_API_KEY/
// ANTHROPIC_AUTH_TOKEN passed through and ~/.claude credentials copy-mounted
// into a foreign engine's container. It must now fail CLOSED: resolveAuth
// always returns ok=false, and the hint names the missing spec rather
// than Anthropic's env vars.
func TestEngineContainerSpecFor_UnknownIsDefault(t *testing.T) {
	for _, name := range []string{"", "no-such-engine"} {
		p := engineContainerSpecFor(name)
		assert.Equal(t, defaultContainerImage, p.image, "backend %q", name)
		assert.Nil(t, p.engineInstall, "backend %q is not composable", name)
		assert.Contains(t, p.overlayDirs, ".claude", "backend %q", name)
		require.NotNil(t, p.resolveAuth, "backend %q must still wire a resolver, just one that fails closed", name)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-test")
		_, ok := p.resolveAuth("/root", t.TempDir())
		assert.False(t, ok, "backend %q must NOT authenticate as claude — no spec is registered for it", name)
		assert.NotContains(t, p.authHint, "ANTHROPIC_API_KEY", "backend %q must not inherit claude's degrade hint", name)
	}
}

// TestEngineContainerSpecFor_NoRegisteredEngineReachesClaudeDefault is a
// regression guard: every REGISTERED backend in the composable set must
// resolve its OWN auth — none of them may reach
// resolveClaudeContainerAuth/defaultOverlayDirs, the security edge where a
// containerized codex/opencode run would silently authenticate with (or
// overlay) the user's Anthropic credentials into a foreign engine.
func TestEngineContainerSpecFor_NoRegisteredEngineReachesClaudeDefault(t *testing.T) {
	withFakeHome(t) // no real ~/.codex or ~/.local/share/opencode creds to fall back onto
	claudeDefault := engineContainerSpecFor("")
	for _, name := range []string{"codex", "opencode"} {
		p := engineContainerSpecFor(name)
		require.NotNil(t, p.resolveAuth, "backend %q must wire its own auth resolver", name)
		// Behavioral check: feed the resolver an ANTHROPIC_API_KEY only and
		// confirm it does NOT authenticate via it — a func value can't be
		// compared for equality, so this proves it is not
		// resolveClaudeContainerAuth by behavior rather than identity. The
		// fake (creds-free) home means the ONLY way any resolver could
		// return ok=true here is misreading ANTHROPIC_API_KEY.
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("OPENROUTER_API_KEY", "")
		_, ok := p.resolveAuth("/root", t.TempDir())
		assert.False(t, ok, "backend %q must NOT authenticate off ANTHROPIC_API_KEY (that would be the claude-shaped security edge)", name)
		assert.NotEqual(t, claudeDefault.authHint, p.authHint, "backend %q must not inherit claude's degrade hint verbatim", name)
	}
}

// TestEngineContainerSpecFor_Codex pins that codex is composable (its own
// official-installer fragment) AND has its own auth/overlay set — no longer
// inheriting the default (claude) spec's auth axis.
func TestEngineContainerSpecFor_Codex(t *testing.T) {
	p := engineContainerSpecFor("codex")
	assert.NotNil(t, p.engineInstall, "codex is composable")
	assert.Equal(t, "codex --version", p.validate)
	assert.Contains(t, p.overlayDirs, ".codex")
	assert.NotContains(t, p.overlayDirs, ".claude", "codex writes no .claude config")
	assert.Contains(t, p.authHint, "OPENAI_API_KEY")
	require.NotNil(t, p.resolveAuth)

	t.Setenv("OPENAI_API_KEY", "sk-codex-test")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "with OPENAI_API_KEY set the wired resolver authenticates")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "OPENAI_API_KEY")
}

// TestEngineContainerSpecFor_Opencode pins opencode's own auth/overlay set — no
// longer inheriting the default (claude) spec's auth axis.
func TestEngineContainerSpecFor_Opencode(t *testing.T) {
	p := engineContainerSpecFor("opencode")
	assert.NotNil(t, p.engineInstall, "opencode is composable")
	assert.Equal(t, "opencode --version", p.validate)
	assert.Contains(t, p.overlayDirs, ".opencode")
	assert.NotContains(t, p.overlayDirs, ".claude", "opencode writes no .claude config")
	assert.Contains(t, p.authHint, "OPENROUTER_API_KEY")
	require.NotNil(t, p.resolveAuth)

	t.Setenv("OPENROUTER_API_KEY", "or-test")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "with OPENROUTER_API_KEY set the wired resolver authenticates")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "OPENROUTER_API_KEY")
}

// TestEngineContainerSpecFor_Mock pins mock's own spec: composable (so
// `ctxloom container build mock` no longer refuses with "no local build
// recipe"), a validate command that proves the image without any vendor
// client (mock installs none), an auth resolver that ALWAYS succeeds (mock
// authenticates against no vendor at all — unlike every other engine's
// resolver, which degrades on some path), and an overlay set scoped to
// mock's own managed-config directory (.mock, covering mockSkillsPath's
// .mock/skills) plus the shared .ctxloom/cache — never claude's .claude.
func TestEngineContainerSpecFor_Mock(t *testing.T) {
	p := engineContainerSpecFor("mock")
	assert.Equal(t, defaultContainerImage, p.image)
	assert.NotNil(t, p.engineInstall, "mock must be composable so `container build mock` has a recipe")
	assert.Contains(t, string(p.engineInstall), "cat", "mock's fragment asserts the one thing it actually needs: cat")
	assert.Equal(t, "cat --version", p.validate, "mock has no vendor client to validate; cat is its one real dependency")
	assert.Contains(t, p.overlayDirs, ".mock")
	assert.NotContains(t, p.overlayDirs, ".claude", "mock writes no .claude config")
	assert.Contains(t, p.overlayDirs, filepath.FromSlash(".ctxloom/cache"))
	assert.Empty(t, p.transcriptStoreRel, "mock keeps no transcripts (NilSessionHistory)")

	require.NotNil(t, p.resolveAuth, "the mock spec wires an auth resolver")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "mock authenticates against no vendor, so resolution always succeeds")
	assert.Equal(t, authNone, auth.mode)
	assert.Empty(t, auth.envPassthrough)
	assert.Empty(t, auth.mounts)
}

// TestResolveMockContainerAuth_AlwaysSucceeds is the unit-level pin on the
// resolver itself (as opposed to TestEngineContainerSpecFor_Mock's pin that the
// spec WIRES it): unlike every other resolveXContainerAuth in this
// package, it must return ok=true unconditionally — there is no env var or
// credential file whose presence/absence could flip it, because mock has no
// vendor to authenticate against.
func TestResolveMockContainerAuth_AlwaysSucceeds(t *testing.T) {
	auth, ok := resolveMockContainerAuth("/home/ctxloom", t.TempDir())
	require.True(t, ok)
	assert.Equal(t, authNone, auth.mode)
	assert.Empty(t, auth.envPassthrough)
	assert.Empty(t, auth.mounts)

	// Vary the inputs (a different containerHome/scratchDir, and an empty
	// scratchDir) — the resolver reads neither, so the outcome must not move.
	auth2, ok2 := resolveMockContainerAuth("", "")
	require.True(t, ok2)
	assert.Equal(t, authNone, auth2.mode)
}

// TestNewContainerFor_UsesSpecImage / TestNewContainer_ExplicitImageWins pin
// the two constructors: For resolves the spec's image; the legacy explicit
// image overrides it over the default spec.
func TestNewContainerFor_UsesSpecImage(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	assert.Equal(t, defaultContainerImage, c.image)

	explicit := NewContainerFor(fakeRuntime{name: "docker", available: true}, "mock").WithImage("custom:tag")
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
// that switch checked below — each composable engine, plus the
// unknown/empty default — sets a non-empty store root. This pins that
// reachability argument so the row's premise cannot become true unnoticed: a
// new engine spec that forgets its store root turns this red rather than
// silently losing that engine's transcripts.
//
// mock is the ONE deliberate, documented exception (see engineContainerSpecFor's
// "mock" case doc): it keeps no transcripts at all
// (internal/lm/backends.NewMock wires &NilSessionHistory{}), so "" is the
// CORRECT value there, not an oversight the loop above should catch. It gets
// its own explicit assertion instead of being silently excluded from the
// names list, so a future change that gives mock a non-empty root (or
// accidentally empties some other engine's) is visible either way.
func TestEngineContainerSpecFor_EverySpecMapsATranscriptStore(t *testing.T) {
	names := append(composableEngines(), "", "no-such-engine")
	for _, name := range names {
		p := engineContainerSpecFor(name)
		assert.NotEmpty(t, p.transcriptStoreRel,
			"backend %q must map a native transcript store root; an empty one silently drops the transcript mount in sessionStateMounts", name)
	}
	assert.Empty(t, engineContainerSpecFor("mock").transcriptStoreRel,
		"mock keeps no transcripts (NilSessionHistory) — an empty store root is the correct, deliberate value here")
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
// HasContainerAuth reads the spec table back through noContainerAuthHint
// rather than keeping a second roster, so (1) is a real check of the table and
// not of a copy of it: dropping an engine's resolveAuth turns this red.
func TestContainerAuthEngines_MatchesTheTable(t *testing.T) {
	require.NotEmpty(t, ContainerAuthEngines(), "the supported set a refusal names must not be empty")
	for _, name := range ContainerAuthEngines() {
		assert.True(t, HasContainerAuth(name),
			"ContainerAuthEngines() advertises %q, so engineContainerSpecFor(%q) must map a real auth resolver", name, name)
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
