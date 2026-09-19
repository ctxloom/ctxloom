package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// TestPresentEnvKeys_OnlyKnownSetVars: the scoped auth-env set carries ONLY the
// NAMES of the known auth vars that are actually set — never a value (the value
// would leak into the world-readable `run` argv), and never the host's full
// environment.
func TestPresentEnvKeys_OnlyKnownSetVars(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "", "PATH": "/x"}
	out := presentEnvKeys(func(k string) string { return env[k] }, claudeAuth(t).EnvPassthrough)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, out, "only set, known auth var NAMES cross (no value; empty + unknown dropped)")
}

// TestHostCredentialSeed_RefusesSymlinkedDestination is the SECURITY pin that
// outlived the copy path it was written against.
//
// The old seed wrote with os.WriteFile, which FOLLOWS a symlink at the
// destination, so a repo-tracked `.credentials.json` link pointing at the real
// `~/.claude/.credentials.json` turned a routine seed into an arbitrary-file
// overwrite of the user's own OAuth token. Copying is gone; the hazard is not,
// because every placement still opens a destination inside an instance home
// nobody validated.
//
// It is refused at the OPEN SYSCALL now (iox.WriteFileInPlace's O_NOFOLLOW),
// which is strictly better than the Lstat-then-write it replaces: there is no
// window between the check and the write for a link to be swapped in. This
// test drives the REAL production path rather than the primitive, so it fails
// if any future placement reaches the filesystem some other way.
func TestHostCredentialSeed_RefusesSymlinkedDestination(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)

	// The victim stands in for the user's REAL credential. Nothing in this
	// test may touch a real one, so it is an ordinary temp file the seed is
	// lured at through a link inside the instance home.
	dest := t.TempDir()
	victim := filepath.Join(dest, "victim.json")
	require.NoError(t, os.WriteFile(victim, []byte("do-not-touch"), 0o600))
	seedDir := filepath.Join(dest, "claude")
	require.NoError(t, os.MkdirAll(seedDir, 0o700))
	require.NoError(t, os.Symlink(victim, filepath.Join(seedDir, ".credentials.json")))

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.Error(t, err, "must refuse to place credential bytes through a symlinked destination")
	assert.Equal(t, seedNoSource, result, "a refused placement never reports success")

	victimContent, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	assert.Equal(t, "do-not-touch", string(victimContent), "the symlink target must be untouched")
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

