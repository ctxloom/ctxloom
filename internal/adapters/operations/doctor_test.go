package operations

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeFakeExecutable creates an executable regular file named name inside
// dir, so exec.LookPath(name) succeeds when PATH is pointed at dir. Content
// doesn't matter — doctorCheckDeps only probes presence, never runs it.
func writeFakeExecutable(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0755))
}

// setupProject scaffolds a real, hermetic (no network) .ctxloom project via
// InitializeProject — the same call `ctxloom manage install`
// makes — under a fresh temp dir, and loads it back with the config read. The
// scaffolded agent ("default") binds one profile ("default", the embedded
// seed profile InitializeProject writes) to the given engine label.
func setupProject(t *testing.T, engine string) (root string, cfg *config.Config) {
	t.Helper()
	// Real-OS-fs the config read below (no injected fs): isolate HOME so the
	// home-layer read (D2/D3 layering) never reaches this developer's real
	// ~/.ctxloom — this scaffolded project is meant to be the only source.
	testsupport.Isolate(t)
	root = t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	_, err := InitializeProject(context.Background(), engines.Registry(), InitializeProjectRequest{
		AppDir: appDir, Engine: engine,
	})
	require.NoError(t, err)
	cfg, err = configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	return root, cfg
}

// applyHooksHermetically runs ApplyHooks the same way every real
// caller does (manage.go, init.go all pass RegenerateContext: true) so the
// always-available, network-free SessionStart context-injection hook lands —
// unlike the bundle-shipped hooks a profile's remote parent would otherwise
// supply, this one needs no `ctxloom deps pull`/cache, so it's reachable on
// a bare host. It also pins the injected hook's exec token to "ctxloom" via
// selfexec.SetPathForTesting (restored on cleanup): left at its `go test`
// default, the hook would name the test binary itself, and
// exectoken.IsManaged(command, "ctxloom") — keyed on that exact exec-token
// identity — would report it as foreign, not ctxloom-managed. Both are
// needed for doctorCheckHooksTrust to observe "ok" hermetically, on any
// host, matching the SAME hooks a fully-wired project always carries
// regardless of what's cached under its real $HOME.
func applyHooksHermetically(t *testing.T, cfg *config.Config, root, backend string) {
	t.Helper()
	t.Cleanup(selfexec.SetPathForTesting("ctxloom"))
	_, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Cfg: cfg, Backend: backend, WorkDir: root, RegenerateContext: true,
	})
	require.NoError(t, err)
}

// --- DOCTOR-CHECK-SETUP-MARKER-e5 ---

func TestDoctorCheckSetupMarker_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupMarker(cfg, nil)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, cfg.GetAppPaths()[0])
}

func TestDoctorCheckSetupMarker_WrongState_NoMarkerDir(t *testing.T) {
	check := doctorCheckSetupMarker(&config.Config{}, nil)
	assert.Equal(t, DoctorWarn, check.Status, "an empty AppPaths must fail loud, not silently pass")
	assert.Contains(t, check.Detail, "no .ctxloom marker directory found")
}

func TestDoctorCheckSetupMarker_WrongState_ConfigLoadError(t *testing.T) {
	check := doctorCheckSetupMarker(nil, assert.AnError)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "config did not load")
}

// --- DOCTOR-CHECK-DEPS-a1: git added to the dep probe ---

func TestDoctorCheckDeps_RightState_GitPresentIsEnumeratedInOK(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "ssh-keygen", "git", "docker"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir)
	check := doctorCheckDeps(engines.Registry(), &config.Config{})
	// docker/podman availability (isolation.Docker{}.Available()) does more
	// than a PATH lookup, so this may still warn about the container runtime
	// on some hosts; what this test pins down is that git is bucketed with
	// the OTHER always-checked deps, never silently skipped.
	if check.Status == DoctorOK {
		assert.Contains(t, check.Detail, "git")
	} else {
		assert.NotContains(t, check.Detail, "git", "git IS on PATH here, so it must not appear in a missing list")
	}
}

func TestDoctorCheckDeps_WrongState_GitMissing(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "ssh-keygen"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir) // deliberately no git on this PATH
	check := doctorCheckDeps(engines.Registry(), &config.Config{})
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "git", "a missing git must be named, not silently absorbed into a generic failure")
	assert.Contains(t, check.Detail, "required", "a missing git must be reported in the REQUIRED bucket, not lumped with recommended")
}

func TestDoctorDepBinariesRequired_IncludesGit(t *testing.T) {
	assert.Contains(t, doctorDepBinariesRequired, "git", "worktree isolation and deps pull hard-depend on git")
}

// TestDoctorCheckDeps_WrongState_SSHKeygenMissing_IsRecommendedNotRequired
// pins DEPS-a1's TRUTHFULNESS fix: an audit found ssh-keygen is NEVER exec'd
// by ctxloom (signing is pure Go over the ssh-agent protocol —
// internal/adapters/signing/sign.go, internal/adapters/signing/agentkey/agentkey.go), so a
// missing ssh-keygen (with git/engine/runtime all present) must be reported
// as RECOMMENDED, not implied to be required for signing, and must NOT use
// the word "signing" to explain why it's missing.
func TestDoctorCheckDeps_WrongState_SSHKeygenMissing_IsRecommendedNotRequired(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "git", "docker"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir) // deliberately no ssh-keygen
	check := doctorCheckDeps(engines.Registry(), &config.Config{})
	if check.Status == DoctorOK {
		// A host without a real docker/podman daemon can still warn on the
		// container runtime alone; skip only if ssh-keygen genuinely wasn't
		// flagged at all, which would itself be the bug this test guards.
		t.Skip("container runtime unexpectedly available in this ok path; ssh-keygen-missing behavior is exercised by the warn branch below on hosts without docker/podman")
	}
	assert.Contains(t, check.Detail, "ssh-keygen", "a missing ssh-keygen must still be named")
	assert.Contains(t, check.Detail, "recommended", "must be labeled recommended, not implied required")
	assert.NotContains(t, check.Detail, "for signing", "must not claim signing needs ssh-keygen — it's pure Go and never execs it")
}

// TestDoctorCheckDeps_RightState_AllPresent_DoesNotClaimSigningNeedsThem
// proves the OK Detail text — reached when ssh/ssh-keygen/git/engine/runtime
// are ALL present — never claims ssh/ssh-keygen are needed "for signing"
// either; the truthful framing must hold on both the ok and warn paths.
func TestDoctorCheckDeps_RightState_AllPresent_DoesNotClaimSigningNeedsThem(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "ssh-keygen", "git", "docker"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir)
	check := doctorCheckDeps(engines.Registry(), &config.Config{})
	if check.Status != DoctorOK {
		t.Skip("container runtime unexpectedly unavailable on this host; the all-present ok Detail wording is exercised only on the ok path")
	}
	assert.NotContains(t, check.Detail, "for signing", "must not claim signing needs ssh/ssh-keygen — it's pure Go and never execs either")
}

