package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
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

// prependFakeBinToPath adds a fake executable named name to a NEW PATH entry
// prepended in front of the host's real PATH — for a full-command test that
// needs ONE additional binary resolvable (e.g. claude-code-acp, which is
// npm-installed and not expected to be on the suite's real host PATH)
// without losing the real PATH's other binaries (git, ssh, ssh-keygen, the
// engine clients, a container runtime) that the same test also depends on.
func prependFakeBinToPath(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	writeFakeExecutable(t, dir, name)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// setupProject scaffolds a real, hermetic (no network) .ctxloom project via
// operations.InitializeProject — the same call `ctxloom manage install`
// makes — under a fresh temp dir, and loads it back with config.Load. The
// scaffolded agent ("default") binds one profile ("default", the embedded
// seed profile InitializeProject writes) to the given engine label.
func setupProject(t *testing.T, engine string) (root string, cfg *config.Config) {
	t.Helper()
	// Real-OS-fs config.Load below (no config.WithFS): isolate HOME so the
	// home-layer read (D2/D3 layering) never reaches this developer's real
	// ~/.ctxloom — this scaffolded project is meant to be the only source.
	testsupport.Isolate(t)
	root = t.TempDir()
	appDir := filepath.Join(root, ".ctxloom")
	_, err := operations.InitializeProject(context.Background(), operations.InitializeProjectRequest{
		AppDir: appDir, Engine: engine,
	})
	require.NoError(t, err)
	cfg, err = config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	return root, cfg
}

// applyHooksHermetically runs operations.ApplyHooks the same way every real
// caller does (manage.go, init.go all pass RegenerateContext: true) so the
// always-available, network-free SessionStart context-injection hook lands —
// unlike the bundle-shipped hooks a profile's remote parent would otherwise
// supply, this one needs no `ctxloom deps pull`/cache, so it's reachable on
// a bare host. It also pins the injected hook's exec token to "ctxloom" via
// selfexec.SetPathForTesting (restored on cleanup): left at its `go test`
// default, the hook would name the test binary itself, and
// agent.IsManaged(command, "ctxloom") — keyed on that exact exec-token
// identity — would report it as foreign, not ctxloom-managed. Both are
// needed for doctorCheckHooksTrust to observe "ok" hermetically, on any
// host, matching the SAME hooks a fully-wired project always carries
// regardless of what's cached under its real $HOME.
func applyHooksHermetically(t *testing.T, cfg *config.Config, root, backend string) {
	t.Helper()
	t.Cleanup(selfexec.SetPathForTesting("ctxloom"))
	_, err := operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend: backend, WorkDir: root, RegenerateContext: true,
	})
	require.NoError(t, err)
}

// --- DOCTOR-CHECK-SETUP-MARKER-e5 ---

func TestDoctorCheckSetupMarker_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupMarker(cfg, nil)
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, cfg.GetAppPaths()[0])
}

func TestDoctorCheckSetupMarker_WrongState_NoMarkerDir(t *testing.T) {
	check := doctorCheckSetupMarker(&config.Config{}, nil)
	assert.Equal(t, doctorWarn, check.Status, "an empty AppPaths must fail loud, not silently pass")
	assert.Contains(t, check.Detail, "no .ctxloom marker directory found")
}

func TestDoctorCheckSetupMarker_WrongState_ConfigLoadError(t *testing.T) {
	check := doctorCheckSetupMarker(nil, assert.AnError)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "config did not load")
}

// --- DOCTOR-CHECK-DEPS-a1: git added to the dep probe ---

func TestDoctorCheckDeps_RightState_GitPresentIsEnumeratedInOK(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "ssh-keygen", "git", "docker"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir)
	check := doctorCheckDeps(&config.Config{})
	// docker/podman availability (isolation.Docker{}.Available()) does more
	// than a PATH lookup, so this may still warn about the container runtime
	// on some hosts; what this test pins down is that git is bucketed with
	// the OTHER always-checked deps, never silently skipped.
	if check.Status == doctorOK {
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
	check := doctorCheckDeps(&config.Config{})
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "git", "a missing git must be named, not silently absorbed into a generic failure")
	assert.Contains(t, check.Detail, "required", "a missing git must be reported in the REQUIRED bucket, not lumped with recommended")
}

func TestDoctorDepBinariesRequired_IncludesGit(t *testing.T) {
	assert.Contains(t, doctorDepBinariesRequired, "git", "worktree isolation and deps pull hard-depend on git")
}

