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

// TestCopyCredentialFile_RefusesSymlinkDestination pins that
// copyCredentialFile used to os.WriteFile straight at dst, which FOLLOWS a
// symlink there — a seed then writes through to whatever the link points at.
// Live in this repo's own working copy: `.codex/auth.json` can be a symlink
// to the real `~/.codex/auth.json`, turning a routine credential seed into an
// arbitrary-file overwrite of the user's own OAuth token. Refuse a
// pre-existing symlink destination outright.
func TestCopyCredentialFile_RefusesSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	require.NoError(t, os.WriteFile(src, []byte("seed-content"), 0o600))

	victim := filepath.Join(dir, "victim.json")
	require.NoError(t, os.WriteFile(victim, []byte("do-not-touch"), 0o600))
	dst := filepath.Join(dir, "auth.json")
	require.NoError(t, os.Symlink(victim, dst))

	err := copyCredentialFile(src, dst, nil)
	require.Error(t, err, "must refuse to write through a symlinked destination")
	assert.Contains(t, err.Error(), "symlink")

	victimContent, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	assert.Equal(t, "do-not-touch", string(victimContent), "the symlink target must be untouched")
}

// TestCopyCredentialFile_PlainDestinationStillWorks: the guard must not
// regress the common case — a fresh (non-symlink) destination copies
// verbatim at 0600, exactly as before.
func TestCopyCredentialFile_PlainDestinationStillWorks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	require.NoError(t, os.WriteFile(src, []byte("seed-content"), 0o600))
	dst := filepath.Join(dir, "auth.json")

	require.NoError(t, copyCredentialFile(src, dst, nil))
	content, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "seed-content", string(content))
	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
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
// claudeCredentialMounts' doc): it leaked the host user's own mcpServers
// registrations into every isolated agent, for mere onboarding convenience
// .credentials.json alone doesn't need.
func TestClaudeCredentialMounts_PresentAndAbsent(t *testing.T) {
	home := withFakeHome(t)

	_, ok := claudeCredentialMounts("/root")
	assert.False(t, ok, "no ~/.claude/.credentials.json → cannot credential-mount")

	realCreds := filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(realCreds), 0o755))
	require.NoError(t, os.WriteFile(realCreds,
		[]byte(`{"claudeAiOauth":{"accessToken":"at","refreshToken":"single-use-rotating-rt"}}`), 0o600))

	mounts, ok := claudeCredentialMounts("/root")
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
	mounts, ok := claudeCredentialMounts("/root")
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
	withFakeHome(t)                   // no ~/.claude credentials on disk
	t.Setenv("ANTHROPIC_API_KEY", "") // both trigger vars are unset…
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

// --- Host+worktree credential seeding ----------------------------------

// TestCredentialSeedSpecs_ClaudeCodeRegistered pins the registry entry claude
// needs: keyed by the REGISTERED backend name "claude-code" (not "claude" —
// see enginespec.go's engineContainerSpecFor using the same key), the ANTHROPIC_API_KEY
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
// shape (per-engine-isolation-home plan §6, replacing
// the old "codex/kiro not registered" pin): codex IS now registered — this
// registry is its ONE credential-seed mechanism (backend.go's
// linkUserCodexAuth symlink is deleted) — with copyable sourceFiles and
// HonoursVarForCreds true (CODEX_HOME relocates creds too). kiro IS also
// registered (for its HomeVars — KIRO_HOME/XDG_DATA_HOME — so Env() has one
// place to read them from) but carries NO sourceFiles: its creds live in a
// global sqlite no per-agent HomeVar relocates, so HonoursVarForCreds is
// false and its XDG_DATA_HOME entry is gated instead of seeded (see
// gateHomeVars in worktree.go).
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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
// seed (see credentialSeedSpecs' claude-code sourceFiles doc): it leaked
// the host user's own mcpServers registrations into every isolated agent's
// config-home, for mere onboarding convenience .credentials.json alone
// doesn't need.
func TestHostCredentialSeed_CopiesCredentialFileWhenPresent(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true) // withDotClaude=true: host ALSO has ~/.claude.json — must not be seeded
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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
// CopyAmbient, codex arm — THE one-way ambient copy-in, exercised through the
// in-tree axis's shape (a per-session instance root the worktree axis's
// provisionConfigHome never sees). It superseded the PrepareCodexHome /
// PrepareClaudeHome pair, which were this one function twice.
// =============================================================================