// --- DOCTOR-CHECK-SIGNKEY-k1: reuses agentkey.Discoverer, the SAME
// resolver `ctxloom sign` itself uses (internal/adapters/signing/agentkey), via an
// in-memory ssh-agent keyring (agent.NewKeyring — no socket, no real
// SSH_AUTH_SOCK, no host ssh-agent state leaks in) mirroring sign_test.go's
// discovererWithSoleAgentIdentity pattern.

// signKeyDiscoverer wires an agentkey.Discoverer to an in-memory ssh-agent
// keyring holding exactly the given comments (0, 1, or many identities) and
// no git config value, so doctorCheckSignKey is exercisable without a real
// ssh-agent or git binary.
func signKeyDiscoverer(t *testing.T, comments ...string) (*agentkey.Discoverer, []ssh.Signer) {
	t.Helper()
	kr := agent.NewKeyring()
	for _, comment := range comments {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv, Comment: comment}))
	}
	signers, err := kr.Signers()
	require.NoError(t, err)
	return &agentkey.Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return kr, nil },
		ReadFile:  func(path string) ([]byte, error) { return nil, assert.AnError },
	}, signers
}

func TestDoctorCheckSignKey_RightState_SoleIdentityResolves(t *testing.T) {
	disc, signers := signKeyDiscoverer(t, "ben@abbitt.me")
	check := doctorCheckSignKey(context.Background(), &config.Config{}, disc)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "ssh-agent (sole identity)", "must name the SAME Source agentkey.Discovered reports")
	assert.Contains(t, check.Detail, ssh.FingerprintSHA256(signers[0].PublicKey()), "must name the resolved key's fingerprint")
}

func TestDoctorCheckSignKey_WrongState_NothingResolvable(t *testing.T) {
	disc, _ := signKeyDiscoverer(t) // empty agent, no git config, no explicit key
	check := doctorCheckSignKey(context.Background(), &config.Config{}, disc)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "no signing key resolves")
	assert.Contains(t, check.Detail, "ctxloom review", "must lead with approve — a missing key blocks ordinary review, not just publishing")
	assert.Contains(t, check.Detail, "ctxloom bundle sign", "must also name the publishing feature this gap affects")
	assert.Contains(t, check.Detail, "ssh-add", "must give an actionable fix")
}

// TestDoctorCheckSignKey_WrongState_Ambiguous observes agentkey's REAL
// multi-identity behavior directly: with no git config user.signingkey and
// no explicit sign.key, ssh-agent holding MORE than one identity resolves to
// agentkey.AmbiguousKeyError (agentkey.go resolveSoleAgentIdentity) — it
// never silently picks one. The warn message must reflect that specific
// situation, not the generic "no key" wording.
func TestDoctorCheckSignKey_WrongState_Ambiguous(t *testing.T) {
	disc, _ := signKeyDiscoverer(t, "one@example.com", "two@example.com")
	check := doctorCheckSignKey(context.Background(), &config.Config{}, disc)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "ambiguous", "must name the specific ambiguous-choice situation, not generic absence")
	assert.Contains(t, check.Detail, "one@example.com")
	assert.Contains(t, check.Detail, "two@example.com")
}

// TestDoctorCheckSignKey_ConfiguredSignKeyDisambiguates proves the check
// honors cfg.SignKey() (sign.key config) exactly like runSign does (sign.go:
// "explicit := keyFlag; if explicit == "" ... explicit = cfg.SignKey()"): an
// agent holding multiple identities resolves cleanly once sign.key names one
// by comment.
func TestDoctorCheckSignKey_ConfiguredSignKeyDisambiguates(t *testing.T) {
	disc, signers := signKeyDiscoverer(t, "other@example.com", "ben@abbitt.me")
	cfg := config.NewFixture(config.Fixture{Settings: config.SettingsConfig{Sign: &config.SignConfig{Key: "ben@abbitt.me"}}})
	check := doctorCheckSignKey(context.Background(), cfg, disc)
	assert.Equal(t, DoctorOK, check.Status)
	// The comment-matched signer is the second one added.
	assert.Contains(t, check.Detail, ssh.FingerprintSHA256(signers[1].PublicKey()))
}

// --- DOCTOR-CHECK-GITIDENT-l2: reuses agentkey's git-config plumbing (the
// one existing generic `git config --get <key>` reader in this codebase,
// already used to resolve user.signingkey) rather than shelling out a
// second, bespoke way. A fake gitConfigFunc closure isolates every test from
// the host's real git config (no ~/.gitconfig read, no real git binary
// call), same discipline as signKeyDiscoverer above.

// fakeGitConfig returns a gitConfigFunc backed by an in-memory map — set
// values resolve, everything else is "unset" ("", false, nil), exactly
// execGitConfig's contract for a key `git config --get` doesn't find.
func fakeGitConfig(values map[string]string) gitConfigFunc {
	return func(ctx context.Context, dir, key string) (string, bool, error) {
		v, ok := values[key]
		return v, ok, nil
	}
}

func TestDoctorCheckGitIdentity_RightState_BothSet(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.name": "Ben", "user.email": "ben@abbitt.me"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "Ben <ben@abbitt.me>", "must name the resolved identity")
}

func TestDoctorCheckGitIdentity_WrongState_NameUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.email": "ben@abbitt.me"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.name", "must name which field is missing")
	assert.NotContains(t, check.Detail, "git config --global user.email", "must not falsely also offer an email fix")
	assert.Contains(t, check.Detail, "git config --global user.name", "must give the actionable fix")
}

func TestDoctorCheckGitIdentity_WrongState_EmailUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.name": "Ben"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.email", "must name which field is missing")
	assert.Contains(t, check.Detail, "git config --global user.email", "must give the actionable fix")
}

func TestDoctorCheckGitIdentity_WrongState_BothUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.name")
	assert.Contains(t, check.Detail, "user.email")
}

// TestDoctorCheckGitIdentity_WrongState_BlankValueTreatedAsUnset guards
// against a git config value that's present but empty/whitespace-only (e.g.
// `git config user.name ""`) being mistaken for a real identity.
func TestDoctorCheckGitIdentity_WrongState_BlankValueTreatedAsUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.name": "  ", "user.email": "ben@abbitt.me"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.name")
}

// --- DOCTOR-CHECK-AGENTS-b2: promoted to WARN on an empty roster ---

func TestDoctorCheckAgents_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckAgents(context.Background(), engines.Registry(), cfg, nil)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "default")
}

func TestDoctorCheckAgents_WrongState_EmptyRoster(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{}
	cfg = config.NewFixture(f)

	check := doctorCheckAgents(context.Background(), engines.Registry(), cfg, nil)
	assert.Equal(t, DoctorWarn, check.Status, "an empty roster is an incomplete setup postcondition, not a neutral fact")
	assert.Contains(t, check.Detail, "no agents configured")
}

func TestDoctorCheckAgents_WrongState_UnresolvableProfile(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{
		"broken": {Name: "broken", LLM: "claude-code", Profiles: []string{"does-not-exist"}},
	}
	cfg = config.NewFixture(f)
	check := doctorCheckAgents(context.Background(), engines.Registry(), cfg, nil)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "broken")
}

// --- DOCTOR-CHECK-SETUP-DEPS-h8 (lockfile + context assembly) ---