// TestDoctorCheckDeps_WrongState_SSHKeygenMissing_IsRecommendedNotRequired
// pins DEPS-a1's TRUTHFULNESS fix: an audit found ssh-keygen is NEVER exec'd
// by ctxloom (signing is pure Go over the ssh-agent protocol —
// internal/signing/sign.go, internal/signing/agentkey/agentkey.go), so a
// missing ssh-keygen (with git/engine/runtime all present) must be reported
// as RECOMMENDED, not implied to be required for signing, and must NOT use
// the word "signing" to explain why it's missing.
func TestDoctorCheckDeps_WrongState_SSHKeygenMissing_IsRecommendedNotRequired(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"ssh", "git", "docker"} {
		writeFakeExecutable(t, dir, bin)
	}
	t.Setenv("PATH", dir) // deliberately no ssh-keygen
	check := doctorCheckDeps(&config.Config{})
	if check.Status == doctorOK {
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
	check := doctorCheckDeps(&config.Config{})
	if check.Status != doctorOK {
		t.Skip("container runtime unexpectedly unavailable on this host; the all-present ok Detail wording is exercised only on the ok path")
	}
	assert.NotContains(t, check.Detail, "for signing", "must not claim signing needs ssh/ssh-keygen — it's pure Go and never execs either")
}

// --- DOCTOR-CHECK-SIGNKEY-k1: reuses agentkey.Discoverer, the SAME
// resolver `ctxloom sign` itself uses (internal/signing/agentkey), via an
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
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "ssh-agent (sole identity)", "must name the SAME Source agentkey.Discovered reports")
	assert.Contains(t, check.Detail, ssh.FingerprintSHA256(signers[0].PublicKey()), "must name the resolved key's fingerprint")
}

func TestDoctorCheckSignKey_WrongState_NothingResolvable(t *testing.T) {
	disc, _ := signKeyDiscoverer(t) // empty agent, no git config, no explicit key
	check := doctorCheckSignKey(context.Background(), &config.Config{}, disc)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "no signing key resolves")
	assert.Contains(t, check.Detail, "ctxloom review", "must lead with approve — a missing key blocks ordinary review, not just publishing")
	assert.Contains(t, check.Detail, "ctxloom sign", "must also name the publishing feature this gap affects")
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
	assert.Equal(t, doctorWarn, check.Status)
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
	assert.Equal(t, doctorOK, check.Status)
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
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "Ben <ben@abbitt.me>", "must name the resolved identity")
}

func TestDoctorCheckGitIdentity_WrongState_NameUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.email": "ben@abbitt.me"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.name", "must name which field is missing")
	assert.NotContains(t, check.Detail, "git config --global user.email", "must not falsely also offer an email fix")
	assert.Contains(t, check.Detail, "git config --global user.name", "must give the actionable fix")
}

func TestDoctorCheckGitIdentity_WrongState_EmailUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.name": "Ben"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.email", "must name which field is missing")
	assert.Contains(t, check.Detail, "git config --global user.email", "must give the actionable fix")
}

func TestDoctorCheckGitIdentity_WrongState_BothUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.name")
	assert.Contains(t, check.Detail, "user.email")
}

// TestDoctorCheckGitIdentity_WrongState_BlankValueTreatedAsUnset guards
// against a git config value that's present but empty/whitespace-only (e.g.
// `git config user.name ""`) being mistaken for a real identity.
func TestDoctorCheckGitIdentity_WrongState_BlankValueTreatedAsUnset(t *testing.T) {
	gc := fakeGitConfig(map[string]string{"user.name": "  ", "user.email": "ben@abbitt.me"})
	check := doctorCheckGitIdentity(context.Background(), gc)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "user.name")
}

// --- DOCTOR-CHECK-ACPADAPTER-m3: the ACP adapter is a SEPARATE npm-installed
// binary (claude-code-acp/codex-acp), distinct from the engine's own client
// binary DEPS-a1 checks. Isolated from whatever the host actually has
// installed via t.Setenv("PATH", ...), same discipline as
// TestDoctorCheckDeps_* above — never depends on claude-code-acp really
// being on the machine running this suite.

func TestDoctorCheckACPAdapter_RightState_AdapterPresent(t *testing.T) {
	dir := t.TempDir()
	writeFakeExecutable(t, dir, claude.ClaudeACPAdapter)
	t.Setenv("PATH", dir)
	_, cfg := setupProject(t, "claude-code")

	check := doctorCheckACPAdapter(cfg)
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "claude-code")
}

func TestDoctorCheckACPAdapter_WrongState_AdapterMissingForConfiguredEngine(t *testing.T) {
	dir := t.TempDir() // deliberately empty: claude-code-acp is NOT on this PATH
	t.Setenv("PATH", dir)
	_, cfg := setupProject(t, "claude-code")

	check := doctorCheckACPAdapter(cfg)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, claude.ClaudeACPAdapter, "must name the missing adapter binary")
	assert.Contains(t, check.Detail, "claude-code", "must name the engine it's missing for")
	assert.Contains(t, check.Detail, "npm install -g @zed-industries/"+claude.ClaudeACPAdapter, "must give the exact install command")
	assert.Contains(t, check.Detail, "HOST-runtime", "must scope the warning to host-runtime structured chat")
	assert.Contains(t, check.Detail, "containerized agents", "must acknowledge container-runtime agents get the adapter from their image, not a false universal block")
}

