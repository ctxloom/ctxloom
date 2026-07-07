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