func TestDoctorCheckSetupLockAndAssembly_RightState(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	stubLocalDefaultProfile(t, root)
	var err error
	cfg, err = configload.Load(configload.WithAppDir(cfg.GetAppPaths()[0]))
	require.NoError(t, err)

	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "lockfile: 0 entries parse cleanly")
	assert.Contains(t, check.Detail, "context assembly: succeeds")
}

// stubLocalDefaultProfile overwrites the scaffolded seed profile
// (.ctxloom/profiles/default.yaml) with a self-contained profile carrying no
// remote parent. The embedded seed profile (resources/profiles/default.yaml)
// inherits https://github.com/ctxloom/ctxloom-default, which a hermetic test
// never installs (no `ctxloom deps pull` ever runs here) — a fixture meant to
// represent a genuinely fully-wired project must not carry a dependency this
// suite can never satisfy, or DOCTOR-CHECK-SETUP-DEPS-h8 correctly reports
// the unresolved parent as a skipped ref.
func stubLocalDefaultProfile(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".ctxloom", "profiles", "default.yaml")
	content := "version: \"1.0.0\"\ndescription: \"self-contained test profile, no remote dependency\"\ntags:\n  - default\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func TestDoctorCheckSetupLockAndAssembly_WrongState_CorruptLockfile(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	lockPath := filepath.Join(cfg.GetAppPaths()[0], "lock.yaml")
	require.NoError(t, os.WriteFile(lockPath, []byte("not: [valid: yaml: at: all"), 0644))

	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "lockfile:")
}

// TestDoctorCheckSetupLockAndAssembly_WrongState_SkippedProfileRefs is the
// freehand-fabric regression: assembling context for a configured default
// agent whose profiles cannot be resolved is fault-tolerant by design — it
// skips each broken profile and still returns a nil error — so a check that
// only looks at AssembleContext's error return reports "[ok] ... succeeds"
// for a session that is actually running on an almost-empty context. The
// check must instead read what assembly already recorded during that call
// and refuse to say ok.
func TestDoctorCheckSetupLockAndAssembly_WrongState_SkippedProfileRefs(t *testing.T) {
	resetStrictness(t)
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.DefaultAgent = "default"
	f.Agents = map[string]agents.Agent{
		"default": {Profiles: []string{"doctor-missing-profile-one", "doctor-missing-profile-two"}},
	}
	cfg = gatedFixture(f)

	// Guard: prove this exact fixture makes assembly skip TWO refs, by
	// capturing the real stderr warning lines a raw AssembleContext call
	// against it produces — independent of the check's own counting logic
	// (which reads strictness findings, not stderr text), so a bug in that
	// counting cannot also hide in this guard.
	var stderrBuf bytes.Buffer
	restoreSink := clidiag.SetSink(&stderrBuf)
	_, assembleErr := AssembleContext(context.Background(), cfg, AssembleContextRequest{})
	restoreSink()
	require.NoError(t, assembleErr, "the defaults path degrades rather than erroring")
	skipLines := strings.Count(stderrBuf.String(), "skipping default profile")
	require.Equal(t, 2, skipLines,
		"fixture must make BOTH configured default profiles fail to resolve, or this test proves nothing")

	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, DoctorWarn, check.Status,
		"an assembly that skipped every configured default profile ref must never report ok")
	assert.Contains(t, check.Detail, "2 ref(s)",
		"the count must name the exact number of skipped refs, not just that some were skipped")
}

// --- DOCTOR-CHECK-HOOKS-TRUST-d4: hooks AND MCP registration per backend ---

func TestDoctorCheckHooksTrust_RightState(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	applyHooksHermetically(t, cfg, root, "claude-code")
	t.Chdir(root) // HarnessStatus's default WorkDir path resolves off cwd

	check := doctorCheckHooksTrust(context.Background(), engines.Registry(), cfg, nil)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "also registered in the project (the explicit `manage hooks install` door) for: claude-code")
}

// TestDoctorCheckHooksTrust_SessionDelivery_IsTheHealthyDefault: absent
// project-side hooks are the correct state of every project — a session
// carries its own — so the check reports that posture as ok, never as a
// fault (ruled 2026-09-21).
func TestDoctorCheckHooksTrust_SessionDelivery_IsTheHealthyDefault(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	t.Chdir(root) // no ApplyHooks call: nothing project-side

	check := doctorCheckHooksTrust(context.Background(), engines.Registry(), cfg, nil)
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "delivered per session")
	assert.Contains(t, check.Detail, "claude-code")
	assert.NotContains(t, check.Detail, "NOT")
}

func TestDoctorCheckHooksTrust_NoEnginesConfigured(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{}
	cfg = config.NewFixture(f)
	check := doctorCheckHooksTrust(context.Background(), engines.Registry(), cfg, nil)
	assert.Equal(t, DoctorOK, check.Status, "nothing configured to check hooks for is not itself a failure")
	assert.Contains(t, check.Detail, "no engine is configured to check")
}

// --- DOCTOR-CHECK-SETUP-COMPANIONS-i9 / AUTHPING-j0: reporting-only ---

func TestDoctorCheckSetupCompanions_NeverWarns(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupCompanions(cfg, nil, false)
	assert.NotEqual(t, DoctorWarn, check.Status, "companions are optional add-ons, never a doctor failure")
}

// TestDoctorCheckSetupCompanions_TellsNotRunApartFromNotInstalled is the
// report a user acts on: "found on PATH but never allowed to run" and "not
// installed" send them to different remedies, and the check reads BOTH off the
// one resolved catalog rather than discovering companions a second time.
func TestDoctorCheckSetupCompanions_TellsNotRunApartFromNotInstalled(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{
			Loadouts: []bundles.CompanionLoadout{
				{Bin: "ltk", Path: "/opt/bin/ltk", Document: []byte("run:\n  version: \"1.0\"\n")},
			},
			Candidates: []bundles.CompanionCandidate{
				{Bin: "taskloom", Path: "/opt/bin/taskloom", Reason: bundles.CandidateUnconsented},
				{Bin: "reprise", Reason: bundles.CandidateAbsent},
				{Bin: "wedged", Path: "/opt/bin/wedged", Reason: bundles.CandidateProbeFailed},
			},
		}, nil
	})

	check := doctorCheckSetupCompanions(cfg, nil, false)

	assert.NotEqual(t, DoctorWarn, check.Status, "companions are optional add-ons, never a doctor failure")
	assert.Contains(t, check.Detail, "loadouts read: ltk")
	assert.Contains(t, check.Detail, "NOT RUN: taskloom (/opt/bin/taskloom)")
	assert.Contains(t, check.Detail, "ctxloom companion trust",
		"a refusal a user cannot act on is a dead end")
	assert.Contains(t, check.Detail, "not installed: reprise")
	assert.Contains(t, check.Detail, "probe failed: wedged (/opt/bin/wedged)")
	assert.NotContains(t, check.Detail, "not installed: taskloom",
		"a companion that is present and refused must never be reported as missing")
}