func TestDoctorCheckACPAdapter_WrongState_CodexAdapterMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	_, cfg := setupProject(t, "codex")

	check := doctorCheckACPAdapter(cfg)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, codex.CodexACPAdapter)
	assert.Contains(t, check.Detail, "codex")
	assert.Contains(t, check.Detail, "npm install -g @zed-industries/"+codex.CodexACPAdapter)
}

// TestDoctorCheckACPAdapter_RightState_NativeACPEngineNeedsNone proves kiro
// (which declares agent.ACPNative — no separate adapter subprocess, see
// backends.ACPTransportFor("kiro")) never warns here regardless of PATH.
func TestDoctorCheckACPAdapter_RightState_NativeACPEngineNeedsNone(t *testing.T) {
	dir := t.TempDir() // empty PATH is irrelevant: kiro has no adapter to look up
	t.Setenv("PATH", dir)
	_, cfg := setupProject(t, "kiro")

	check := doctorCheckACPAdapter(cfg)
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "natively")
}

// --- DOCTOR-CHECK-AGENTS-b2: promoted to WARN on an empty roster ---

func TestDoctorCheckAgents_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckAgents(context.Background(), cfg, nil)
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "default")
}

func TestDoctorCheckAgents_WrongState_EmptyRoster(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{}
	cfg = config.NewFixture(f)

	check := doctorCheckAgents(context.Background(), cfg, nil)
	assert.Equal(t, doctorWarn, check.Status, "an empty roster is an incomplete setup postcondition, not a neutral fact")
	assert.Contains(t, check.Detail, "no agents configured")
}

func TestDoctorCheckAgents_WrongState_UnresolvableProfile(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{
		"broken": {Name: "broken", LLM: "claude-code", Profiles: []string{"does-not-exist"}},
	}
	cfg = config.NewFixture(f)
	check := doctorCheckAgents(context.Background(), cfg, nil)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "broken")
}

// --- DOCTOR-CHECK-SETUP-DEPS-h8 (lockfile + context assembly) ---

func TestDoctorCheckSetupLockAndAssembly_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "lockfile: 0 entries parse cleanly")
	assert.Contains(t, check.Detail, "context assembly: succeeds")
}

func TestDoctorCheckSetupLockAndAssembly_WrongState_CorruptLockfile(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	lockPath := filepath.Join(cfg.GetAppPaths()[0], "lock.yaml")
	require.NoError(t, os.WriteFile(lockPath, []byte("not: [valid: yaml: at: all"), 0644))

	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "lockfile:")
}

// --- DOCTOR-CHECK-HOOKS-TRUST-d4: hooks AND MCP registration per backend ---

func TestDoctorCheckHooksTrust_RightState(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	applyHooksHermetically(t, cfg, root, "claude-code")
	t.Chdir(root) // HarnessStatus's default WorkDir path resolves off cwd

	check := doctorCheckHooksTrust(context.Background(), cfg, nil)
	assert.Equal(t, doctorOK, check.Status)
	assert.Contains(t, check.Detail, "hooks/MCP registered for: claude-code")
}

func TestDoctorCheckHooksTrust_WrongState_NotInstalled(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	t.Chdir(root) // no ApplyHooks call: hooks were never installed

	check := doctorCheckHooksTrust(context.Background(), cfg, nil)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "NOT registered")
	assert.Contains(t, check.Detail, "claude-code")
}

func TestDoctorCheckHooksTrust_NoEnginesConfigured(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{}
	cfg = config.NewFixture(f)
	check := doctorCheckHooksTrust(context.Background(), cfg, nil)
	assert.Equal(t, doctorOK, check.Status, "nothing configured to check hooks for is not itself a failure")
	assert.Contains(t, check.Detail, "no engine is configured to check")
}

// --- DOCTOR-CHECK-SETUP-COMPANIONS-i9 / AUTHPING-j0: reporting-only ---

func TestDoctorCheckSetupCompanions_NeverWarns(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupCompanions(cfg, nil)
	assert.NotEqual(t, doctorWarn, check.Status, "companions are optional add-ons, never a doctor failure")
}

func TestDoctorCheckSetupAuthPing_AlwaysInfoAndNamesTheGap(t *testing.T) {
	check := doctorCheckSetupAuthPing()
	assert.Equal(t, doctorInfo, check.Status)
	assert.Contains(t, check.Detail, "no auth-ping surface")
}

// --- DOCTOR-CHECK-LOCAL-STATE-p6 ---

