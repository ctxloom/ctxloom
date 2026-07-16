package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPresentEnvKeys_OnlyKnownSetVars: the scoped auth-env set carries ONLY the
// NAMES of the known auth vars that are actually set — never a value (the value
// would leak into the world-readable `run` argv), and never the host's full
// environment.
func TestPresentEnvKeys_OnlyKnownSetVars(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "", "PATH": "/x"}
	out := presentEnvKeys(func(k string) string { return env[k] }, claudeAuthEnvVars)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, out, "only set, known auth var NAMES cross (no value; empty + unknown dropped)")
}

// withFakeHome points hostHomeDir at a temp dir for hermetic credential tests.
func withFakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	orig := hostHomeDir
	hostHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { hostHomeDir = orig })
	return home
}

// writeCreds writes a host ~/.claude/.credentials.json (and optionally
// ~/.claude.json) under home.
func writeCreds(t *testing.T, home string, withDotClaude bool) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600))
	if withDotClaude {
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600))
	}
}

// TestClaudeCredentialCopyMounts_PresentAndAbsent: absent OAuth creds → not
// ok; when present, the mounts are COPIES of the two credential files under
// scratchDir, mounted read-write into the container HOME, byte-identical to
// the host originals (payload assertion — the copy, not just its presence,
// is what makes the refresh-safe rw mount correct).
func TestClaudeCredentialCopyMounts_PresentAndAbsent(t *testing.T) {
	home := withFakeHome(t)
	scratch := t.TempDir()

	_, ok := claudeCredentialCopyMounts("/root", scratch)
	assert.False(t, ok, "no ~/.claude/.credentials.json → cannot credential-mount")

	writeCreds(t, home, true)
	mounts, ok := claudeCredentialCopyMounts("/root", scratch)
	require.True(t, ok)
	require.Len(t, mounts, 2)
	assert.Equal(t, "/root/.claude/.credentials.json", mounts[0].Container)
	assert.False(t, mounts[0].ReadOnly, "rw so claude's token refresh can write back into the scratch copy")
	assert.NotEqual(t, filepath.Join(home, ".claude", ".credentials.json"), mounts[0].Host, "the mount targets a SCRATCH COPY, never the host original")
	gotCreds, err := os.ReadFile(mounts[0].Host)
	require.NoError(t, err)
	wantCreds, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, wantCreds, gotCreds, "the scratch copy is byte-identical to the host source")

	assert.Equal(t, "/root/.claude.json", mounts[1].Container)
	assert.False(t, mounts[1].ReadOnly)
	assert.NotEqual(t, filepath.Join(home, ".claude.json"), mounts[1].Host)
}

// TestClaudeCredentialCopyMounts_OmitsAbsentDotClaude: ~/.claude.json is
// optional — only the OAuth token file is required.
func TestClaudeCredentialCopyMounts_OmitsAbsentDotClaude(t *testing.T) {
	home := withFakeHome(t)
	writeCreds(t, home, false)
	mounts, ok := claudeCredentialCopyMounts("/root", t.TempDir())
	require.True(t, ok)
	require.Len(t, mounts, 1, "only the OAuth token file is mounted when ~/.claude.json is absent")
	assert.False(t, mounts[0].ReadOnly)
}

// TestResolveClaudeContainerAuth_PrefersEnvThenCredsThenDegrades pins the precedence:
// ANTHROPIC_API_KEY (env passthrough) wins; else credential-mount; else ok=false
// so the caller degrades to None.
func TestResolveClaudeContainerAuth_PrefersEnvThenCredsThenDegrades(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "") // ensure no ambient key
	scratch := t.TempDir()

	// No key, no creds → degrade (the caller falls back to none).
	_, ok := resolveClaudeContainerAuth("/root", scratch)
	assert.False(t, ok, "no resolvable auth → degrade to none")

	// Creds present, still no key → credential-mount.
	writeCreds(t, home, false)
	auth, ok := resolveClaudeContainerAuth("/root", scratch)
	require.True(t, ok)
	assert.Equal(t, authCredentialMount, auth.mode)
	assert.NotEmpty(t, auth.mounts)
	assert.Empty(t, auth.envPassthrough, "credential-mount injects no env")

	// Key present → env passthrough PREFERRED over the mounted creds. The plan
	// carries the NAME only (never the value): the value is forwarded from the
	// launcher's env at run time, so it never reaches the world-readable argv.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok = resolveClaudeContainerAuth("/root", scratch)
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_API_KEY", "the auth var crosses by NAME")
	for _, e := range auth.envPassthrough {
		assert.NotContains(t, e, "sk-test", "the secret VALUE must never be stored in the auth plan")
	}
	assert.Empty(t, auth.mounts, "env passthrough does not mount credentials")
}

