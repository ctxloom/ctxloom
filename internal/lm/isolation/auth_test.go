package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScopedEnv_OnlyKnownSetVars: the scoped auth-env set carries ONLY the known
// auth vars that are actually set — never the host's full environment.
func TestScopedEnv_OnlyKnownSetVars(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "", "PATH": "/x"}
	out := scopedEnv(func(k string) string { return env[k] }, claudeAuthEnvVars)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY=k"}, out, "only set, known auth vars cross (empty + unknown dropped)")
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
	assert.Empty(t, auth.env, "credential-mount injects no env")

	// Key present → env passthrough PREFERRED over the mounted creds.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok = resolveClaudeContainerAuth("/root")
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.env, "ANTHROPIC_API_KEY=sk-test")
	assert.Empty(t, auth.mounts, "env passthrough does not mount credentials")
}

// TestContainerAuthMode_String documents the diagnostic labels (no secrets).
func TestContainerAuthMode_String(t *testing.T) {
	assert.Equal(t, "env-passthrough", authEnv.String())
	assert.Equal(t, "credential-mount", authCredentialMount.String())
	assert.Equal(t, "none", authNone.String())
}
