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

// TestClaudeCredentialMounts_PresentAndAbsent: absent OAuth creds → not ok; when
// present, the mounts are the two credential files, read-only, mapped into the
// container HOME.
func TestClaudeCredentialMounts_PresentAndAbsent(t *testing.T) {
	home := withFakeHome(t)

	_, ok := claudeCredentialMounts("/root")
	assert.False(t, ok, "no ~/.claude/.credentials.json → cannot credential-mount")

	writeCreds(t, home, true)
	mounts, ok := claudeCredentialMounts("/root")
	require.True(t, ok)
	require.Len(t, mounts, 2)
	assert.Equal(t, Mount{
		Host:      filepath.Join(home, ".claude", ".credentials.json"),
		Container: "/root/.claude/.credentials.json",
		ReadOnly:  true,
	}, mounts[0])
	assert.Equal(t, Mount{
		Host:      filepath.Join(home, ".claude.json"),
		Container: "/root/.claude.json",
		ReadOnly:  true,
	}, mounts[1])
}

// TestClaudeCredentialMounts_OmitsAbsentDotClaude: ~/.claude.json is optional —
// only the OAuth token file is required.
func TestClaudeCredentialMounts_OmitsAbsentDotClaude(t *testing.T) {
	home := withFakeHome(t)
	writeCreds(t, home, false)
	mounts, ok := claudeCredentialMounts("/root")
	require.True(t, ok)
	require.Len(t, mounts, 1, "only the OAuth token file is mounted when ~/.claude.json is absent")
	assert.True(t, mounts[0].ReadOnly)
}

// TestResolveClaudeContainerAuth_PrefersEnvThenCredsThenDegrades pins the precedence:
// ANTHROPIC_API_KEY (env passthrough) wins; else credential-mount; else ok=false
// so the caller degrades to None.
func TestResolveClaudeContainerAuth_PrefersEnvThenCredsThenDegrades(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "") // ensure no ambient key

	// No key, no creds → degrade (the caller falls back to none).
	_, ok := resolveClaudeContainerAuth("/root")
	assert.False(t, ok, "no resolvable auth → degrade to none")

	// Creds present, still no key → credential-mount.
	writeCreds(t, home, false)
	auth, ok := resolveClaudeContainerAuth("/root")
	require.True(t, ok)
	assert.Equal(t, authCredentialMount, auth.mode)
	assert.NotEmpty(t, auth.mounts)
	assert.Empty(t, auth.envPassthrough, "credential-mount injects no env")

	// Key present → env passthrough PREFERRED over the mounted creds. The plan
	// carries the NAME only (never the value): the value is forwarded from the
	// launcher's env at run time, so it never reaches the world-readable argv.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok = resolveClaudeContainerAuth("/root")
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_API_KEY", "the auth var crosses by NAME")
	for _, e := range auth.envPassthrough {
		assert.NotContains(t, e, "sk-test", "the secret VALUE must never be stored in the auth plan")
	}
	assert.Empty(t, auth.mounts, "env passthrough does not mount credentials")
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
	t.Setenv("ANTHROPIC_API_KEY", "")           // the trigger var is unset…
	t.Setenv("ANTHROPIC_BASE_URL", "https://x") // …but OTHER ANTHROPIC_* vars ARE set
	t.Setenv("ANTHROPIC_MODEL", "claude-x")

	auth, ok := resolveClaudeContainerAuth("/root")
	require.False(t, ok,
		"other ANTHROPIC_* set without ANTHROPIC_API_KEY (and no creds) must NOT env-trigger — it degrades")
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

// TestCredentialSeedSpecs_CodexAndKiroNotRegistered documents the deliberate
// exclusions (see credentialSeedSpecs' doc): kiro's creds live in a global
// sqlite KIRO_HOME doesn't relocate, and codex ALREADY seeds itself through a
// separate mechanism (internal/codex/backend.go) that wins over anything this
// package's Env() would ship — registering either here would be inert or wrong.
func TestCredentialSeedSpecs_CodexAndKiroNotRegistered(t *testing.T) {
	_, codexOK := credentialSeedSpecs["codex"]
	assert.False(t, codexOK, "codex seeds its own CODEX_HOME via backend.go's linkUserCodexAuth, not this registry")
	_, kiroOK := credentialSeedSpecs["kiro"]
	assert.False(t, kiroOK, "kiro's KIRO_HOME does not relocate credentials (global sqlite) — nothing to seed")
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
