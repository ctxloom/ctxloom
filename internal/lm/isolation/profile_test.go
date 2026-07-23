package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerProfileFor_Claude pins the claude-code profile: the generic agent
// image (compat with the container-build-claude tagging), a local-build recipe,
// and the .claude overlay set.
func TestContainerProfileFor_Claude(t *testing.T) {
	p := containerProfileFor("claude-code")
	assert.Equal(t, defaultContainerImage, p.image)
	assert.Empty(t, p.officialImage, "no publicly-resolvable official claude-code image (verified live); the composed engine fragment builds")
	assert.Empty(t, p.containerfile, "composable via engineInstall, not the retired embedded monolithic Containerfile")
	assert.NotEmpty(t, p.engineInstall, "claude is composable (official npm installer fragment)")
	assert.Contains(t, string(p.engineInstall), "npm install -g @anthropic-ai/claude-code")
	assert.Equal(t, "claude --version", p.validate)
	assert.Contains(t, p.overlayDirs, ".claude")
	assert.NotContains(t, p.overlayDirs, ".kiro")

	// The auth axis: the degrade hint names claude's trigger var, and the wired
	// resolver IS the claude (ANTHROPIC_*) one — asserted behaviorally since a
	// func value is not directly comparable.
	assert.Contains(t, p.authHint, "ANTHROPIC_API_KEY", "the degrade hint names claude's trigger var")
	require.NotNil(t, p.resolveAuth, "the claude profile wires an auth resolver")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "with ANTHROPIC_API_KEY set the wired resolver authenticates")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_API_KEY", "the wired resolver is the claude (ANTHROPIC_*) resolver")
}

// TestContainerProfileFor_Kiro pins the kiro profile: its OWN image tag (a kiro
// run in a claude image would fail at engine spawn, worse than degrading), a
// local-build recipe, and the .kiro overlay set.
func TestContainerProfileFor_Kiro(t *testing.T) {
	p := containerProfileFor("kiro")
	assert.Equal(t, "ctxloom-agent-kiro:latest", p.image)
	assert.Empty(t, p.officialImage, "kiro ships no official container image (community images are not a trustworthy base)")
	assert.Empty(t, p.containerfile, "composable via engineInstall, not the retired embedded monolithic Containerfile")
	assert.NotEmpty(t, p.engineInstall, "kiro is composable (official installer fragment)")
	assert.Contains(t, string(p.engineInstall), "cli.kiro.dev/install")
	assert.Equal(t, "kiro-cli --version", p.validate)
	assert.Contains(t, p.overlayDirs, ".kiro")
	assert.NotContains(t, p.overlayDirs, ".claude", "kiro writes no .claude config")

	// The auth axis: the degrade hint names kiro's trigger var, and the wired
	// resolver IS the kiro (KIRO_API_KEY) one — not claude's — asserted
	// behaviorally since a func value is not directly comparable.
	assert.Contains(t, p.authHint, "KIRO_API_KEY", "the degrade hint names kiro's trigger var")
	require.NotNil(t, p.resolveAuth, "the kiro profile wires an auth resolver")
	t.Setenv("KIRO_API_KEY", "kiro-test")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "with KIRO_API_KEY set the wired resolver authenticates")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "KIRO_API_KEY", "the wired resolver is the kiro (KIRO_API_KEY) resolver")
}

// TestContainerProfileFor_UnknownIsDefault: a genuinely unknown/unregistered
// backend name keeps the pre-profile semantics — the generic image, claude
// auth, NO local build (run if the image is present, degrade if not). This is
// now the ONLY door into the default profile — every REGISTERED backend
// (claude-code/kiro/codex/opencode/antigravity) has its own case with its
// own auth, asserted by the tests below (the paced-even regression guard).
func TestContainerProfileFor_UnknownIsDefault(t *testing.T) {
	for _, name := range []string{"", "mock"} {
		p := containerProfileFor(name)
		assert.Equal(t, defaultContainerImage, p.image, "backend %q", name)
		assert.Empty(t, p.containerfile, "backend %q has no local-build recipe", name)
		assert.Nil(t, p.engineInstall, "backend %q is not composable", name)
		assert.Contains(t, p.overlayDirs, ".claude", "backend %q", name)
		// The default profile is claude-oriented throughout, including auth —
		// legitimate ONLY for a truly unrecognized backend name.
		require.NotNil(t, p.resolveAuth, "backend %q wires the default (claude) auth resolver", name)
		assert.Contains(t, p.authHint, "ANTHROPIC_API_KEY", "backend %q inherits claude's degrade hint", name)
	}
}

// TestContainerProfileFor_NoRegisteredEngineReachesClaudeDefault is the
// paced-even regression guard: every REGISTERED backend (the composable set
// plus antigravity) must resolve its OWN auth — none of them may reach
// resolveClaudeContainerAuth/defaultOverlayDirs, the security edge where a
// containerized codex/opencode/antigravity run would silently authenticate
// with (or overlay) the user's Anthropic credentials into a foreign engine.
func TestContainerProfileFor_NoRegisteredEngineReachesClaudeDefault(t *testing.T) {
	withFakeHome(t) // no real ~/.codex or ~/.local/share/opencode creds to fall back onto
	claudeDefault := containerProfileFor("")
	for _, name := range []string{"codex", "opencode", "antigravity"} {
		p := containerProfileFor(name)
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
		t.Setenv("KIRO_API_KEY", "")
		_, ok := p.resolveAuth("/root", t.TempDir())
		assert.False(t, ok, "backend %q must NOT authenticate off ANTHROPIC_API_KEY (that would be the claude-shaped security edge)", name)
		assert.NotEqual(t, claudeDefault.authHint, p.authHint, "backend %q must not inherit claude's degrade hint verbatim", name)
	}
}