// TestCopyAmbient_Codex_CopiesAuthJson is the PAYLOAD-asserting case: the host's
// ~/.codex/auth.json lands byte-identical, owner-only, at destDir/.codex/auth.json
// — exactly where cellScopedCodexHome(destDir) resolves CODEX_HOME to.
func TestCopyAmbient_Codex_CopiesAuthJson(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("OPENAI_API_KEY", "")
	writeCodexAuth(t, home)
	dest := t.TempDir()

	report, err := CopyAmbient(AmbientRequest{Engine: "codex", InstanceHome: dest, WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.False(t, report.SkippedEnv)
	assert.False(t, report.NoSource)
	assert.Equal(t, 1, report.Copied, "the report must count the bytes that actually moved")

	want, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dest, ".codex", "auth.json"))
	require.NoError(t, err, "seeded auth.json must exist at destDir/.codex/auth.json")
	assert.Equal(t, want, got, "seeded bytes are byte-identical to the host source")

	info, err := os.Stat(filepath.Join(dest, ".codex", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "seeded credential is owner-only")
}

// TestCopyAmbient_Codex_EnvTriggerSkips: OPENAI_API_KEY set → SkippedEnv, nil
// error, no copy attempted (matches hostCredentialSeed's envTrigger precedence).
func TestCopyAmbient_Codex_EnvTriggerSkips(t *testing.T) {
	withFakeHome(t) // no ~/.codex/auth.json on disk
	t.Setenv("OPENAI_API_KEY", "sk-test")
	dest := t.TempDir()

	report, err := CopyAmbient(AmbientRequest{Engine: "codex", InstanceHome: dest, WorkDir: t.TempDir()})
	require.NoError(t, err)
	assert.True(t, report.SkippedEnv)
	assert.False(t, report.NoSource)
	assert.NoFileExists(t, filepath.Join(dest, ".codex", "auth.json"))
}

// TestCopyAmbient_Codex_NoSourceFailsLoud pins the fail-loud contract: no
// OPENAI_API_KEY and no host ~/.codex/auth.json reports NoSource with an
// actionable reason — NEVER a silent success that would let codex launch
// straight into a 401. It is a DECISION rather than a Go error because the two
// axes act on it differently; both callers surface this exact string.
func TestCopyAmbient_Codex_NoSourceFailsLoud(t *testing.T) {
	withFakeHome(t) // empty fake home — no .codex at all
	t.Setenv("OPENAI_API_KEY", "")
	dest := t.TempDir()

	report, err := CopyAmbient(AmbientRequest{Engine: "codex", InstanceHome: dest, WorkDir: t.TempDir()})
	require.NoError(t, err, "nothing seedable is a DECISION, not an I/O error")
	require.True(t, report.NoSource)
	assert.Contains(t, report.NoSourceReason, "OPENAI_API_KEY")
	assert.Contains(t, report.NoSourceReason, "auth.json")
	assert.Contains(t, report.NoSourceReason, "codex login", "the reason must name a fix that works")
}

// TestCopyAmbient_Codex_NoSourceOffersNoDegradedEscape pins the REMOVAL of the
// "(or pass --degraded)" clause this error used to carry. The flag relaxes
// this package's strictness recording; the caller that surfaces this error
// (internal/codex's ensureCodexCredentials → Codex.Setup, whose result
// Execute refuses to launch on) never consults strictness, so --degraded
// changes nothing on this path. An error naming an escape hatch that does
// not exist sends the user round a loop that cannot terminate — a worse
// failure than saying less.
func TestCopyAmbient_Codex_NoSourceOffersNoDegradedEscape(t *testing.T) {
	withFakeHome(t)
	t.Setenv("OPENAI_API_KEY", "")

	report, err := CopyAmbient(AmbientRequest{Engine: "codex", InstanceHome: t.TempDir(), WorkDir: t.TempDir()})
	require.NoError(t, err)
	require.True(t, report.NoSource)
	assert.NotContains(t, report.NoSourceReason, "degraded",
		"--degraded does not unblock this path — internal/codex never reads strictness")
}

// =============================================================================
// CopyAmbient, claude arm — the same one mechanism against claude's allow-list,
// on the IN-TREE AGENT axis where internal/operations points CLAUDE_CONFIG_DIR
// at a per-session instance (internal/claude.SessionConfigDir).
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
// regression test: claudeCredentialMounts must not carry the user's own
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

	mounts, ok := claudeCredentialMounts("/root")
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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

// =============================================================================
// opencode host+worktree credential seeding ---------------------------
//
// Before this registry entry, "opencode" had NO credentialSeedSpec: worktree.go's
// seedCredentials short-circuited at `if !ok { return nil }` with no
// strictness.Fail at all — a SILENT no-op, strictly worse than the loud
// handling every OTHER registered backend gets (an unregistered engine simply
// isn't offered isolation; a registered-but-unseedable one fails loud). This
// closes that gap: XDG_DATA_HOME/XDG_CONFIG_HOME are UNDOCUMENTED opencode
// behaviour (only OPENCODE_CONFIG/OPENCODE_CONFIG_DIR are documented) — pinned
// against opencode 1.18.1 by direct interrogation of its compiled binary
// (`process.env.XDG_DATA_HOME` read directly, joined with "opencode" then
// "auth.json"/"mcp-auth.json") and confirmed live: an isolated `opencode auth
// list` under a fresh scratch HOME + XDG_DATA_HOME pointed at a seeded
// auth.json printed the RELOCATED path and "1 credentials" (OpenRouter),
// where the same command against an unseeded scratch XDG_DATA_HOME printed
// "0 credentials" — the exact experiment the task brief ran. Must be
// re-verified on any opencode upgrade; this is not a vendor-documented
// contract.
// =============================================================================

// writeOpencodeAuth writes a host ~/.local/share/opencode/auth.json (and,
// when withMcpAuth, a sibling mcp-auth.json — confirmed to live in the SAME
// directory by the same join(Path.data, ...) call in opencode's own binary)
// under home.
func writeOpencodeAuth(t *testing.T, home string, withMcpAuth bool) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "opencode")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openrouter":{"type":"api","key":"sk-or-test"}}`), 0o600))
	if withMcpAuth {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp-auth.json"), []byte(`{}`), 0o600))
	}
}