// TestDoctorCheckLocalTierState_RightState_AllPresent proves a checkout that
// actually carries every paths.TierLocal path (internal/paths.Layout) reports
// clean — this is the "already used this project for a while" state, not a
// fresh init's (see the WrongState test below for that one).
func TestDoctorCheckLocalTierState_RightState_AllPresent(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	scaffoldLocalTierState(t, root)

	check := doctorCheckLocalTierState(cfg)
	assert.Equal(t, doctorOK, check.Status)
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

	check := doctorCheckLocalTierState(cfg)
	assert.Equal(t, doctorWarn, check.Status)
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

	check := doctorCheckLocalTierState(cfg)
	assert.Equal(t, doctorWarn, check.Status, "the pre-existing PresenceMustExist project rows still warn")
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
// must-exist rows and the overall status is still doctorWarn.
func TestDoctorCheckLocalTierState_HomeRowPresent_IsReported(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	sessionsRel := filepath.Join(paths.AppDirName, paths.SessionsDir)
	require.NoError(t, os.MkdirAll(filepath.Join(home, sessionsRel), 0o755))

	check := doctorCheckLocalTierState(cfg)
	assert.Equal(t, doctorWarn, check.Status, "the project rows are still missing")
	assert.Contains(t, check.Detail, "home-rooted store(s) in use")
	assert.Contains(t, check.Detail, sessionsRel)
}

// TestDoctorCheckLocalTierState_WrongState_NoMarkerDir mirrors
// doctorCheckSetupMarker's own "no .ctxloom at all" guard: an empty AppPaths
// must fail loud, not silently report "ok" for a directory that doesn't
// exist to check.
func TestDoctorCheckLocalTierState_WrongState_NoMarkerDir(t *testing.T) {
	check := doctorCheckLocalTierState(&config.Config{})
	assert.Equal(t, doctorWarn, check.Status)
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

	check := doctorCheckLocalTierState(cfg)
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "1 local-only path(s) absent")
	assert.Contains(t, check.Detail, skipRel)
}

// --- full command wiring: JSON shape, back-compat "always exits 0", read-only ---

// runDoctor executes the real doctorCmd (not a hand-rolled reimplementation)
// with the given args, in root, returning its stdout and any RunE error.
// doctorCmd's OWN FlagSet (currently just --deps) is added by reference, so
// --deps here binds the SAME doctorDepsOnlyFlag var doctorCmd.RunE reads;
// t.Cleanup resets it so one test's --deps never bleeds into the next.
//
// SSH_AUTH_SOCK is forced empty: doctorCmd.RunE wires DOCTOR-CHECK-SIGNKEY-k1
// to the REAL agentkey.NewDiscoverer(), which dials the host's actual
// ssh-agent. Without this, every full-command test here would depend on
// whatever ssh-agent identities happen to be loaded on the machine running
// the suite — exactly the kind of host-state leak that must never happen.
// runDoctorWithSSHAgentSock lets a test opt into a specific, hermetic
// in-process agent instead when it needs the "ok" resolution path.
func runDoctor(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	return runDoctorWithSSHAgentSock(t, root, "", args...)
}

// runDoctorWithSSHAgentSock is runDoctor with SSH_AUTH_SOCK pointed at a
// caller-supplied socket (see startFakeSSHAgent) instead of forced empty —
// for a full-command test that needs `ctxloom doctor` to actually resolve a
// signing key end to end. Git identity (user.name/user.email) is left
// unresolvable (fresh, empty HOME) — see runDoctorClean for a test that
// needs BOTH checks to land "ok".
func runDoctorWithSSHAgentSock(t *testing.T, root, sshAuthSock string, args ...string) (string, error) {
	t.Helper()
	isolateGitHostState(t, sshAuthSock, t.TempDir())
	return execDoctor(t, root, args...)
}

// runDoctorClean is runDoctor with BOTH host-dependent checks forced to
// resolve cleanly: a hermetic ssh-agent holding one identity (sock) and a
// real, minimal ~/.gitconfig (in the isolated HOME) naming a git identity —
// for the one full-command test that asserts a fully-wired project shows NO
// warn lines anywhere, including the two new checks.
func runDoctorClean(t *testing.T, root, sshAuthSock string, args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	gitconfig := "[user]\n\tname = Ben\n\temail = ben@abbitt.me\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0644))
	isolateGitHostState(t, sshAuthSock, home)
	return execDoctor(t, root, args...)
}