// TestResolveClaudeContainerAuth_AuthTokenAlsoTriggers pins the paced-even fix:
// a gateway host authenticates via ANTHROPIC_BASE_URL+ANTHROPIC_AUTH_TOKEN and
// carries no ANTHROPIC_API_KEY at all — the resolver must still prefer env
// passthrough over the credential mount in that case, not degrade to the
// (possibly absent) on-disk creds.
func TestResolveClaudeContainerAuth_AuthTokenAlsoTriggers(t *testing.T) {
	withFakeHome(t) // no ~/.claude credentials on disk
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "gw-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example")

	auth, ok := resolveClaudeContainerAuth("/root", t.TempDir())
	require.True(t, ok, "ANTHROPIC_AUTH_TOKEN alone must trigger env passthrough")
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_AUTH_TOKEN")
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_BASE_URL")
	for _, e := range auth.envPassthrough {
		assert.NotContains(t, e, "gw-token", "the secret VALUE must never be stored in the auth plan")
	}
}

// TestResolveClaudeContainerAuth_TriggersOnApiKeyNotOtherAnthropicVars pins the
// env-passthrough BOUNDARY: the trigger is ANTHROPIC_API_KEY specifically, NOT
// any ANTHROPIC_* var. With another ANTHROPIC_* set alone (base URL / model) but
// no key and no on-disk creds, the resolver must DEGRADE (ok=false, authNone) —
// never select env passthrough — so a run is not launched against a partial,
// keyless auth env. This kills the mutant that would trigger on
// len(presentEnvKeys) > 0 instead of on the key itself.
func TestResolveClaudeContainerAuth_TriggersOnApiKeyNotOtherAnthropicVars(t *testing.T) {
	withFakeHome(t)                             // no ~/.claude credentials on disk
	t.Setenv("ANTHROPIC_API_KEY", "")           // both trigger vars are unset…
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "https://x") // …but OTHER ANTHROPIC_* vars ARE set
	t.Setenv("ANTHROPIC_MODEL", "claude-x")

	auth, ok := resolveClaudeContainerAuth("/root", t.TempDir())
	require.False(t, ok,
		"other ANTHROPIC_* set without ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN (and no creds) must NOT env-trigger — it degrades")
	assert.Equal(t, authNone, auth.mode, "no key and no creds resolves to no auth, not env passthrough")
	assert.Empty(t, auth.envPassthrough, "nothing crosses when the trigger var is absent")
}

// TestContainerAuthMode_String documents the diagnostic labels (no secrets).
func TestContainerAuthMode_String(t *testing.T) {
	assert.Equal(t, "env-passthrough", authEnv.String())
	assert.Equal(t, "credential-mount", authCredentialMount.String())
	assert.Equal(t, "none", authNone.String())
}

// --- Host+worktree credential seeding (grave-prize) -------------------------

// TestCredentialSeedSpecs_ClaudeCodeRegistered pins the registry entry claude
// needs: keyed by the REGISTERED backend name "claude-code" (not "claude" —
// see profile.go's containerProfileFor using the same key), the ANTHROPIC_API_KEY
// trigger, and the "claude" destSubdir that worktree.go's Env() already points
// CLAUDE_CONFIG_DIR at.
func TestCredentialSeedSpecs_ClaudeCodeRegistered(t *testing.T) {
	spec, ok := credentialSeedSpecs["claude-code"]
	require.True(t, ok, "claude-code must have a credentialSeedSpec")
	assert.Equal(t, "claude", spec.engine)
	assert.Equal(t, "claude", spec.destSubdir)
	assert.Equal(t, "ANTHROPIC_API_KEY", spec.envTrigger)
}