func TestDoctorCheckSetupAuthPing_AlwaysInfoAndNamesTheGap(t *testing.T) {
	check := doctorCheckSetupAuthPing()
	assert.Equal(t, DoctorInfo, check.Status)
	assert.Contains(t, check.Detail, "no auth-ping surface")
}

// --- DOCTOR-CHECK-LOCAL-STATE-p6 ---

// TestDoctorCheckLocalTierState_RightState_AllPresent proves a checkout that
// actually carries every paths.TierLocal path (internal/core/paths.Layout) reports
// clean — this is the "already used this project for a while" state, not a
// fresh init's (see the WrongState test below for that one).
func TestDoctorCheckLocalTierState_RightState_AllPresent(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)

	check := doctorCheckLocalTierState(cfg, isolatedHome(t))
	assert.Equal(t, DoctorOK, check.Status)
	assert.Contains(t, check.Detail, "every local-only state path is present")
}

// TestDoctorCheckLocalTierState_WrongState_FreshInitMissesEvery proves the
// case this check exists for: a project immediately after `ctxloom init`
// (setupProject's own shape) has NONE of the PresenceMustExist local-only
// state yet, and the report names every one of them plus its Lost text — the
// thing a fresh clone has no way to learn today, per the config-layer-scope
// design doc. PresenceIfUsed rows (the RootHome stores) are asserted absent
// from the report by the companion test below — a fresh project says
// nothing about them, which is not the same claim as "they are present".
func TestDoctorCheckLocalTierState_WrongState_FreshInitMissesEvery(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")

	check := doctorCheckLocalTierState(cfg, isolatedHome(t))
	assert.Equal(t, DoctorWarn, check.Status)
	for _, entry := range paths.Layout() {
		if entry.Tier != paths.TierLocal || entry.Presence != paths.PresenceMustExist {
			continue
		}
		assert.Contains(t, check.Detail, entry.Rel, "every absent must-exist TierLocal path must be named")
		assert.Contains(t, check.Detail, entry.Lost, "and its Lost text must ride along")
	}
}

// TestDoctorCheckLocalTierState_FreshHome_HomeRowsNeverWarn is the C13
// design-note mutation-kill target: "a home store that legitimately doesn't
// exist yet (fresh install) must NOT warn as broken." setupProject's own
// testsupport.Isolate call gives this test a fresh, empty HOME, so every
// PresenceIfUsed (RootHome) row is absent — and none of them may be named in
// the report, unlike the PresenceMustExist project rows this same fresh
// state DOES (correctly) warn about.
//
// The assertion checks the "Rel (Lost)" PAIR, not bare Rel: a RootHome row
// and its RootProject sibling can legitimately share Rel text (".ctxloom/
// sessions" names both the project's distilled-history row and the home
// store row), so a bare-Rel check would false-fail on the unrelated project
// row's own, correctly-reported, absence.
func TestDoctorCheckLocalTierState_FreshHome_HomeRowsNeverWarn(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")

	check := doctorCheckLocalTierState(cfg, isolatedHome(t))
	assert.Equal(t, DoctorWarn, check.Status, "the pre-existing PresenceMustExist project rows still warn")
	for _, entry := range paths.Layout() {
		if entry.Presence != paths.PresenceIfUsed {
			continue
		}
		named := fmt.Sprintf("%s (%s)", entry.Rel, entry.Lost)
		assert.NotContains(t, check.Detail, named,
			"a PresenceIfUsed row absent on a fresh install/machine must never be reported as missing")
	}
}

// TestDoctorCheckLocalTierState_HomeRowPresent_IsReported proves the other
// half of C13's goal — "doctor can finally see them": with a fake HOME that
// actually has a home-rooted store on disk (here, the sessions dir any real
// session anywhere would have created), doctor reports it BY NAME, even
// though the project side of this same fresh project still has missing
// must-exist rows and the overall status is still DoctorWarn.
func TestDoctorCheckLocalTierState_HomeRowPresent_IsReported(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	sessionsRel := filepath.Join(paths.AppDirName, paths.SessionsDir)
	require.NoError(t, os.MkdirAll(filepath.Join(home, sessionsRel), 0o755))

	check := doctorCheckLocalTierState(cfg, isolatedHome(t))
	assert.Equal(t, DoctorWarn, check.Status, "the project rows are still missing")
	assert.Contains(t, check.Detail, "home-rooted store(s) in use")
	assert.Contains(t, check.Detail, sessionsRel)
}

// TestDoctorCheckLocalTierState_WrongState_NoMarkerDir mirrors
// doctorCheckSetupMarker's own "no .ctxloom at all" guard: an empty AppPaths
// must fail loud, not silently report "ok" for a directory that doesn't
// exist to check.
func TestDoctorCheckLocalTierState_WrongState_NoMarkerDir(t *testing.T) {
	check := doctorCheckLocalTierState(&config.Config{}, isolatedHome(t))
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "no .ctxloom marker directory found")
}

// TestDoctorCheckLocalTierState_PartialState_NamesOnlyWhatsMissing proves the
// report is precise, not all-or-nothing: scaffolding every TierLocal path
// EXCEPT one must name exactly that one, and no other.
func TestDoctorCheckLocalTierState_PartialState_NamesOnlyWhatsMissing(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	// The skipped entry must be a LEAF of the layout: skipping a path other
	// entries nest inside would make them absent too, and the report would name
	// more than one. It must also be PresenceMustExist -- a PresenceIfUsed
	// skip would never appear as "missing" at all (see the FreshHome test),
	// which would make this test's "names only what's missing" claim vacuous.
	skipRel := filepath.Join(paths.AppDirName, paths.ProjectIDFileName)
	materializeLayoutEntries(t, root, map[string]bool{skipRel: true})

	check := doctorCheckLocalTierState(cfg, isolatedHome(t))
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "1 local-only path(s) absent")
	assert.Contains(t, check.Detail, skipRel)
}

// --- full command wiring: JSON shape, back-compat "always exits 0", read-only ---

// scaffoldLocalTierState creates a stand-in for every paths.TierLocal path
// (internal/core/paths.Layout) — the local-only state a FRESH init/machine never
// has (it's exactly what accrues from actually using a project AND this
// machine: running sessions, using taskloom, reviewing an update, giving a
// countersignature, trusting a signer, running a coordinator). RootProject
// entries land under root's .ctxloom; RootHome entries land under the
// isolated HOME testsupport.Isolate already set for this test (setupProject
// calls it). Only DOCTOR-CHECK-LOCAL-STATE-p6 reads these paths at all
// (existence only, not content), so an empty placeholder file/dir at each is
// enough to represent "a fully-wired, actually-used project and machine" for
// that check.
func scaffoldLocalTierState(t *testing.T, root string) {
	t.Helper()
	materializeLayoutEntries(t, root, nil)
}

// materializeLayoutEntries scaffolds every paths.Layout() TierLocal entry
// whose Rel is not a key of skip (nil skips nothing), each at ITS root:
// RootProject entries under root's .ctxloom, RootHome entries under the
// isolated HOME this test's setupProject call already set via testsupport.
// Isolate.
func materializeLayoutEntries(t *testing.T, root string, skip map[string]bool) {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	for _, entry := range paths.Layout() {
		if entry.Tier != paths.TierLocal || skip[entry.Rel] {
			continue
		}
		base := root
		if entry.Root == paths.RootHome {
			base = home
		}
		materializeLayoutEntry(t, base, entry.Rel)
	}
}