// isolateGitHostState points SSH_AUTH_SOCK and git's config search path at
// caller-controlled locations so `ctxloom doctor`'s DOCTOR-CHECK-SIGNKEY-k1
// and DOCTOR-CHECK-GITIDENT-l2 — both wired to the REAL
// agentkey.NewDiscoverer() in doctorCmd.RunE, which shells out to the real
// git binary and dials the real ssh-agent — never depend on whatever is
// loaded/configured on the machine running the suite. A developer machine
// that already has SSH commit signing AND a git identity configured (exactly
// the population these features are FOR) would otherwise make every
// full-command test's outcome depend on that machine's state — GIT_CONFIG_
// NOSYSTEM additionally excludes /etc/gitconfig, which HOME can't reach.
func isolateGitHostState(t *testing.T, sshAuthSock, home string) {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", sshAuthSock)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// execDoctor builds and executes the real doctorCmd (not a hand-rolled
// reimplementation) with the given args, in root, returning its stdout and
// any RunE error. doctorCmd's OWN FlagSet (currently just --deps) is added
// by reference, so --deps here binds the SAME doctorDepsOnlyFlag var
// doctorCmd.RunE reads; t.Cleanup resets it so one test's --deps never
// bleeds into the next.
func execDoctor(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(root)
	t.Cleanup(func() { doctorDepsOnlyFlag = false })
	buf := &bytes.Buffer{}
	c := &cobra.Command{Use: "doctor", RunE: doctorCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	c.Flags().AddFlagSet(doctorCmd.Flags())
	// The root's persistent flags are re-declared here because this stand-in
	// has no parent to inherit them from — but only when they are not already
	// present. (*Command).Flags() MERGES a command's inherited flags in the
	// first time it is called after an Execute, so once anything in this
	// package has driven `doctor` through the real root, the AddFlagSet above
	// already carried them and a second declaration panics pflag ("doctor flag
	// redefined: format"). That is a test-ordering landmine, which is the kind
	// that lands on whoever adds an unrelated test next.
	addFlagOnce := func(name string, declare func()) {
		if c.Flags().Lookup(name) == nil {
			declare()
		}
	}
	addFlagOnce("format", func() { c.Flags().String("format", formatText, "") })
	addFlagOnce("degraded", func() { c.Flags().Bool("degraded", false, "") })
	addFlagOnce("no-companions", func() { c.Flags().Bool("no-companions", false, "") })
	c.SetOut(buf)
	c.SetContext(context.Background())
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

// startFakeSSHAgent starts a REAL ssh-agent-protocol server (agent.ServeAgent
// over a unix socket — the same wire protocol agentkey's production
// dialEnvAgent speaks) backed by an in-memory keyring holding exactly the
// given comments, so a full-command `ctxloom doctor` test can exercise
// DOCTOR-CHECK-SIGNKEY-k1's "ok" path without ever touching the host
// machine's real ssh-agent. Returns the socket path to set SSH_AUTH_SOCK to;
// the listener is torn down via t.Cleanup.
func startFakeSSHAgent(t *testing.T, comments ...string) string {
	t.Helper()
	kr := agent.NewKeyring()
	for _, comment := range comments {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: priv, Comment: comment}))
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}
			go func() { _ = agent.ServeAgent(kr, conn) }()
		}
	}()
	return sock
}

func TestDoctorCmd_AlwaysExitsCleanEvenWhenMisconfigured(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	// Hooks were never applied — a real misconfiguration `doctor` DOES flag
	// (as a "warn" line) — but the command itself stays diagnostic-only per
	// its documented contract: always exits 0, never blocks.
	out, err := runDoctor(t, root)
	require.NoError(t, err, "`ctxloom doctor` must never fail the process even when it finds a misconfiguration")
	assert.Contains(t, out, "DOCTOR-CHECK-HOOKS-TRUST-d4 [warn]", "the misconfiguration must still be VISIBLE in the report")
}

func TestDoctorCmd_ReportsCleanOnRightState(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	applyHooksHermetically(t, cfg, root, "claude-code")

	// A fully-wired project must show no warn lines at all, including the
	// host-dependent checks — so it needs a hermetic ssh-agent with a
	// resolvable sole identity, a real git identity, and (npm-installed,
	// almost certainly absent from this suite's real host PATH) a fake
	// claude-code-acp so DOCTOR-CHECK-ACPADAPTER-m3 resolves too — not the
	// empty defaults runDoctor/runDoctorWithSSHAgentSock otherwise force.
	// DOCTOR-CHECK-DEPS-a1 needs the same treatment for its two probes that
	// have no ambient presence in a bare container (unlike git/ssh/ssh-keygen,
	// which the devcontainer image itself provides): a fake "claude" binary
	// (doctorEngineBinaries["claude-code"]) and a fake "docker" — its `docker
	// info` reachability check (isolation.Docker.Available) only shells out to
	// whatever LookPath finds, so a no-op script satisfies it exactly like the
	// ACP-adapter fake above.
	sock := startFakeSSHAgent(t, "ben@abbitt.me")
	prependFakeBinToPath(t, claude.ClaudeACPAdapter)
	prependFakeBinToPath(t, "claude")
	prependFakeBinToPath(t, "docker")
	scaffoldLocalTierState(t, root)
	out, err := runDoctorClean(t, root, sock)
	require.NoError(t, err)
	assert.Contains(t, out, "DOCTOR-CHECK-SETUP-MARKER-e5 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-DEPS-a1 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-SIGNKEY-k1 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-GITIDENT-l2 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-ACPADAPTER-m3 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-HOOKS-TRUST-d4 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-LOCAL-STATE-p6 [ok]")
	assert.NotContains(t, out, "[warn]", "a fully-wired project must show no warn lines")
}