// TestCredentialSeedSpecs_OpencodeRegistered pins the registry entry itself:
// keyed by the registered backend name "opencode" (enginespec.go's
// engineContainerSpecFor uses the same key), OPENROUTER_API_KEY as envTrigger
// (mirroring resolveOpencodeContainerAuth's container-side trigger — the
// same var covers both axes), HonoursVarForCreds TRUE — UNLIKE kiro:
// opencode's XDG_DATA_HOME genuinely relocates its credential file, not a
// global unrelocatable store — and two HomeVars, neither gated (gating only
// applies to a HonoursVarForCreds==false spec).
func TestCredentialSeedSpecs_OpencodeRegistered(t *testing.T) {
	spec, ok := credentialSeedSpecs["opencode"]
	require.True(t, ok, "opencode must have a credentialSeedSpec (sunny-saga)")
	assert.Equal(t, "opencode", spec.engine)
	assert.Equal(t, "OPENROUTER_API_KEY", spec.envTrigger)
	assert.True(t, spec.HonoursVarForCreds, "opencode's XDG_DATA_HOME genuinely relocates auth.json — unlike kiro's partial lever")
	require.NotNil(t, spec.sourceFiles, "opencode has a copyable auth.json to seed")

	require.Len(t, spec.HomeVars, 2)
	byVar := map[string]homeVar{}
	for _, hv := range spec.HomeVars {
		byVar[hv.EnvVar] = hv
	}
	_, hasConfig := byVar["XDG_CONFIG_HOME"]
	_, hasData := byVar["XDG_DATA_HOME"]
	assert.True(t, hasConfig, "XDG_CONFIG_HOME must be one of opencode's HomeVars")
	assert.True(t, hasData, "XDG_DATA_HOME must be one of opencode's HomeVars")
	assert.False(t, byVar["XDG_CONFIG_HOME"].GatedOnCreds, "HonoursVarForCreds==true specs are never gated")
	assert.False(t, byVar["XDG_DATA_HOME"].GatedOnCreds, "HonoursVarForCreds==true specs are never gated")
}