// TestClaudeCredentialMounts_PresentAndAbsent: absent OAuth creds → not ok;
// when present, the mount SOURCE is the host's REAL ~/.claude/.credentials.json
// (NOT a staged scratch copy), mounted read-write into the container HOME.
// Read-write so claude's in-place token refresh lands in the ONE real file —
// the single source of truth the host also holds — so a container refresh can
// never desync the host's single-use rotating token. The mounted file is the
// FULL real credential: its refresh token is PRESENT (unlike the host+worktree
// axes, which copy an access-token-ONLY credential precisely because a copy
// that refreshes WOULD rotate the host's single-use token). This used to assert
// a SECOND mount carrying ~/.claude.json — removed along with that mount (see
// credentialFileMounts' doc): it leaked the host user's own mcpServers
// registrations into every isolated agent, for mere onboarding convenience
// .credentials.json alone doesn't need.
func TestClaudeCredentialMountsAt_PresentAndAbsent(t *testing.T) {
	home := withFakeHome(t)

	_, ok := credentialFileMounts(claudeAuth(t).CredentialFiles, "/root")
	assert.False(t, ok, "no ~/.claude/.credentials.json → cannot credential-mount")

	realCreds := filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(realCreds), 0o755))
	require.NoError(t, os.WriteFile(realCreds,
		[]byte(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"single-use-rotating-rt"}}`), 0o600))

	mounts, ok := credentialFileMounts(claudeAuth(t).CredentialFiles, "/root")
	require.True(t, ok)
	require.Len(t, mounts, 1, "only the OAuth token file is ever mounted — never ~/.claude.json (tangy-heave)")
	assert.Equal(t, "/root/.claude/.credentials.json", mounts[0].Container)
	assert.False(t, mounts[0].ReadOnly, "rw so claude's token refresh writes back into the ONE real file")
	assert.Equal(t, realCreds, mounts[0].Host,
		"the mount SOURCE is the host's REAL credential — NOT a staged copy (a copy that refreshes would rotate the host's single-use token out from under it)")
	gotCreds, err := os.ReadFile(mounts[0].Host)
	require.NoError(t, err)
	assert.Contains(t, string(gotCreds), "single-use-rotating-rt",
		"the mount carries the FULL real credential, refresh token PRESENT (the container axis is NOT stripped, unlike the host axes)")
}

// TestClaudeCredentialMounts_OmitsDotClaudeEvenWhenPresent: ~/.claude.json is
// never mounted at all, regardless of whether it exists on the host — the OAuth
// token file is the only thing ever mounted.
func TestClaudeCredentialMounts_OmitsDotClaudeEvenWhenPresent(t *testing.T) {
	home := withFakeHome(t)
	writeCreds(t, home, true) // withDotClaude=true: ~/.claude.json DOES exist on the host
	mounts, ok := credentialFileMounts(claudeAuth(t).CredentialFiles, "/root")
	require.True(t, ok)
	require.Len(t, mounts, 1, "~/.claude.json must never be mounted, present or not")
	assert.Equal(t, filepath.Join(home, ".claude", ".credentials.json"), mounts[0].Host, "the REAL credential, no copy")
	assert.False(t, mounts[0].ReadOnly)
}

// TestResolveClaudeContainerAuth_PrefersEnvThenCredsThenDegrades pins the precedence:
// ANTHROPIC_API_KEY (env passthrough) wins; else credential-mount; else ok=false
// so the caller degrades to None.
func TestResolveClaudeContainerAuth_PrefersEnvThenCredsThenDegrades(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "") // ensure no ambient key

	// No key, no creds → degrade (the caller falls back to none).
	_, ok := resolveDeclaredAuth(claudeAuth(t), "/root")
	assert.False(t, ok, "no resolvable auth → degrade to none")

	// Creds present, still no key → credential-mount.
	writeCreds(t, home, false)
	auth, ok := resolveDeclaredAuth(claudeAuth(t), "/root")
	require.True(t, ok)
	assert.Equal(t, authCredentialMount, auth.mode)
	assert.NotEmpty(t, auth.mounts)
	assert.Empty(t, auth.envPassthrough, "credential-mount injects no env")

	// Key present → env passthrough PREFERRED over the mounted creds. The plan
	// carries the NAME only (never the value): the value is forwarded from the
	// launcher's env at run time, so it never reaches the world-readable argv.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	auth, ok = resolveDeclaredAuth(claudeAuth(t), "/root")
	require.True(t, ok)
	assert.Equal(t, authEnv, auth.mode)
	assert.Contains(t, auth.envPassthrough, "ANTHROPIC_API_KEY", "the auth var crosses by NAME")
	for _, e := range auth.envPassthrough {
		assert.NotContains(t, e, "sk-test", "the secret VALUE must never be stored in the auth plan")
	}
	assert.Empty(t, auth.mounts, "env passthrough does not mount credentials")
}

// TestResolveClaudeContainerAuth_AuthTokenAlsoTriggers pins the fix:
// a gateway host authenticates via ANTHROPIC_BASE_URL+ANTHROPIC_AUTH_TOKEN and
// carries no ANTHROPIC_API_KEY at all — the resolver must still prefer env
// passthrough over the credential mount in that case, not degrade to the
// (possibly absent) on-disk creds.
func TestResolveClaudeContainerAuth_AuthTokenAlsoTriggers(t *testing.T) {
	withFakeHome(t) // no ~/.claude credentials on disk
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "gw-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example")

	auth, ok := resolveDeclaredAuth(claudeAuth(t), "/root")
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
	withFakeHome(t)                   // no ~/.claude credentials on disk
	t.Setenv("ANTHROPIC_API_KEY", "") // both trigger vars are unset…
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "https://x") // …but OTHER ANTHROPIC_* vars ARE set
	t.Setenv("ANTHROPIC_MODEL", "claude-x")

	auth, ok := resolveDeclaredAuth(claudeAuth(t), "/root")
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

// --- Host+worktree credential seeding ----------------------------------

// TestCredentialSeed_ClaudeDeclarationReachesTheSeam pins what the pushed
// declaration must carry for the seed to land where the engine looks: keyed
// by the REGISTERED backend name, an env trigger, and the destination leaf
// equal to the one CLAUDE_CONFIG_DIR is pointed at (claude.HomeLeaf).
func TestCredentialSeed_ClaudeDeclarationReachesTheSeam(t *testing.T) {
	seed := claudeSeed(t)
	assert.Contains(t, CredentialSeedEngineNames(), claude.EngineName)
	assert.Equal(t, claude.HomeLeaf, seed.Subdir)
	assert.Equal(t, "ANTHROPIC_API_KEY", seed.EnvTrigger)
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

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	assert.Equal(t, seedSkippedEnv, result)
	assert.NoDirExists(t, filepath.Join(dest, "claude"), "no seed dir is created when the env trigger covers auth")
}

// TestHostCredentialSeed_CopiesCredentialFileWhenPresent is the
// PAYLOAD-asserting test that would have caught the original
// bug: it does not just check for a nil error, it reads the seeded bytes
// back and proves they are byte-identical to the host source, owner-only
// (0600), and land at the exact path worktree.go's Env()
// (CLAUDE_CONFIG_DIR = <configHome>/claude) expects. This used
// to also assert a seeded ~/.claude.json copy — removed along with that
// seed (see claude's descriptor's Home declaration): it leaked
// the host user's own mcpServers registrations into every isolated agent's
// config-home, for mere onboarding convenience .credentials.json alone
// doesn't need.
func TestHostCredentialSeed_CopiesCredentialFileWhenPresent(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true) // withDotClaude=true: host ALSO has ~/.claude.json — must not be seeded
	dest := t.TempDir()

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
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

	assert.NoFileExists(t, filepath.Join(dest, "claude", ".claude.json"),
		"~/.claude.json must never be seeded, present on the host or not (tangy-heave)")
}

// TestHostCredentialSeed_OnlyCredentialFileRequired: only the OAuth token
// file is required for seedOK — and ~/.claude.json is never
// seeded regardless of whether it exists on the host.
func TestHostCredentialSeed_OnlyCredentialFileRequired(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	dest := t.TempDir()

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)
	assert.FileExists(t, filepath.Join(dest, "claude", ".credentials.json"))
	assert.NoFileExists(t, filepath.Join(dest, "claude", ".claude.json"), "never seeded")
}

// TestHostCredentialSeed_NoSourceReturnsNoSourceNotError: no ANTHROPIC_API_KEY
// AND no host ~/.claude/.credentials.json → seedNoSource (the fail-loud case
// the CALLER — worktree.go's seedCredentials — turns into a strictness.Fail),
// never a Go error and never a silently-created empty seed dir.
func TestHostCredentialSeed_NoSourceReturnsNoSourceNotError(t *testing.T) {
	withFakeHome(t) // empty fake home — no .claude at all
	t.Setenv("ANTHROPIC_API_KEY", "")
	dest := t.TempDir()

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
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

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result)
}

// =============================================================================
// CopyAmbient, claude arm — the same one mechanism against claude's allow-list,
// on the IN-TREE AGENT axis where internal/adapters/operations points CLAUDE_CONFIG_DIR
// at a per-session instance (internal/engines/claude.SessionConfigDir).
// =============================================================================

// TestCopyAmbient_Claude_CopiesCredentials is the PAYLOAD-asserting case: the
// host's ~/.claude/.credentials.json lands byte-identical, owner-only, at
// destDir/claude/.credentials.json — exactly where claude.SessionConfigDir
// resolves CLAUDE_CONFIG_DIR to. The empty-source guard (a non-empty fixture,
// re-read and compared) keeps the byte comparison from passing vacuously on two
// empty files, which is this project's signature failure mode.
func TestCopyAmbient_Claude_CopiesCredentials(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)

	src := filepath.Join(home, ".claude", ".credentials.json")
	want, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NotEmpty(t, want, "fixture must carry bytes, or the comparison below proves nothing")

	dest := t.TempDir()
	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: dest, WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.False(t, report.SkippedEnv)
	assert.False(t, report.NoSource)
	assert.Equal(t, 1, report.Copied)

	got, err := os.ReadFile(filepath.Join(dest, "claude", ".credentials.json"))
	require.NoError(t, err, "seeded credential must exist at destDir/claude/.credentials.json")
	assert.Equal(t, want, got, "seeded bytes are byte-identical to the host source")

	info, err := os.Stat(filepath.Join(dest, "claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "seeded credential is owner-only")
}

// TestCopyAmbient_Claude_NeverWritesTheHostHome is the migration-shaped guard for
// an axis that has no migration: the real ~/.claude is READ and never written.
// An in-tree agent home that mutated the human's own home would be the exact
// regression this phase exists to avoid.
func TestCopyAmbient_Claude_NeverWritesTheHostHome(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)

	src := filepath.Join(home, ".claude", ".credentials.json")
	before, err := os.Stat(src)
	require.NoError(t, err)
	original, err := os.ReadFile(src)
	require.NoError(t, err)

	_, err = CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)

	after, err := os.Stat(src)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "the host credential was rewritten")
	nowBytes, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, original, nowBytes, "the host credential's bytes changed")

	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "seeding added files to the host's own ~/.claude")
}

// TestCopyAmbient_Claude_EnvTriggerSkips: ANTHROPIC_API_KEY set → SkippedEnv,
// nil error, no CREDENTIAL copied. Auth rides the environment, so an unseeded
// controlled home is correct and the caller still points CLAUDE_CONFIG_DIR at
// it.
func TestCopyAmbient_Claude_EnvTriggerSkips(t *testing.T) {
	withFakeHome(t) // no ~/.claude on disk
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	dest := t.TempDir()

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: dest, WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.True(t, report.SkippedEnv)
	assert.NoFileExists(t, filepath.Join(dest, "claude", ".credentials.json"))
}

// TestCopyAmbient_Claude_NoSourceFailsLoud pins the fail-loud contract: no
// ANTHROPIC_API_KEY and no host ~/.claude/.credentials.json returns a non-nil,
// actionable error naming fixes that actually work — never a silent success
// that would point claude at an empty home and strand the agent logged out.
func TestCopyAmbient_Claude_NoSourceFailsLoud(t *testing.T) {
	withFakeHome(t) // empty fake home — no .claude at all
	t.Setenv("ANTHROPIC_API_KEY", "")

	report, err := CopyAmbient(AmbientRequest{Engine: "claude-code", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.NoSource)
	assert.Contains(t, report.NoSourceReason, "ANTHROPIC_API_KEY")
	assert.Contains(t, report.NoSourceReason, ".credentials.json")
	assert.Contains(t, report.NoSourceReason, "claude login", "the reason must name a fix that works")
	assert.NotContains(t, report.NoSourceReason, "degraded",
		"--degraded must not be offered as a way past a missing credential: the seeder never consults strictness, so advising it would name a remedy the caller cannot take")
}

// realisticDotClaudeJSON is a stand-in for a real user's ~/.claude.json: on a
// live host that file is not a narrow OAuth-association record — it is
// claude's WHOLE top-level config, including mcpServers entries for the
// user's OWN personal integrations (an incident named Spotify,
// Gmail, Google Drive/Calendar, Todoist). Whatever value is under
// "mcpServers" here stands in for that live confidential data.
const realisticDotClaudeJSON = `{
	"oauthAccount": {"emailAddress": "user@example.com"},
	"mcpServers": {
		"spotify": {"command": "spotify-mcp", "args": ["--token", "SECRET-SPOTIFY-TOKEN"]},
		"gmail": {"command": "gmail-mcp", "env": {"GMAIL_REFRESH_TOKEN": "SECRET-GMAIL-TOKEN"}}
	}
}`

// TestClaudeCredentialMounts_NeverLeaksPersonalMCPConfig is the container-path
// regression test: claudeCredentialMountsAt must not carry the user's own
// mcpServers registrations (or any other non-auth state) into an isolated
// agent's mounted config — an isolated agent seeing the host user's personal
// Spotify/Gmail/etc. integrations (and their embedded tokens) is a
// confidentiality leak, not a convenience. Only the OAuth token file
// (.credentials.json) is ever mounted — already live-verified elsewhere in
// this package's own comments to be sufficient to authenticate — so
// .claude.json (which holds those registrations) never crosses at all.
func TestClaudeCredentialMounts_NeverLeaksPersonalMCPConfig(t *testing.T) {
	home := withFakeHome(t)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(realisticDotClaudeJSON), 0o600))

	mounts, ok := credentialFileMounts(claudeAuth(t).CredentialFiles, "/root")
	require.True(t, ok)
	for _, m := range mounts {
		data, err := os.ReadFile(m.Host)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "mcpServers",
			"an isolated agent's mounted config must never carry the host user's OWN mcpServers registrations")
		assert.NotContains(t, string(data), "SECRET-SPOTIFY-TOKEN")
		assert.NotContains(t, string(data), "SECRET-GMAIL-TOKEN")
	}
}

// TestHostCredentialSeed_NeverLeaksPersonalMCPConfig is the
// host+worktree-path counterpart: the same confidentiality property for
// hostCredentialSeed (worktree.go's provisionConfigHome).
func TestHostCredentialSeed_NeverLeaksPersonalMCPConfig(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude.json"), []byte(realisticDotClaudeJSON), 0o600))
	dest := t.TempDir()

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)

	seededDir := filepath.Join(dest, "claude")
	entries, err := os.ReadDir(seededDir)
	require.NoError(t, err)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(seededDir, e.Name()))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "mcpServers",
			"an isolated agent's seeded config-home must never carry the host user's OWN mcpServers registrations")
		assert.NotContains(t, string(data), "SECRET-SPOTIFY-TOKEN")
		assert.NotContains(t, string(data), "SECRET-GMAIL-TOKEN")
	}
}

// TestFileExists pins this package's copy of the "existing regular file"
// predicate; see internal/adapters/cli's TestFileExists for why all three verbatim
// copies are pinned separately rather than compared to each other.
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "credentials.json")
	require.NoError(t, os.WriteFile(regular, []byte("{}"), 0o600))

	assert.True(t, fileExists(regular), "an existing regular file exists")
	assert.False(t, fileExists(dir), "a DIRECTORY is not a file — a credential mount source must not pass this")
	assert.False(t, fileExists(filepath.Join(dir, "absent.json")), "a missing path does not exist")
}

// TestHostCredentialSeed_TightensAPreExistingDestination pins the owner-only
// guarantee on a destination that ALREADY EXISTED.
//
// It survives the copy path's deletion because the hazard survives it: an
// in-place write sets the mode only on a file it CREATED, so a stale
// destination left at a looser mode by an earlier run would keep that mode
// while holding a live token — the same trap os.WriteFile's create-only perm
// argument laid, reached by a different route.
func TestHostCredentialSeed_TightensAPreExistingDestination(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)

	dest := t.TempDir()
	seedDir := filepath.Join(dest, "claude")
	require.NoError(t, os.MkdirAll(seedDir, 0o700))
	stale := filepath.Join(seedDir, ".credentials.json")
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o644))

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)

	info, err := os.Stat(stale)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the owner-only guarantee must hold on a destination that already existed")
	got, err := os.ReadFile(stale)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(got), "the stale bytes are replaced by the host's")
}

// TestHostCredentialSeed_TightensAPreExistingSeedDir is the other half of the
// pin above: os.MkdirAll(destDir, 0o700) is likewise a no-op on an existing
// directory, so a seed dir that already existed at a looser mode kept it
// while holding the copied credential files.
func TestHostCredentialSeed_TightensAPreExistingSeedDir(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	dest := t.TempDir()

	seedDir := filepath.Join(dest, claudeSeed(t).Subdir)
	require.NoError(t, os.MkdirAll(seedDir, 0o755))

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	require.Equal(t, seedOK, result)

	info, err := os.Stat(seedDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"a pre-existing seed dir must still end up owner-only; it holds live credential bytes")
}

// TestHostCredentialSeed_UnresolvableHostHomeIsSurfaced pins a
// regression. An unresolvable host HOME was folded into seedNoSource with the
// error discarded, so a genuine environment fault reached the user as the
// caller's "no credentials found to authenticate this run — run `<engine> login`"
// — advice that cannot possibly help, for a cause never named.
//
// The RESULT deliberately stays seedNoSource with no hard error: an existing
// pin fixes that this must degrade rather than abort provisioning. What
// changes is that the cause is no longer
// silent, matching provisionCuratedHome's handling of the identical failure.
func TestHostCredentialSeed_UnresolvableHostHomeIsSurfaced(t *testing.T) {
	orig := hostHomeDir
	hostHomeDir = func() (string, error) { return "", assertErr("HOME lookup exploded") }
	t.Cleanup(func() { hostHomeDir = orig })
	t.Setenv("ANTHROPIC_API_KEY", "")

	done := captureStderr(t)
	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), t.TempDir())
	t.Cleanup(func() { _ = provisioned.Close() })
	stderr := done()

	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result, "still a degrade, not an abort")
	assert.Contains(t, stderr, "HOME lookup exploded", "the discarded cause must reach the user")
	assert.Contains(t, stderr, "claude-code credential seed", "…named for the engine whose seed it broke")
}

// TestClaudeCredentialMounts_AbsentCredentialStaysSilent: a host with no
// credential file at all is an ordinary, expected state (the caller's authHint
// already explains it), so it must degrade quietly — ok=false, no warning.
// There is no copy step here (the real file is mounted directly), so there is
// no copy-failure diagnosis to emit either: absence is the only degrade, and it
// is silent.
func TestClaudeCredentialMounts_AbsentCredentialStaysSilent(t *testing.T) {
	withFakeHome(t) // no ~/.claude/.credentials.json written

	done := captureStderr(t)
	_, ok := credentialFileMounts(claudeAuth(t).CredentialFiles, "/home/ctxloom")
	stderr := done()

	assert.False(t, ok)
	assert.Empty(t, stderr, "an absent host credential is an expected state, not a fault to warn about")
}

// --- hostCredentialSeed characterization: every arm, before and after a
// complexity-reduction split. These are not regressions; they exist so a
// change that alters behaviour cannot pass as a pure refactor.

// TestHostCredentialSeed_SeedDirUncreatable: the destination cannot be made at
// all (configHome is a file). That is a hard error, distinct from every
// "nothing to seed" degrade — the caller must not treat it as an absent
// credential.
func TestHostCredentialSeed_SeedDirUncreatable(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)

	notADir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o600))

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), notADir)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential seed dir")
	assert.Equal(t, seedNoSource, result)
}

// TestHostCredentialSeed_UnreadableSourceIsAnError: the required host file is
// present but cannot be read. Nothing was seeded, and unlike an absent file this
// is a fault the caller must see as an error rather than a quiet degrade.
func TestHostCredentialSeed_UnreadableSourceIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads regardless of mode; cannot make the source unreadable")
	}
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, false)
	src := filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.Chmod(src, 0o000))
	t.Cleanup(func() { _ = os.Chmod(src, 0o600) })

	result, provisioned, err := hostCredentialSeed(claude.EngineName, claudeSeed(t), t.TempDir())
	t.Cleanup(func() { _ = provisioned.Close() })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provision claude-code credential material")
	assert.Equal(t, seedNoSource, result)
}

// TestHostCredentialSeed_AllOptionalAndNonePresent covers the defensive arm: a
// spec whose files are ALL optional and none of which exist copies nothing, and
// must report seedNoSource rather than seedOK — "succeeded having delivered
// nothing" is precisely the shape this project's characteristic bug takes.
func TestHostCredentialSeed_AllOptionalAndNonePresent(t *testing.T) {
	withFakeHome(t)
	seed := agent.CredentialSeed{
		Subdir: "phantom",
		Files:  []agent.SeedFile{{HostRelHome: "nope", DestName: "nope"}},
	}

	dest := t.TempDir()
	result, provisioned, err := hostCredentialSeed("phantom", seed, dest)
	t.Cleanup(func() { _ = provisioned.Close() })
	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result, "placing nothing is never seedOK")
	assert.NoFileExists(t, filepath.Join(dest, "phantom", "nope"))
}
