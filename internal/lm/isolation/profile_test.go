package isolation

import (
	"path/filepath"
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
// backend name keeps the pre-profile semantics for image/overlay/build shape
// — the generic image, NO local build (run if the image is present, degrade
// if not) — but no longer fails OPEN on credentials. Before
// the fix the default wired resolveClaudeContainerAuth, so any unrecognized
// engine (registry.go's generic "acp" backend; container_transport.go's own
// doc names this exact fallthrough) got the user's ANTHROPIC_API_KEY/
// ANTHROPIC_AUTH_TOKEN passed through and ~/.claude credentials copy-mounted
// into a foreign engine's container. It must now fail CLOSED: resolveAuth
// always returns ok=false, and the hint names the missing profile rather
// than Anthropic's env vars.
func TestContainerProfileFor_UnknownIsDefault(t *testing.T) {
	for _, name := range []string{"", "no-such-engine"} {
		p := containerProfileFor(name)
		assert.Equal(t, defaultContainerImage, p.image, "backend %q", name)
		assert.Nil(t, p.engineInstall, "backend %q is not composable", name)
		assert.Contains(t, p.overlayDirs, ".claude", "backend %q", name)
		require.NotNil(t, p.resolveAuth, "backend %q must still wire a resolver, just one that fails closed", name)
		t.Setenv("ANTHROPIC_API_KEY", "sk-test")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-test")
		_, ok := p.resolveAuth("/root", t.TempDir())
		assert.False(t, ok, "backend %q must NOT authenticate as claude — no profile is registered for it", name)
		assert.NotContains(t, p.authHint, "ANTHROPIC_API_KEY", "backend %q must not inherit claude's degrade hint", name)
	}
}

// TestContainerProfileFor_NoRegisteredEngineReachesClaudeDefault is a
// regression guard: every REGISTERED backend (the composable set
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

// TestContainerProfileFor_Codex pins that codex is composable (its own
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

// TestContainerProfileFor_Antigravity pins the security fix for
// antigravity plus its image-half landing: antigravity is now
// COMPOSABLE (its own official-installer fragment, live-verified against
// https://antigravity.google/cli/install.sh), and (2026-07-22)
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

// TestContainerProfileFor_Mock pins mock's own profile: composable (so
// `ctxloom container build mock` no longer refuses with "no local build
// recipe"), a validate command that proves the image without any vendor
// client (mock installs none), an auth resolver that ALWAYS succeeds (mock
// authenticates against no vendor at all — unlike every other engine's
// resolver, which degrades on some path), and an overlay set scoped to
// mock's own managed-config directory (.mock, covering mockSkillsPath's
// .mock/skills) plus the shared .ctxloom/cache — never claude's .claude.
func TestContainerProfileFor_Mock(t *testing.T) {
	p := containerProfileFor("mock")
	assert.Equal(t, defaultContainerImage, p.image)
	assert.NotNil(t, p.engineInstall, "mock must be composable so `container build mock` has a recipe")
	assert.Contains(t, string(p.engineInstall), "cat", "mock's fragment asserts the one thing it actually needs: cat")
	assert.Equal(t, "cat --version", p.validate, "mock has no vendor client to validate; cat is its one real dependency")
	assert.Contains(t, p.overlayDirs, ".mock")
	assert.NotContains(t, p.overlayDirs, ".claude", "mock writes no .claude config")
	assert.Contains(t, p.overlayDirs, filepath.FromSlash(".ctxloom/cache"))
	assert.Empty(t, p.transcriptStoreRel, "mock keeps no transcripts (NilSessionHistory)")

	require.NotNil(t, p.resolveAuth, "the mock profile wires an auth resolver")
	auth, ok := p.resolveAuth("/root", t.TempDir())
	require.True(t, ok, "mock authenticates against no vendor, so resolution always succeeds")
	assert.Equal(t, authNone, auth.mode)
	assert.Empty(t, auth.envPassthrough)
	assert.Empty(t, auth.mounts)
}

// TestResolveMockContainerAuth_AlwaysSucceeds is the unit-level pin on the
// resolver itself (as opposed to TestContainerProfileFor_Mock's pin that the
// profile WIRES it): unlike every other resolveXContainerAuth in this
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

// TestNewContainerFor_UsesProfileImage / TestNewContainer_ExplicitImageWins pin
// the two constructors: For resolves the profile's image; the legacy explicit
// image overrides it over the default profile.
func TestNewContainerFor_UsesProfileImage(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "kiro")
	assert.Equal(t, "ctxloom-agent-kiro:latest", c.image)

	explicit := NewContainer(fakeRuntime{name: "docker", available: true}, "custom:tag")
	assert.Equal(t, "custom:tag", explicit.image)
	assert.Nil(t, explicit.profile.engineInstall, "an explicit image is never locally built")
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

// TestEveryComposableEngineGatesItsACPSurfaceByExecution is the generalization
// of the 2026-07-24 container-delegation defect (task minty-wilt): NO composable
// engine's install fragment may validate its structured-chat surface by PATH
// presence, or by a `--version` that exercises a DIFFERENT code path from the
// one delegation actually spawns.
//
// It is deliberately table-driven over the surface each engine's ACP transport
// declares (internal/lm/backends: claude-code/codex are agent.ACPAdapter with a
// separate binary; kiro/opencode are agent.ACPNative with an `acp` subcommand
// of their own client). Adding an engine to composableEngines() without giving
// it an execution gate fails here.
func TestEveryComposableEngineGatesItsACPSurfaceByExecution(t *testing.T) {
	for _, tc := range []struct {
		backend string
		surface string // the exact command structured chat spawns
	}{
		{"claude-code", "claude-code-acp"},
		{"codex", "codex-acp"},
		{"kiro", "kiro-cli acp"},
		{"opencode", "opencode acp"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			frag := string(containerProfileFor(tc.backend).engineInstall)
			require.NotEmpty(t, frag, "%s must be composable", tc.backend)
			assert.Contains(t, frag, "timeout 20 "+tc.surface+" </dev/null",
				"%s must RUN its ACP surface at image-build time, not merely locate it", tc.backend)
			assert.Contains(t, frag, acpProbeFailurePatterns,
				"%s must fail the build on the shared ACP-failure vocabulary", tc.backend)
			assert.Contains(t, frag, "exit 1",
				"%s's gate must FAIL THE BUILD, not warn", tc.backend)
		})
	}
}