// TestContainerProfileFor_Codex pins bony-spoof: codex is composable (its own
// official-installer fragment) AND has its own auth/overlay set — no longer
// inheriting the default (claude) profile's auth axis.
func TestContainerProfileFor_Codex(t *testing.T) {
	p := containerProfileFor("codex")
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

// TestContainerProfileFor_Opencode pins opencode's own auth/overlay set — no
// longer inheriting the default (claude) profile's auth axis.
func TestContainerProfileFor_Opencode(t *testing.T) {
	p := containerProfileFor("opencode")
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

// TestContainerProfileFor_Antigravity pins the paced-even fix for
// antigravity plus task sweet-fruit's image-half landing: antigravity is now
// COMPOSABLE (its own official-installer fragment, live-verified against
// https://antigravity.google/cli/install.sh), and (fatal-amino, 2026-07-22)
// its AUTH half is now a REAL resolver — it must NOT silently reuse claude's
// auth/overlay, and must degrade (not authenticate) when the host has no
// seedable antigravity OAuth token, even with ANTHROPIC_API_KEY set.
func TestContainerProfileFor_Antigravity(t *testing.T) {
	withFakeHome(t) // hermetic: no real ~/.gemini/antigravity-cli/antigravity-oauth-token to accidentally pick up
	p := containerProfileFor("antigravity")
	assert.Equal(t, defaultContainerImage, p.image, "fallback tag only — the real tag is the composed multi-engine one")
	assert.NotNil(t, p.engineInstall, "antigravity is composable as of task sweet-fruit")
	assert.Equal(t, "agy --version", p.validate)
	assert.Nil(t, p.overlayDirs, "no known project-relative managed-config surface for antigravity yet")
	require.NotNil(t, p.resolveAuth)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	_, ok := p.resolveAuth("/root", t.TempDir())
	assert.False(t, ok, "no host antigravity OAuth token seeded → must degrade, never silently borrow claude's ANTHROPIC_API_KEY")
	assert.Contains(t, p.authHint, "antigravity-oauth-token")
}

// TestNewContainerFor_UsesProfileImage / TestNewContainer_ExplicitImageWins pin
// the two constructors: For resolves the profile's image; the legacy explicit
// image overrides it over the default profile.
func TestNewContainerFor_UsesProfileImage(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "kiro")
	assert.Equal(t, "ctxloom-agent-kiro:latest", c.image)

	explicit := NewContainer(fakeRuntime{name: "docker", available: true}, "custom:tag")
	assert.Equal(t, "custom:tag", explicit.image)
	assert.Empty(t, explicit.profile.containerfile, "an explicit image is never locally built")
}

// TestResolveKiroContainerAuth pins kiro's container auth: KIRO_API_KEY env
// passthrough (headless mode) or nothing — no credential mount until the
// ~/.kiro layout is verified live.
func TestResolveKiroContainerAuth(t *testing.T) {
	t.Setenv("KIRO_API_KEY", "")
	_, ok := resolveKiroContainerAuth("/root", t.TempDir())
	assert.False(t, ok, "no KIRO_API_KEY → degrade (never launch an engine stuck at browser login)")

	t.Setenv("KIRO_API_KEY", "kiro-test")
	auth, ok := resolveKiroContainerAuth("/root", t.TempDir())
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "KIRO_API_KEY", "the auth var crosses by NAME only")
	assert.NotContains(t, auth.envPassthrough, "KIRO_API_KEY=kiro-test", "the secret value must not be stored in the plan")
	assert.Empty(t, auth.mounts, "kiro env passthrough mounts nothing")
}

// TestResolveKiroContainerAuth_AWSRidesAlongOnlyWhenTriggered pins wired-unit's
// AWS/Bedrock passthrough: AWS_* vars ride ALONG in the passthrough when
// KIRO_API_KEY is the trigger, but do NOT stand alone as a trigger of their
// own (that combination needs a live kiro check before it can be trusted —
// see kiroAuthEnvVars' doc).
func TestResolveKiroContainerAuth_AWSRidesAlongOnlyWhenTriggered(t *testing.T) {
	t.Setenv("KIRO_API_KEY", "")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	_, ok := resolveKiroContainerAuth("/root", t.TempDir())
	assert.False(t, ok, "AWS_* alone (no KIRO_API_KEY) must NOT trigger — unverified live combination")

	t.Setenv("KIRO_API_KEY", "kiro-test")
	auth, ok := resolveKiroContainerAuth("/root", t.TempDir())
	require.True(t, ok)
	assert.Contains(t, auth.envPassthrough, "KIRO_API_KEY")
	assert.Contains(t, auth.envPassthrough, "AWS_REGION", "AWS_* rides along once KIRO_API_KEY triggers")
	assert.Contains(t, auth.envPassthrough, "AWS_ACCESS_KEY_ID")
}