// layoutFileEntries names every paths.Layout() TierLocal Rel that is a FILE
// on disk rather than a directory — materializeLayoutEntry's exception list.
// Keyed by Rel alone (not Root): no directory-shaped Rel collides with one of
// these names, so root is irrelevant to the question "is this a file".
var layoutFileEntries = map[string]bool{
	filepath.Join(paths.AppDirName, paths.ProjectIDFileName):                true,
	filepath.Join(paths.AppDirName, paths.AllowedSignersFileName):           true,
	filepath.Join(paths.AppDirName, paths.DistrustedSignersFileName):        true,
	filepath.Join(paths.AppDirName, paths.CompanionConsentFileName+".yaml"): true,
}

// materializeLayoutEntry creates one Layout path under base as the KIND it
// really is, per layoutFileEntries — everything else is a directory.
//
// Writing a file for all of them used to work and stopped the moment the layout
// grew a nested entry (.ctxloom/state holds per-session subdirectories): the fixture
// turned real directories into files, and the damage surfaced two checks later
// as an ENOTDIR from doctor's MCP reader rather than as "this fixture is
// wrong".
func materializeLayoutEntry(t *testing.T, base, rel string) {
	t.Helper()
	full := filepath.Join(base, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	if layoutFileEntries[rel] {
		require.NoError(t, os.WriteFile(full, []byte("test-fixture placeholder\n"), 0o644))
		return
	}
	require.NoError(t, os.MkdirAll(full, 0o755))
}

// TestDoctorTrustStoreDetail_UnreadableEntriesWarnAndAreNotCountedActive pins
// the real defect. An earlier finding claimed doctorCheckHooksTrust appends
// ListSigners' ERROR text without setting warn; that mechanism is refuted —
// ListSigners returns `out, nil` unconditionally (signer.go), so the
// error arm is unreachable. The IMPACT it describes was real by another route:
// a store ListSigners could not read comes back as SignerListing rows with
// Unreadable set, the old count treated every non-Suppressed row as an active
// signer, and the status stayed "ok" — reporting more trust than the machine
// has, and calling it healthy.
func TestDoctorTrustStoreDetail_UnreadableEntriesWarnAndAreNotCountedActive(t *testing.T) {
	detail, ok := doctorTrustStoreDetail([]SignerListing{
		{Source: "embedded", Path: "(compiled-in)"},
		{Source: "embedded", Path: "(compiled-in)", Suppressed: true},
		{Source: "project", Path: "/p/.ctxloom/allowed_signers", Unreadable: "line 2 is not a usable entry"},
	}, nil)

	assert.False(t, ok, "a store that could not be fully read is not an 'ok' trust store")
	assert.Contains(t, detail, "1 active signer(s)",
		"an unreadable row grants no trust and must not inflate the count")
	assert.Contains(t, detail, "/p/.ctxloom/allowed_signers", "the gap must name the file")
	assert.Contains(t, detail, "grant NO trust")
}

// TestDoctorTrustStoreDetail_ListsProjectStoreExecuteAndApproveGrants: the
// project store is committed, so anyone who can land a commit can add a line
// to it. Doctor names every principal it grants companion execution or
// approval, so such a grant is visible rather than inferred. Publish grants
// and grants from other stores are not listed.
func TestDoctorTrustStoreDetail_ListsProjectStoreExecuteAndApproveGrants(t *testing.T) {
	const path = "/p/.ctxloom/allowed_signers"
	grant := func(principal, source string, ns ...string) SignerListing {
		return SignerListing{Source: source, Path: path, Entry: allowedsigners.Entry{Principals: []string{principal}, Namespaces: ns}}
	}
	detail, ok := doctorTrustStoreDetail([]SignerListing{
		grant("ci@example.com", signerSourceProject, signing.NamespaceCompanion),
		grant("lead@example.com", signerSourceProject, signing.NamespaceApprove, signing.NamespaceReject),
		grant("publisher@example.com", signerSourceProject, signing.NamespacePublish),
		grant("me@example.com", "user", signing.NamespaceCompanion),
	}, nil)

	assert.True(t, ok, "listing a grant is information, not a fault")
	assert.Contains(t, detail, signing.NamespaceCompanion+" (execute companions) to ci@example.com")
	assert.Contains(t, detail, signing.NamespaceApprove+" to lead@example.com")
	assert.Contains(t, detail, path)
	assert.NotContains(t, detail, "publisher@example.com", "a publish-only grant is not listed")
	assert.NotContains(t, detail, "me@example.com", "the user store is not committed to the repo")
}

func TestDoctorTrustStoreDetail_HealthyStore(t *testing.T) {
	detail, ok := doctorTrustStoreDetail([]SignerListing{
		{Source: "embedded", Path: "(compiled-in)"},
		{Source: "project", Path: "/p/.ctxloom/allowed_signers"},
	}, nil)

	assert.True(t, ok)
	assert.Equal(t, "trust store: 2 active signer(s)", detail)
}

func TestDoctorTrustStoreDetail_ErrorArmWarns(t *testing.T) {
	// Defensive only: ListSigners cannot currently return an error (it ends in
	// `return out, nil`), so this arm is unreachable in production. It is still
	// pinned, because the old code appended the error text and left the status
	// at "ok".
	detail, ok := doctorTrustStoreDetail(nil, assert.AnError)
	assert.False(t, ok)
	assert.Contains(t, detail, assert.AnError.Error())
}

// TestDoctorCheckHooksTrust_WrongState_UnreadableProjectTrustStore drives the
// whole check against a REAL malformed allowed_signers file, so the wiring
// (not just the helper) is pinned: a line with no key field is a parse error
// ListSigners surfaces as an Unreadable row.
func TestDoctorCheckHooksTrust_WrongState_UnreadableProjectTrustStore(t *testing.T) {
	testsupport.Isolate(t) // keep the developer's ~/.ctxloom store out of the listing
	appDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "allowed_signers"),
		[]byte("this-line-has-no-key-field\n"), 0o644))
	// No configured agents: the hooks half short-circuits, isolating the trust half.
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	check := doctorCheckHooksTrust(context.Background(), engines.Registry(), cfg, nil)

	assert.Equal(t, DoctorWarn, check.Status,
		"a trust store the loader could not fully read must not report ok")
	assert.Contains(t, check.Detail, "grant NO trust")
	assert.Contains(t, check.Detail, appDir)
}

// --- a container runtime is required only where containers run ---