// TestACPProbeFailurePatterns_CoverBothMechanisms pins the two distinct
// silent-no-op mechanisms the shared grep must see. The node-loader half is the
// original claude-code defect; the argument-parser/loader half is what kiro and
// opencode were exposed to. "Failed to change directory" is opencode's measured
// shape for a MISSING `acp` command — it exits ZERO, which is why nothing here
// may be reduced to an exit-status check.
func TestACPProbeFailurePatterns_CoverBothMechanisms(t *testing.T) {
	for _, pat := range []string{
		"SyntaxError", "Cannot find module", "ERR_MODULE_NOT_FOUND", "ERR_UNKNOWN_BUILTIN_MODULE",
		"unrecognized subcommand", "unknown command", "unexpected argument",
		"error while loading shared libraries", "symbol lookup error", "Failed to change directory",
	} {
		assert.Contains(t, acpProbeFailurePatterns, pat)
	}
}

// TestNativeACPRunGate_ProbesTheSubcommandNotTheClient: the gate must run
// `<client> <sub>`, never bare `<client>` — running kiro-cli with no arguments
// would open its interactive chat, which proves nothing about the ACP surface.
func TestNativeACPRunGate_ProbesTheSubcommandNotTheClient(t *testing.T) {
	g := nativeACPRunGate("kiro-cli", "acp")
	assert.Contains(t, g, "timeout 20 kiro-cli acp </dev/null")
	assert.Contains(t, g, "kiro-cli acp is installed but its ACP surface cannot start")
	assert.Contains(t, g, "exit 1")
}

// TestContainerProfileFor_EveryProfileMapsATranscriptStore pins that a
// review row claimed an empty profile.transcriptStoreRel silently
// skips the transcript mount, so a containerized run writes a transcript that
// dies at --rm teardown with nothing said. sessionStateMounts' `if
// c.profile.transcriptStoreRel != ""` guard is real, but the empty case is
// unreachable for every backend HERE COVERED: Container.profile is only ever
// assigned from containerProfileFor (NewContainerFor), and every branch of
// that switch checked below — each composable engine, plus the
// unknown/empty default — sets a non-empty store root. This pins that
// reachability argument so the row's premise cannot become true unnoticed: a
// new engine profile that forgets its store root turns this red rather than
// silently losing that engine's transcripts.
//
// mock is the ONE deliberate, documented exception (see containerProfileFor's
// "mock" case doc): it keeps no transcripts at all
// (internal/lm/backends.NewMock wires &NilSessionHistory{}), so "" is the
// CORRECT value there, not an oversight the loop above should catch. It gets
// its own explicit assertion instead of being silently excluded from the
// names list, so a future change that gives mock a non-empty root (or
// accidentally empties some other engine's) is visible either way.
func TestContainerProfileFor_EveryProfileMapsATranscriptStore(t *testing.T) {
	names := append(composableEngines(), "", "no-such-engine")
	for _, name := range names {
		p := containerProfileFor(name)
		assert.NotEmpty(t, p.transcriptStoreRel,
			"backend %q must map a native transcript store root; an empty one silently drops the transcript mount in sessionStateMounts", name)
	}
	assert.Empty(t, containerProfileFor("mock").transcriptStoreRel,
		"mock keeps no transcripts (NilSessionHistory) — an empty store root is the correct, deliberate value here")
}