// TestCredentialSeedSpecs_OpencodeSourceFilesIncludeAuthAndOptionalMcpAuth
// pins WHICH files opencode's spec copies: auth.json REQUIRED (the file
// `opencode auth login` writes, and the one this task's live `opencode auth
// list` proof read back), and mcp-auth.json OPTIONAL — confirmed present in
// opencode 1.18.1's own binary at the SAME Path.data-joined location as
// auth.json (MCP server OAuth tokens), but not every user configures an
// OAuth MCP server, so its absence must never block seeding.
func TestCredentialSeedSpecs_OpencodeSourceFilesIncludeAuthAndOptionalMcpAuth(t *testing.T) {
	spec := credentialSeedSpecs["opencode"]
	files := spec.sourceFiles("/home/u")
	require.Len(t, files, 2)
	byName := map[string]seedFile{}
	for _, f := range files {
		byName[f.destName] = f
	}
	auth, ok := byName["auth.json"]
	require.True(t, ok)
	assert.True(t, auth.required, "auth.json is the credential itself — required")
	assert.Equal(t, filepath.Join("/home/u", ".local", "share", "opencode", "auth.json"), auth.host)

	mcpAuth, ok := byName["mcp-auth.json"]
	require.True(t, ok)
	assert.False(t, mcpAuth.required, "mcp-auth.json is optional — not every user has OAuth MCP servers configured")
	assert.Equal(t, filepath.Join("/home/u", ".local", "share", "opencode", "mcp-auth.json"), mcpAuth.host)
}

// TestHostCredentialSeed_OpencodeSeedsAuthJsonUnderXdgDataOpencode is the
// PAYLOAD-asserting regression test: the seeded auth.json
// must land NOT at <configHome>/xdg-data/auth.json but at
// <configHome>/xdg-data/opencode/auth.json — the exact path opencode itself
// resolves ($XDG_DATA_HOME + "/opencode/auth.json"). If the destSubdir
// nesting were wrong, Env()'s XDG_DATA_HOME and opencode's own resolution
// would disagree, and an isolated agent would silently see no credentials —
// exactly the failure mode a bare exit-code check would miss.
func TestHostCredentialSeed_OpencodeSeedsAuthJsonUnderXdgDataOpencode(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	writeOpencodeAuth(t, home, true)
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["opencode"], dest, nil)
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)

	xdgDataHome := filepath.Join(dest, "xdg-data") // the HomeVar's own Subdir
	wantAuth, err := os.ReadFile(filepath.Join(home, ".local", "share", "opencode", "auth.json"))
	require.NoError(t, err)
	gotAuth, err := os.ReadFile(filepath.Join(xdgDataHome, "opencode", "auth.json"))
	require.NoError(t, err, "auth.json must exist at $XDG_DATA_HOME/opencode/auth.json — opencode's OWN resolution path")
	assert.Equal(t, wantAuth, gotAuth, "seeded bytes must be byte-identical to the host source")

	info, err := os.Stat(filepath.Join(xdgDataHome, "opencode", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "seeded credential is owner-only")

	wantMcp, err := os.ReadFile(filepath.Join(home, ".local", "share", "opencode", "mcp-auth.json"))
	require.NoError(t, err)
	gotMcp, err := os.ReadFile(filepath.Join(xdgDataHome, "opencode", "mcp-auth.json"))
	require.NoError(t, err, "mcp-auth.json rides along at the same directory as auth.json")
	assert.Equal(t, wantMcp, gotMcp)
}

// TestHostCredentialSeed_OpencodeMcpAuthOptionalAbsence: no mcp-auth.json on
// the host (the common case — most users have not configured an OAuth MCP
// server) still seeds OK off auth.json alone.
func TestHostCredentialSeed_OpencodeMcpAuthOptionalAbsence(t *testing.T) {
	home := withFakeHome(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	writeOpencodeAuth(t, home, false) // no mcp-auth.json
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["opencode"], dest, nil)
	require.NoError(t, err)
	assert.Equal(t, seedOK, result)
	assert.FileExists(t, filepath.Join(dest, "xdg-data", "opencode", "auth.json"))
	assert.NoFileExists(t, filepath.Join(dest, "xdg-data", "opencode", "mcp-auth.json"))
}

// TestHostCredentialSeed_OpencodeSkipsWhenOpenrouterKeySet: OPENROUTER_API_KEY
// present → seeding is skipped entirely, mirroring
// resolveOpencodeContainerAuth's authEnv precedence — even with no host
// auth.json at all, this is NOT the fail-loud case, and no seed dir is created.
func TestHostCredentialSeed_OpencodeSkipsWhenOpenrouterKeySet(t *testing.T) {
	withFakeHome(t) // no opencode auth on disk
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["opencode"], dest, nil)
	require.NoError(t, err)
	assert.Equal(t, seedSkippedEnv, result)
	assert.NoDirExists(t, filepath.Join(dest, "xdg-data"))
}