// TestCredentialSeedSpecs_CodexRegisteredKiroCredlessButHomed pins the
// balmy-comic/legal-hula shape (per-engine-isolation-home plan §6, replacing
// the old "codex/kiro not registered" pin): codex IS now registered — this
// registry is its ONE credential-seed mechanism (backend.go's
// linkUserCodexAuth symlink is deleted) — with copyable sourceFiles and
// HonoursVarForCreds true (CODEX_HOME relocates creds too). kiro IS also
// registered (for its HomeVars — KIRO_HOME/XDG_DATA_HOME — so Env() has one
// place to read them from) but carries NO sourceFiles: its creds live in a
// global sqlite no per-agent HomeVar relocates, so HonoursVarForCreds is
// false and its XDG_DATA_HOME entry is gated instead of seeded (see
// gateHomeVars in worktree.go). antigravity has no entry at all (no lever).
func TestCredentialSeedSpecs_CodexRegisteredKiroCredlessButHomed(t *testing.T) {
	codexSpec, codexOK := credentialSeedSpecs["codex"]
	require.True(t, codexOK, "codex now rides this registry's copy-seed, replacing linkUserCodexAuth's symlink")
	assert.NotNil(t, codexSpec.sourceFiles, "codex has a copyable auth.json to seed")
	assert.True(t, codexSpec.HonoursVarForCreds, "CODEX_HOME relocates codex's auth.json too")
	require.Len(t, codexSpec.HomeVars, 1)
	assert.Equal(t, "CODEX_HOME", codexSpec.HomeVars[0].EnvVar)

	kiroSpec, kiroOK := credentialSeedSpecs["kiro"]
	require.True(t, kiroOK, "kiro is registered for its HomeVars, even though it has nothing copyable")
	assert.Nil(t, kiroSpec.sourceFiles, "kiro's creds live in a global sqlite no HomeVar relocates — nothing to seed")
	assert.False(t, kiroSpec.HonoursVarForCreds)
	require.Len(t, kiroSpec.HomeVars, 2)

	_, agyOK := credentialSeedSpecs["antigravity"]
	assert.False(t, agyOK, "antigravity has no config-home lever at all (vast-rut)")
}

// TestHostCredentialSeed_SkipsWhenEnvTriggerSet: ANTHROPIC_API_KEY present →
// seeding is skipped entirely (auth rides the env, mirroring
// resolveClaudeContainerAuth's authEnv precedence) — even when no host creds
// exist, this is NOT the fail-loud case, and the destination dir is never
// created.
func TestHostCredentialSeed_SkipsWhenEnvTriggerSet(t *testing.T) {
	withFakeHome(t) // no ~/.claude/.credentials.json on disk
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest)
	require.NoError(t, err)
	assert.Equal(t, seedSkippedEnv, result)
	assert.NoDirExists(t, filepath.Join(dest, "claude"), "no seed dir is created when the env trigger covers auth")
}

// TestHostCredentialSeed_CopiesBothFilesWhenPresent is the PAYLOAD-asserting
// test that would have caught the original grave-prize bug: it does not just
// check for a nil error, it reads the seeded bytes back and proves they are
// byte-identical to the host source, owner-only (0600), and land at the exact
// paths worktree.go's Env() (CLAUDE_CONFIG_DIR = <configHome>/claude) expects.
func TestHostCredentialSeed_CopiesBothFilesWhenPresent(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true)
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest)
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)

	wantCreds, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	gotCreds, err := os.ReadFile(filepath.Join(dest, "claude", ".credentials.json"))
	require.NoError(t, err, "the seeded credential file must exist at <configHome>/claude/.credentials.json")
	assert.Equal(t, wantCreds, gotCreds, "seeded bytes must be byte-identical to the host source")

	info, err := os.Stat(filepath.Join(dest, "claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "seeded credential is owner-only")

	wantDotClaude, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	require.NoError(t, err)
	gotDotClaude, err := os.ReadFile(filepath.Join(dest, "claude", ".claude.json"))
	require.NoError(t, err, "the seeded account-association file must exist too")
	assert.Equal(t, wantDotClaude, gotDotClaude)
}

// TestHostCredentialSeed_OptionalDotClaudeAbsentStillSeedsOK: ~/.claude.json is
// optional (live-verified: claude auto-creates its own inside
// CLAUDE_CONFIG_DIR when absent) — only the OAuth token file is required, and
// its presence alone is sufficient for seedOK.
func TestHostCredentialSeed_OptionalDotClaudeAbsentStillSeedsOK(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest)
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)
	assert.FileExists(t, filepath.Join(dest, "claude", ".credentials.json"))
	assert.NoFileExists(t, filepath.Join(dest, "claude", ".claude.json"), "never fabricated when absent on the host")
}