// TestDoctorContainerRuntimeRequired pins that DEPS-a1 used to bucket
// docker/podman as unconditionally REQUIRED ("git, every configured engine's
// client, and a container runtime are all on PATH (required)"), but ctxloom runs
// engines on the host by default: a project with no container agents needs no
// container runtime, and warning it does trains the user to ignore the report.
//
// Both ownership modes appear on both the per-agent and the project-default
// path on purpose: the check asks "is this containerized AT ALL", so an
// equality test against a single mode would still pass a rootless-only table
// while doctor silently stopped counting rootful agents.
func TestDoctorContainerRuntimeRequired(t *testing.T) {
	hostAgent := agents.Agent{LLM: "claude-code", Runtime: "host"}
	rootlessAgent := agents.Agent{LLM: "claude-code", Runtime: "container-rootless"}
	rootfulAgent := agents.Agent{LLM: "claude-code", Runtime: "container-rootful"}
	inheritingAgent := agents.Agent{LLM: "claude-code"}

	for _, tc := range []struct {
		name    string
		fixture config.Fixture
		want    bool
	}{
		{"no config at all", config.Fixture{}, false},
		{"host agents only", config.Fixture{Agents: map[string]agents.Agent{"a": hostAgent}}, false},
		{"one rootless container agent", config.Fixture{Agents: map[string]agents.Agent{"a": hostAgent, "b": rootlessAgent}}, true},
		{"one rootful container agent", config.Fixture{Agents: map[string]agents.Agent{"a": hostAgent, "b": rootfulAgent}}, true},
		{"project default is a rootless container", config.Fixture{Runtime: "container-rootless"}, true},
		{"project default is a rootful container", config.Fixture{Runtime: "container-rootful"}, true},
		{"agent inherits a rootless container project default", config.Fixture{
			Runtime: "container-rootless", Agents: map[string]agents.Agent{"a": inheritingAgent},
		}, true},
		{"agent inherits a rootful container project default", config.Fixture{
			Runtime: "container-rootful", Agents: map[string]agents.Agent{"a": inheritingAgent},
		}, true},
		{"agent overrides a container project default back to host", config.Fixture{
			Runtime: "host", Agents: map[string]agents.Agent{"a": inheritingAgent},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, doctorContainerRuntimeRequired(config.NewFixture(tc.fixture)))
		})
	}

	assert.False(t, doctorContainerRuntimeRequired(nil), "a nil config must not claim a hard dependency")
}

// TestDoctorCheckDeps_NoContainerAgents_RuntimeIsRecommendedNotRequired is the
// end-to-end half: with git/ssh/ssh-keygen present and NO container runtime
// reachable, a host-only project must report the runtime as recommended.
func TestDoctorCheckDeps_NoContainerAgents_RuntimeIsRecommendedNotRequired(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "ssh-keygen", "git", "claude"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir) // no docker, no podman

	check := doctorCheckDeps(engines.Registry(), config.NewFixture(config.Fixture{
		Agents: map[string]agents.Agent{"a": {LLM: "claude-code", Runtime: "host"}},
	}))

	assert.Equal(t, DoctorWarn, check.Status, "a missing recommended dep still warns")
	assert.Contains(t, check.Detail, "container runtime")
	assert.NotContains(t, check.Detail, "missing (required)",
		"a host-only project has NOTHING required missing here:\n%s", check.Detail)
	assert.Contains(t, check.Detail, "missing (recommended")
}

// TestDoctorCheckDeps_ContainerAgent_RuntimeStaysRequired is the control: the
// project that DOES run containers keeps the hard-dependency reading.
// Both ownership modes are controls: EITHER one is a container that has to be
// launched, so a runtime missing from PATH is a hard dependency for both.
func TestDoctorCheckDeps_ContainerAgent_RuntimeStaysRequired(t *testing.T) {
	for _, mode := range []string{"container-rootless", "container-rootful"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			for _, bin := range []string{"ssh", "ssh-keygen", "git", "claude"} {
				writeFakeExecutable(t, dir, bin)
			}
			t.Setenv("PATH", dir) // no docker, no podman

			check := doctorCheckDeps(engines.Registry(), config.NewFixture(config.Fixture{
				Agents: map[string]agents.Agent{"a": {LLM: "claude-code", Runtime: mode}},
			}))

			assert.Equal(t, DoctorWarn, check.Status)
			required, _, _ := strings.Cut(check.Detail, "; missing (recommended")
			assert.Contains(t, required, "missing (required)")
			assert.Contains(t, required, "container runtime",
				"a project that runs container agents genuinely needs one:\n%s", check.Detail)
		})
	}
}

// TestGitIdentityDetail_ReadErrorIsReported is characterization coverage added
// before GitIdentityDetail was split: the `git config` READ-failure arm
// (as opposed to a value simply being unset) had no test, so the split would
// otherwise have been unguarded.
func TestGitIdentityDetail_ReadErrorIsReported(t *testing.T) {
	failing := func(ctx context.Context, dir, key string) (string, bool, error) {
		return "", false, errors.New("git: " + key + " unreadable")
	}
	ok, detail := GitIdentityDetail(context.Background(), failing)

	assert.False(t, ok)
	assert.Contains(t, detail, "reading git identity failed")
	assert.Contains(t, detail, "user.name unreadable")
	assert.Contains(t, detail, "user.email unreadable", "both failures must be reported, not just the first")
}

// --- DOCTOR-CHECK-CONTENT-TRUST-n4 -------------------------------------------

// A config that will not load must WARN, never report the content-trust check
// as ok: "I could not look" and "I looked and everything is attributable" are
// opposite answers, and only one of them is safe to render green.
func TestDoctorCheckContentTrust_ConfigErrorWarnsRatherThanReportingOK(t *testing.T) {
	got := doctorCheckContentTrust(nil, errors.New("config exploded"))
	assert.Equal(t, DoctorWarn, got.Status)
	assert.Contains(t, got.Detail, "config did not load")
}

// The predicate that decides which bundles are EXPECTED to carry a publisher
// signature. Local, companion and builtin bundles legitimately carry none —
// local content is trusted by provenance and a companion's bytes are verified
// by its own loadout envelope — so flagging them would put a warning on every
// healthy project, which is how a check trains users to ignore it.
func TestDoctorIsRemoteBundle_OnlyCanonicalRemoteRefsAreExpectedToBeSigned(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"https://github.com/acme/ctx@bundles/deploy-runbook", true},
		{"file:///tmp/remote.git@bundles/deploy-runbook", true},
		{"seed", false},
		{"ctxloom:companion@ltk", false},
		{"ctxloom:local@bundles/my-tools", false},
	} {
		assert.Equal(t, tc.want, doctorIsRemoteBundle(tc.name), "%s", tc.name)
	}
}

// --- DOCTOR-CHECK-GITIGNORE-f6 (J001300 row 1) -----------------------------------

func TestDoctorCheckGitignorePosture_RightState_NoBlanketRule(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".ctxloom/cache/\n"), 0644))

	check := doctorCheckGitignorePosture(cfg, nil)
	assert.Equal(t, DoctorOK, check.Status)
}

func TestDoctorCheckGitignorePosture_RightState_NoFileAtAll(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckGitignorePosture(cfg, nil)
	assert.Equal(t, DoctorOK, check.Status, "an absent .gitignore has nothing superseded in it")
}