// scaffoldLocalTierState creates a stand-in for every paths.TierLocal path
// (internal/paths.Layout) — the local-only state a FRESH init/machine never
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

// TestDoctorCmd_DepsFlag_ScopesToDepsAlone proves `ctxloom doctor --deps`
// runs ONLY the machine-capability probes — DOCTOR-CHECK-DEPS-a1,
// DOCTOR-CHECK-SIGNKEY-k1, DOCTOR-CHECK-GITIDENT-l2, and
// DOCTOR-CHECK-ACPADAPTER-m3 (signing-key, git-identity, and ACP-adapter
// readiness all belong beside DEPS-a1: they're dep/capability questions too,
// true-or-false regardless of project setup) — on a project with an empty
// agent roster (which unscoped `doctor` reports as a WARN — see
// TestDoctorCheckAgents_WrongState_EmptyRoster), the scoped invocation must
// show none of that noise, matching what init's PRIME/setup skill's phase 1
// need — a clean machine-capability check before anything is configured yet.
func TestDoctorCmd_DepsFlag_ScopesToDepsAlone(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	f := cfg.ToFixture()
	f.Agents = map[string]agents.Agent{} // would otherwise WARN unscoped
	cfg = config.NewFixture(f)
	_ = cfg

	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)

	lines := 0
	for _, line := range bytes.Split([]byte(out), []byte("\n")) {
		if bytes.Contains(line, []byte("DOCTOR-CHECK-")) {
			lines++
		}
	}
	assert.Equal(t, 4, lines, "--deps must emit exactly the four machine-capability check lines")
	assert.Contains(t, out, "DOCTOR-CHECK-DEPS-a1")
	assert.Contains(t, out, "DOCTOR-CHECK-SIGNKEY-k1", "signing-key readiness is a dep/capability check, must be included in --deps scope")
	assert.Contains(t, out, "DOCTOR-CHECK-GITIDENT-l2", "git-identity readiness is a dep/capability check, must be included in --deps scope")
	assert.Contains(t, out, "DOCTOR-CHECK-ACPADAPTER-m3", "ACP-adapter readiness is a dep/capability check, must be included in --deps scope")
	assert.NotContains(t, out, "DOCTOR-CHECK-AGENTS-b2", "--deps must not surface the empty-roster warn")
	assert.NotContains(t, out, "DOCTOR-CHECK-SETUP-MARKER-e5")
	assert.NotContains(t, out, "DOCTOR-CHECK-HOOKS-TRUST-d4")
}

// TestDoctorCmd_DepsFlag_WorksBeforeAnySetup proves --deps is usable in a
// directory with NO .ctxloom at all — the exact moment init's PRIME needs it,
// before there is a project to be noisy about.
func TestDoctorCmd_DepsFlag_WorksBeforeAnySetup(t *testing.T) {
	root := t.TempDir() // deliberately: no operations.InitializeProject call
	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)
	assert.Contains(t, out, "DOCTOR-CHECK-DEPS-a1")
	assert.Contains(t, out, "DOCTOR-CHECK-SIGNKEY-k1")
	assert.Contains(t, out, "DOCTOR-CHECK-GITIDENT-l2")
	assert.Contains(t, out, "DOCTOR-CHECK-ACPADAPTER-m3")
	assert.NotContains(t, out, "DOCTOR-CHECK-SETUP-MARKER-e5")
}

func TestDoctorCmd_DepsFlag_JSONShapeIsDepsSignKeyGitIdentityAndACPAdapter(t *testing.T) {
	root := t.TempDir()
	out, err := runDoctor(t, root, "--deps", "--format", "json")
	require.NoError(t, err)
	var report doctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Checks, 4)
	markers := make([]string, len(report.Checks))
	for i, c := range report.Checks {
		markers[i] = c.Marker
	}
	assert.Contains(t, markers, "DOCTOR-CHECK-DEPS-a1")
	assert.Contains(t, markers, "DOCTOR-CHECK-SIGNKEY-k1")
	assert.Contains(t, markers, "DOCTOR-CHECK-GITIDENT-l2")
	assert.Contains(t, markers, "DOCTOR-CHECK-ACPADAPTER-m3")
}