// TestHostCredentialSeed_NoSourceReturnsNoSourceNotError: no ANTHROPIC_API_KEY
// AND no host ~/.claude/.credentials.json → seedNoSource (the fail-loud case
// the CALLER — worktree.go's seedCredentials — turns into a strictness.Fail),
// never a Go error and never a silently-created empty seed dir.
func TestHostCredentialSeed_NoSourceReturnsNoSourceNotError(t *testing.T) {
	withFakeHome(t) // empty fake home — no .claude at all
	t.Setenv("ANTHROPIC_API_KEY", "")
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest)
	require.NoError(t, err, "nothing seedable is a DECISION, not an I/O error")
	assert.Equal(t, seedNoSource, result)
	assert.NoDirExists(t, filepath.Join(dest, "claude"), "no half-built seed dir is left behind")
}

// TestHostCredentialSeed_UnresolvableHostHome: hostHomeDir failing (the seam
// tests point elsewhere, but production would see this if os.UserHomeDir
// errors) degrades to seedNoSource exactly like an absent source file — never
// a hard error that would abort provisioning outright.
func TestHostCredentialSeed_UnresolvableHostHome(t *testing.T) {
	orig := hostHomeDir
	hostHomeDir = func() (string, error) { return "", assertErr("no home") }
	t.Cleanup(func() { hostHomeDir = orig })
	t.Setenv("ANTHROPIC_API_KEY", "")
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest)
	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result)
}

// writeCodexAuth writes a host ~/.codex/auth.json under home.
func writeCodexAuth(t *testing.T, home string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":"x"}`), 0o600))
}

// =============================================================================
// SeedCodexHome — the exported seam internal/codex's Setup (ensureCodexCredentials,
// warm-yodel) calls to extend THIS package's copy-based codex credential seeding
// to the in-tree/None axis, which never goes through provisionConfigHome at all.
// =============================================================================

// TestSeedCodexHome_CopiesAuthJson is the PAYLOAD-asserting case: the host's
// ~/.codex/auth.json lands byte-identical, owner-only, at destDir/.codex/auth.json
// — exactly where cellScopedCodexHome(destDir) resolves CODEX_HOME to.
func TestSeedCodexHome_CopiesAuthJson(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("OPENAI_API_KEY", "")
	writeCodexAuth(t, home)
	dest := t.TempDir()

	skipped, err := SeedCodexHome(dest)
	require.NoError(t, err)
	assert.False(t, skipped)

	want, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dest, ".codex", "auth.json"))
	require.NoError(t, err, "seeded auth.json must exist at destDir/.codex/auth.json")
	assert.Equal(t, want, got, "seeded bytes are byte-identical to the host source")

	info, err := os.Stat(filepath.Join(dest, ".codex", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "seeded credential is owner-only")
}

// TestSeedCodexHome_EnvTriggerSkips: OPENAI_API_KEY set → skipped=true, nil
// error, no copy attempted (matches hostCredentialSeed's envTrigger precedence).
func TestSeedCodexHome_EnvTriggerSkips(t *testing.T) {
	withFakeHome(t) // no ~/.codex/auth.json on disk
	t.Setenv("OPENAI_API_KEY", "sk-test")
	dest := t.TempDir()

	skipped, err := SeedCodexHome(dest)
	require.NoError(t, err)
	assert.True(t, skipped)
	assert.NoDirExists(t, filepath.Join(dest, ".codex"))
}

// TestSeedCodexHome_NoSourceFailsLoud is warm-yodel's fail-loud contract: no
// OPENAI_API_KEY and no host ~/.codex/auth.json returns a non-nil, actionable
// error — NEVER a silent success that would let codex launch straight into a 401.
func TestSeedCodexHome_NoSourceFailsLoud(t *testing.T) {
	withFakeHome(t) // empty fake home — no .codex at all
	t.Setenv("OPENAI_API_KEY", "")
	dest := t.TempDir()

	_, err := SeedCodexHome(dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENAI_API_KEY")
	assert.Contains(t, err.Error(), "auth.json")
}