// TestHostCredentialSeed_OpencodeNoSourceReturnsNoSourceNotError: no
// OPENROUTER_API_KEY and no host auth.json → seedNoSource — the fail-loud case
// the CALLER (worktree.go's seedCredentials) turns into a strictness.Fail. This
// is the exact silent no-op this spec exists to close: before it was
// registered, an unregistered "opencode" backend made seedCredentials
// short-circuit with no finding recorded at all.
func TestHostCredentialSeed_OpencodeNoSourceReturnsNoSourceNotError(t *testing.T) {
	withFakeHome(t) // empty fake home — no opencode auth at all
	t.Setenv("OPENROUTER_API_KEY", "")
	dest := t.TempDir()

	result, err := hostCredentialSeed(credentialSeedSpecs["opencode"], dest, nil)
	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result)
}

// TestFileExists pins this package's copy of the "existing regular file"
// predicate; see internal/cli's TestFileExists for why all three verbatim
// copies are pinned separately rather than compared to each other.
func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "credentials.json")
	require.NoError(t, os.WriteFile(regular, []byte("{}"), 0o600))

	assert.True(t, fileExists(regular), "an existing regular file exists")
	assert.False(t, fileExists(dir), "a DIRECTORY is not a file — a credential mount source must not pass this")
	assert.False(t, fileExists(filepath.Join(dir, "absent.json")), "a missing path does not exist")
}

// TestCopyCredentialFile_TightensAPreExistingDestination pins a
// regression. copyCredentialFile documents "copies src to dst at 0600
// (owner-only)", but os.WriteFile applies its perm argument ONLY when it creates
// the file — writing over a destination that already exists keeps whatever mode
// that destination had. The seed then lands live credential bytes in a
// group/world-readable file while every doc and every caller believes the
// owner-only guarantee holds.
func TestCopyCredentialFile_TightensAPreExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(src, []byte(`{"token":"secret"}`), 0o600))
	require.NoError(t, os.WriteFile(dst, []byte("stale"), 0o644))

	require.NoError(t, copyCredentialFile(src, dst, nil))

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the owner-only guarantee must hold on a destination that already existed")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, `{"token":"secret"}`, string(got))
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

	seedDir := filepath.Join(dest, credentialSeedSpecs["claude-code"].destSubdir)
	require.NoError(t, os.MkdirAll(seedDir, 0o755))

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], dest, nil)
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
// caller's "no credentials found to authenticate this run — run `codex login`"
// — advice that cannot possibly help, for a cause never named.
//
// The RESULT deliberately stays seedNoSource with no hard error: an existing pin
// (TestHostCredentialSeed_UnresolvableHostHome) fixes that this must degrade
// rather than abort provisioning. What changes is that the cause is no longer
// silent, matching provisionCuratedHome's handling of the identical failure.
func TestHostCredentialSeed_UnresolvableHostHomeIsSurfaced(t *testing.T) {
	orig := hostHomeDir
	hostHomeDir = func() (string, error) { return "", assertErr("HOME lookup exploded") }
	t.Cleanup(func() { hostHomeDir = orig })
	t.Setenv("ANTHROPIC_API_KEY", "")

	done := captureStderr(t)
	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], t.TempDir(), nil)
	stderr := done()

	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result, "still a degrade, not an abort")
	assert.Contains(t, stderr, "HOME lookup exploded", "the discarded cause must reach the user")
	assert.Contains(t, stderr, "claude credential seed", "…named for the engine whose seed it broke")
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
	_, ok := claudeCredentialMounts("/home/ctxloom")
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], notADir, nil)
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

	result, err := hostCredentialSeed(credentialSeedSpecs["claude-code"], t.TempDir(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "seed claude credential")
	assert.Equal(t, seedNoSource, result)
}

// TestHostCredentialSeed_AllOptionalAndNonePresent covers the defensive arm: a
// spec whose files are ALL optional and none of which exist copies nothing, and
// must report seedNoSource rather than seedOK — "succeeded having delivered
// nothing" is precisely the shape this project's characteristic bug takes.
func TestHostCredentialSeed_AllOptionalAndNonePresent(t *testing.T) {
	withFakeHome(t)
	spec := credentialSeedSpec{
		engine:      "phantom",
		destSubdir:  "phantom",
		sourceFiles: func(h string) []seedFile { return []seedFile{{host: filepath.Join(h, "nope"), destName: "nope"}} },
	}

	dest := t.TempDir()
	result, err := hostCredentialSeed(spec, dest, nil)
	require.NoError(t, err)
	assert.Equal(t, seedNoSource, result, "copying nothing is never seedOK")
	assert.NoFileExists(t, filepath.Join(dest, "phantom", "nope"))
}