func TestDoctorCmd_JSONShape(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	_, err := operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)

	out, err := runDoctor(t, root, "--format", "json")
	require.NoError(t, err)

	var report doctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report), "output must be valid JSON matching doctorReport")
	require.NotEmpty(t, report.Checks)

	markers := make([]string, 0, len(report.Checks))
	for _, c := range report.Checks {
		require.NotEmpty(t, c.Marker)
		require.Contains(t, []doctorStatus{doctorOK, doctorWarn, doctorInfo}, c.Status)
		markers = append(markers, c.Marker)
	}
	sort.Strings(markers)
	for _, want := range []string{
		"DOCTOR-CHECK-SETUP-MARKER-e5",
		"DOCTOR-CHECK-DEPS-a1",
		"DOCTOR-CHECK-SIGNKEY-k1",
		"DOCTOR-CHECK-GITIDENT-l2",
		"DOCTOR-CHECK-ACPADAPTER-m3",
		"DOCTOR-CHECK-AGENTS-b2",
		"DOCTOR-CHECK-HOOKS-TRUST-d4",
		"DOCTOR-CHECK-SETUP-DEPS-h8",
		"DOCTOR-CHECK-SETUP-COMPANIONS-i9",
		"DOCTOR-CHECK-SETUP-AUTHPING-j0",
	} {
		assert.Contains(t, markers, want)
	}
}

// TestDoctorCmd_ReadOnly proves the checker never writes: every file under
// .ctxloom is byte-identical (by content hash) before and after a `doctor`
// run, checked both on a healthy project and on a misconfigured one (a
// write hidden behind either branch would still be caught).
func TestDoctorCmd_ReadOnly(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	_, err := operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)

	before := hashTree(t, root)
	_, err = runDoctor(t, root)
	require.NoError(t, err)
	after := hashTree(t, root)
	assert.Equal(t, before, after, "`doctor` on a healthy project must not change a single byte on disk")

	// Also across the WARN path: remove hooks so a real check fails, and
	// confirm the failure report itself still writes nothing.
	require.NoError(t, os.RemoveAll(filepath.Join(root, ".claude")))
	before2 := hashTree(t, root)
	out, err := runDoctor(t, root)
	require.NoError(t, err)
	assert.Contains(t, out, "[warn]") // the misconfiguration IS detected...
	after2 := hashTree(t, root)
	assert.Equal(t, before2, after2, "...but detecting it must not itself write anything")
}

// hashTree returns a relative-path -> sha256 map of every regular file under
// root, so a read-only assertion catches ANY new/changed/deleted file, not
// just a hand-picked one.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		out[rel] = string(sum[:])
		return nil
	})
	require.NoError(t, err)
	return out
}

// --- the trust half of DOCTOR-CHECK-HOOKS-TRUST-d4 ---