// TestDoctorCheckGitignorePosture_WrongState_BlanketRulePresent is J001300 row 1's
// own assertion, pinned directly: the check must name BOTH the ignore rule
// AND the exact retirement command (`ctxloom manage gitignore install`) —
// tests/acceptance/steps_j001300_closeout.go's j001300Answered checks for
// literal ".ctxloom" and "manage gitignore install" in the combined output.
func TestDoctorCheckGitignorePosture_WrongState_BlanketRulePresent(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".ctxloom/*\n"), 0644))

	check := doctorCheckGitignorePosture(cfg, nil)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, ".ctxloom")
	assert.Contains(t, check.Detail, "manage gitignore install")
}

// TestDoctorCheckGitignorePosture_ReadOnly proves the check itself never
// mutates the .gitignore it inspects — it must only ever REPORT the
// superseded rule, never retire it (RetireSupersededFile/Ensure do that, and
// only when a writer explicitly calls them).
func TestDoctorCheckGitignorePosture_ReadOnly(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	path := filepath.Join(root, ".gitignore")
	original := ".ctxloom/*\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0644))

	_ = doctorCheckGitignorePosture(cfg, nil)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, original, string(after), "a doctor check must never write")
}

func TestDoctorCheckGitignorePosture_ConfigErrorWarns(t *testing.T) {
	check := doctorCheckGitignorePosture(nil, errors.New("config exploded"))
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "config did not load")
}

func TestDoctorCheckGitignorePosture_NoMarkerDir(t *testing.T) {
	check := doctorCheckGitignorePosture(&config.Config{}, nil)
	assert.Equal(t, DoctorInfo, check.Status)
}

// --- DOCTOR-CHECK-FOREIGN-WORKTREES-r8 (J001300 row 2) ---------------------------

func TestDoctorCheckForeignWorktrees_NoProjectDir(t *testing.T) {
	check := doctorCheckForeignWorktrees(context.Background(), nil, "")
	assert.Equal(t, DoctorInfo, check.Status)
}

func TestDoctorCheckForeignWorktrees_NotARepo(t *testing.T) {
	dir := t.TempDir()
	g := &git.Fake{Repos: map[string]bool{}} // IsRepo reports false for everything
	check := doctorCheckForeignWorktrees(context.Background(), g, dir)
	assert.Equal(t, DoctorInfo, check.Status)
}

func TestDoctorCheckForeignWorktrees_RightState_OnlyMainWorktree(t *testing.T) {
	root := t.TempDir()
	g := &git.Fake{Worktrees: []git.Worktree{{Path: root, Branch: "refs/heads/main"}}}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Equal(t, DoctorOK, check.Status)
}

// TestDoctorCheckForeignWorktrees_SessionsRootExcluded proves ctxloom's OWN
// scratch worktrees (under paths.HomeSessionsDir()) are never reported here —
// this check's whole job is the population ctxloom did NOT create.
func TestDoctorCheckForeignWorktrees_SessionsRootExcluded(t *testing.T) {
	testsupport.Isolate(t)
	root := t.TempDir()
	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	scratch := filepath.Join(sessionsRoot, "amber-quiet-heron", "ephemeral", "ctxloom-wt-clean")

	g := &git.Fake{Worktrees: []git.Worktree{
		{Path: root, Branch: "refs/heads/main"},
		{Path: scratch, Detached: true},
	}}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Equal(t, DoctorOK, check.Status, "a ctxloom-owned scratch worktree must never be reported as foreign")
}

// TestDoctorCheckForeignWorktrees_WrongState_NamesUnmergedDirtyAndExactCommands
// is J001300 row 2's own assertion, pinned directly: the report must carry the
// foreign tree's name, that it is unmerged, that it is dirty, and the exact
// (safe) commands to remove it — `git worktree remove <path>` then
// `git branch -d <branch>`, NEVER `-D` (the project's refusal list forbids
// force-removing by proxy).
func TestDoctorCheckForeignWorktrees_WrongState_NamesUnmergedDirtyAndExactCommands(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(t.TempDir(), "proj--stale-feature")
	g := &git.Fake{
		Worktrees: []git.Worktree{
			{Path: root, Branch: "refs/heads/main"},
			{Path: foreign, Branch: "refs/heads/stale-feature"},
		},
		Dirty:               map[string]bool{foreign: true},
		MergedBranchesValue: []string{"main"}, // stale-feature is NOT in this list
	}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "proj--stale-feature")
	assert.Contains(t, check.Detail, "unmerged")
	assert.Contains(t, check.Detail, "dirty")
	assert.Contains(t, check.Detail, "git worktree remove "+foreign)
	assert.Contains(t, check.Detail, "git branch -d stale-feature")
	assert.NotContains(t, check.Detail, "-D", "force-removing by proxy is on the project's refusal list")
}

// TestDoctorCheckForeignWorktrees_MergedBranchIsReportedMerged proves the
// merge state is a real read, not a hardcoded "unmerged": a foreign branch
// this check's own MergedBranches call reports as merged must say so.
func TestDoctorCheckForeignWorktrees_MergedBranchIsReportedMerged(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(t.TempDir(), "proj--done-feature")
	g := &git.Fake{
		Worktrees: []git.Worktree{
			{Path: root, Branch: "refs/heads/main"},
			{Path: foreign, Branch: "refs/heads/done-feature"},
		},
		MergedBranchesValue: []string{"main", "done-feature"},
	}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Contains(t, check.Detail, "merged")
	assert.NotContains(t, check.Detail, "unmerged")
}

// TestDoctorCheckForeignWorktrees_NeverFabricatesDirtyState is the review
// correction directly under test: a foreign tree that is CLEAN when doctor
// actually runs (regardless of what it once held) must be reported clean,
// never "dirty" — reporting a fabricated claim about what is safe to delete
// is exactly the defect this journey exists to prevent.
func TestDoctorCheckForeignWorktrees_NeverFabricatesDirtyState(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(t.TempDir(), "proj--stale-feature")
	g := &git.Fake{
		Worktrees: []git.Worktree{
			{Path: root, Branch: "refs/heads/main"},
			{Path: foreign, Branch: "refs/heads/stale-feature"},
		},
		Dirty: map[string]bool{}, // explicitly clean at check time
	}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Contains(t, check.Detail, "clean")
	assert.NotContains(t, check.Detail, "dirty)")
}

// TestDoctorCheckForeignWorktrees_MergedBranchesErrorDoesNotClaimUnmerged
// proves a failed merge-ness probe is reported as UNKNOWN, never silently
// read as "unmerged" — printing "unmerged" without having checked would be
// exactly the fabricated claim MergedBranches exists to prevent.
func TestDoctorCheckForeignWorktrees_MergedBranchesErrorDoesNotClaimUnmerged(t *testing.T) {
	root := t.TempDir()
	foreign := filepath.Join(t.TempDir(), "proj--stale-feature")
	g := &git.Fake{
		Worktrees: []git.Worktree{
			{Path: root, Branch: "refs/heads/main"},
			{Path: foreign, Branch: "refs/heads/stale-feature"},
		},
		MergedBranchesErr: errors.New("git branch --merged: boom"),
	}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Contains(t, check.Detail, "merge state unknown")
	assert.NotContains(t, check.Detail, "unmerged")
}

// --- DOCTOR-CHECK-HARP-DURABILITY-s9 (J001300 row 3) -----------------------------

func TestDoctorCheckHarpDurability_RightState_NoSessionsDirYet(t *testing.T) {
	testsupport.Isolate(t)
	check := doctorCheckHarpDurability()
	assert.Equal(t, DoctorOK, check.Status)
}

// TestDoctorCheckHarpDurability_RightState_OnlyClassifiedFiles: a session
// whose every file lives where its paths.HarpMembers row puts it — the
// essence at the top, the transcript and a note under persist/ — is not
// reported.
func TestDoctorCheckHarpDurability_RightState_OnlyClassifiedFiles(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(harpDir, paths.PersistDirName), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(harpDir, paths.EphemeralDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, paths.EssenceFileName), []byte("essence"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, paths.PersistDirName, paths.CanonicalTranscriptFileName), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, paths.PersistDirName, "notes.md"), []byte("fine here"), 0o644))

	check := doctorCheckHarpDurability()
	assert.Equal(t, DoctorOK, check.Status)
}

// TestDoctorCheckHarpDurability_RightState_EngineTranscriptLinksExcluded pins
// that the per-vendor-log engine-transcript SYMLINKS (the shape
// sessions.linkEngineTranscript writes) are never flagged as an at-risk
// authored artifact — several can legitimately sit at one harp dir's top
// level (one per rotation, one per engine).
func TestDoctorCheckHarpDurability_RightState_EngineTranscriptLinksExcluded(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	target := filepath.Join(t.TempDir(), "vendor.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o644))
	for _, leaf := range []string{"claude-code-sess-1", "claude-code-sess-2", "codex-sess-3"} {
		require.NoError(t, os.Symlink(target, filepath.Join(harpDir, paths.EngineTranscriptLinkPrefix+leaf+".jsonl")))
	}

	check := doctorCheckHarpDurability()
	assert.Equal(t, DoctorOK, check.Status)
}

// TestDoctorCheckHarpDurability_WrongState_NamesTheAuthoredFile is J001300 row
// 3's own assertion: an authored plan file sitting at a harp directory's TOP
// LEVEL must be named, with the word "persist" in the fix.
func TestDoctorCheckHarpDurability_WrongState_NamesTheAuthoredFile(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "amber-quiet-heron.plan.md"), []byte("design notes"), 0o644))

	check := doctorCheckHarpDurability()
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "amber-quiet-heron.plan.md")
	assert.Contains(t, check.Detail, ".plan.md")
	assert.Contains(t, check.Detail, "persist")
}

// TestDoctorCheckHarpDurability_SkipsNonDirectoryAtSessionsRootTopLevel is the
// second review correction under direct test: index.yaml sits at
// HomeSessionsDir()'s OWN top level, beside the harp directories, not inside
// any one harp. A walk that does not guard IsDir() on the OUTER iteration
// would try to os.ReadDir(".../sessions/index.yaml") and either error or
// (worse) silently misclassify it; either way it must never appear in the
// report, and a real authored file in a real harp dir alongside it must still
// be found.
func TestDoctorCheckHarpDurability_SkipsNonDirectoryAtSessionsRootTopLevel(t *testing.T) {
	testsupport.Isolate(t)
	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsRoot, "index.yaml"), []byte("sessions: []\n"), 0o644))

	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "amber-quiet-heron.plan.md"), []byte("design notes"), 0o644))

	check := doctorCheckHarpDurability()
	assert.Equal(t, DoctorWarn, check.Status, "index.yaml at the sessions root must not crash or suppress the real finding")
	assert.Contains(t, check.Detail, "amber-quiet-heron.plan.md")
	assert.NotContains(t, check.Detail, "index.yaml")
}

// TestDoctorCheckHarpDurability_CapsNamedListWithCount proves a project with
// many flagged files gets a bounded, readable line rather than an unbounded
// wall of paths.
func TestDoctorCheckHarpDurability_CapsNamedListWithCount(t *testing.T) {
	testsupport.Isolate(t)
	for i := range 8 {
		harp := fmt.Sprintf("harp-%d", i)
		harpDir, err := paths.HarpDir(harp)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(harpDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(harpDir, harp+".plan.md"), []byte("notes"), 0o644))
	}

	check := doctorCheckHarpDurability()
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, "8 authored file(s)")
	assert.Contains(t, check.Detail, "more")
}

// isolatedHome is the HOME testsupport.Isolate set for this test — the value
// the composition root hands operations.Doctor as DoctorRequest.Home.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return home
}

// probeSources is a config.Sources whose companion reader is handed a probe
// directly, for a test about what the catalog decided about a companion.
type probeSources struct {
	cfg   *config.Config
	probe bundles.CompanionProber
}

func (s probeSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	return s.cfg, nil, nil
}

func (s probeSources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.Trust().Root()
	return []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
		bundles.NewCompanionReader(s.probe, bundles.WithTrustRoot(root)),
	}, nil
}

func (s probeSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// withCompanionProbe returns cfg as the generation a process would hold when
// companion discovery answers with probe — what the composition root's
// companion reader would have read.
func withCompanionProbe(t *testing.T, cfg *config.Config, probe bundles.CompanionProber) *config.Config {
	t.Helper()
	owner, err := config.Open(context.Background(), probeSources{cfg: cfg, probe: probe})
	require.NoError(t, err)
	return owner.Current().Config
}

// --- DOCTOR-CHECK-ORPHAN-CONTAINERS-z2 ------------------------------------

// TestDoctorCheckOrphanContainers_ReapsOnEveryRuntimePresent: doctor is the
// one place the orphan-container reaper runs. It asks every runtime present,
// says nothing is wrong when none of them held an orphan, and warns — naming
// the runtime — when one did, because a runner that outlived its owner was
// wedged past its own owner-loss exit.
func TestDoctorCheckOrphanContainers_ReapsOnEveryRuntimePresent(t *testing.T) {
	none := doctorCheckOrphanContainers(context.Background(), nil, nil)
	assert.Equal(t, DoctorInfo, none.Status, none.Detail)

	var asked []string
	reap := func(reaped map[string]int) func(context.Context, isolation.Runtime) isolation.ContainerReapResult {
		return func(_ context.Context, rt isolation.Runtime) isolation.ContainerReapResult {
			asked = append(asked, rt.Name())
			return isolation.ContainerReapResult{Reaped: reaped[rt.Name()]}
		}
	}
	both := []isolation.Runtime{isolation.Docker{}, isolation.Podman{}}

	clean := doctorCheckOrphanContainers(context.Background(), both, reap(nil))
	assert.Equal(t, DoctorOK, clean.Status, clean.Detail)
	assert.Equal(t, []string{"docker", "podman"}, asked, "every runtime present is swept")

	asked = nil
	found := doctorCheckOrphanContainers(context.Background(), both, reap(map[string]int{"podman": 2}))
	assert.Equal(t, DoctorWarn, found.Status)
	assert.Contains(t, found.Detail, "2 podman")
	assert.NotContains(t, found.Detail, "docker", "a runtime that held no orphan is not named as having one")
}