// TestDoctorTrustStoreDetail_UnreadableEntriesWarnAndAreNotCountedActive pins
// the real defect. An earlier finding claimed doctorCheckHooksTrust appends
// ListSigners' ERROR text without setting warn; that mechanism is refuted —
// operations.ListSigners returns `out, nil` unconditionally (signer.go), so the
// error arm is unreachable. The IMPACT it describes was real by another route:
// a store ListSigners could not read comes back as SignerListing rows with
// Unreadable set, the old count treated every non-Suppressed row as an active
// signer, and the status stayed "ok" — reporting more trust than the machine
// has, and calling it healthy.
func TestDoctorTrustStoreDetail_UnreadableEntriesWarnAndAreNotCountedActive(t *testing.T) {
	detail, ok := doctorTrustStoreDetail([]operations.SignerListing{
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

func TestDoctorTrustStoreDetail_HealthyStore(t *testing.T) {
	detail, ok := doctorTrustStoreDetail([]operations.SignerListing{
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

	check := doctorCheckHooksTrust(context.Background(), cfg, nil)

	assert.Equal(t, doctorWarn, check.Status,
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

	check := doctorCheckDeps(config.NewFixture(config.Fixture{
		Agents: map[string]agents.Agent{"a": {LLM: "claude-code", Runtime: "host"}},
	}))

	assert.Equal(t, doctorWarn, check.Status, "a missing recommended dep still warns")
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

			check := doctorCheckDeps(config.NewFixture(config.Fixture{
				Agents: map[string]agents.Agent{"a": {LLM: "claude-code", Runtime: mode}},
			}))

			assert.Equal(t, doctorWarn, check.Status)
			required, _, _ := strings.Cut(check.Detail, "; missing (recommended")
			assert.Contains(t, required, "missing (required)")
			assert.Contains(t, required, "container runtime",
				"a project that runs container agents genuinely needs one:\n%s", check.Detail)
		})
	}
}

// TestDoctorStatus_WireValuesAreUnchanged pins the constraint: the three
// statuses are the vocabulary the "ctxloom-doctor" Agent Skill and every
// `doctor --format json` consumer read, so naming the type must not have moved a
// single byte on the wire.
func TestDoctorStatus_WireValuesAreUnchanged(t *testing.T) {
	assert.Equal(t, "ok", string(doctorOK))
	assert.Equal(t, "warn", string(doctorWarn))
	assert.Equal(t, "info", string(doctorInfo))

	data, err := json.Marshal(doctorCheck{Marker: "M", Status: doctorWarn, Detail: "d"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"marker":"M","status":"warn","detail":"d"}`, string(data),
		"a named string type must marshal exactly as the literal did")

	var buf bytes.Buffer
	require.NoError(t, renderDoctorReport(&buf, doctorReport{Checks: []doctorCheck{
		{Marker: "DOCTOR-CHECK-X", Status: doctorInfo, Detail: "d"},
	}}))
	assert.Contains(t, buf.String(), "DOCTOR-CHECK-X [info] d",
		"the human line must render the status the same way too")
}

// TestGitIdentityDetail_ReadErrorIsReported is characterization coverage added
// before gitIdentityDetail was split: the `git config` READ-failure arm
// (as opposed to a value simply being unset) had no test, so the split would
// otherwise have been unguarded.
func TestGitIdentityDetail_ReadErrorIsReported(t *testing.T) {
	failing := func(ctx context.Context, dir, key string) (string, bool, error) {
		return "", false, errors.New("git: " + key + " unreadable")
	}
	ok, detail := gitIdentityDetail(context.Background(), failing)

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
	assert.Equal(t, doctorWarn, got.Status)
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
	assert.Equal(t, doctorOK, check.Status)
}

func TestDoctorCheckGitignorePosture_RightState_NoFileAtAll(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckGitignorePosture(cfg, nil)
	assert.Equal(t, doctorOK, check.Status, "an absent .gitignore has nothing superseded in it")
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
	assert.Equal(t, doctorWarn, check.Status)
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
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "config did not load")
}

func TestDoctorCheckGitignorePosture_NoMarkerDir(t *testing.T) {
	check := doctorCheckGitignorePosture(&config.Config{}, nil)
	assert.Equal(t, doctorInfo, check.Status)
}

// --- DOCTOR-CHECK-FOREIGN-WORKTREES-r8 (J001300 row 2) ---------------------------

func TestDoctorCheckForeignWorktrees_NoProjectDir(t *testing.T) {
	check := doctorCheckForeignWorktrees(context.Background(), nil, "")
	assert.Equal(t, doctorInfo, check.Status)
}

func TestDoctorCheckForeignWorktrees_NotARepo(t *testing.T) {
	dir := t.TempDir()
	g := &git.Fake{Repos: map[string]bool{}} // IsRepo reports false for everything
	check := doctorCheckForeignWorktrees(context.Background(), g, dir)
	assert.Equal(t, doctorInfo, check.Status)
}

func TestDoctorCheckForeignWorktrees_RightState_OnlyMainWorktree(t *testing.T) {
	root := t.TempDir()
	g := &git.Fake{Worktrees: []git.Worktree{{Path: root, Branch: "refs/heads/main"}}}
	check := doctorCheckForeignWorktrees(context.Background(), g, root)
	assert.Equal(t, doctorOK, check.Status)
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
	assert.Equal(t, doctorOK, check.Status, "a ctxloom-owned scratch worktree must never be reported as foreign")
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
	assert.Equal(t, doctorWarn, check.Status)
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
	assert.Equal(t, doctorOK, check.Status)
}

func TestDoctorCheckHarpDurability_RightState_OnlyClassifiedFiles(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(harpDir, "persist"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(harpDir, "ephemeral"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "essence.md"), []byte("essence"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "transcript.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "persist", "notes.md"), []byte("fine here"), 0o644))

	check := doctorCheckHarpDurability()
	assert.Equal(t, doctorOK, check.Status)
}

// TestDoctorCheckHarpDurability_RightState_EngineTranscriptLinksExcluded pins
// that the per-vendor-log engine-transcript symlinks (sessions.
// linkEngineTranscript, fs-consolidation plan C12) are ctxloom-owned and must
// never be flagged as an at-risk authored artifact — several can legitimately
// sit at one harp dir's top level (one per rotation, one per engine).
func TestDoctorCheckHarpDurability_RightState_EngineTranscriptLinksExcluded(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "engine-transcript-claude-code-sess-1.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "engine-transcript-claude-code-sess-2.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, "engine-transcript-codex-sess-3.jsonl"), []byte("{}"), 0o644))

	check := doctorCheckHarpDurability()
	assert.Equal(t, doctorOK, check.Status)
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
	assert.Equal(t, doctorWarn, check.Status)
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
	assert.Equal(t, doctorWarn, check.Status, "index.yaml at the sessions root must not crash or suppress the real finding")
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
	assert.Equal(t, doctorWarn, check.Status)
	assert.Contains(t, check.Detail, "8 authored file(s)")
	assert.Contains(t, check.Detail, "more")
}
