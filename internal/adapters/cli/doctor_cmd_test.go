package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
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
// needs ONE additional binary resolvable (e.g. a container runtime, which
// is not expected to be on the suite's real host PATH)
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
	_, err := operations.InitializeProject(context.Background(), engines.Registry(), operations.InitializeProjectRequest{
		AppDir: appDir, Engine: engine,
	})
	require.NoError(t, err)
	cfg, err = configload.Load(configload.WithAppDir(appDir))
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
// exectoken.IsManaged(command, "ctxloom") — keyed on that exact exec-token
// identity — would report it as foreign, not ctxloom-managed. Both are
// needed for doctorCheckHooksTrust to observe "ok" hermetically, on any
// host, matching the SAME hooks a fully-wired project always carries
// regardless of what's cached under its real $HOME.
func applyHooksHermetically(t *testing.T, cfg *config.Config, root, backend string) {
	t.Helper()
	t.Cleanup(selfexec.SetPathForTesting("ctxloom"))
	_, err := operations.ApplyHooks(context.Background(), engines.Registry(), operations.ApplyHooksRequest{
		Cfg: cfg, Backend: backend, WorkDir: root, RegenerateContext: true,
	})
	require.NoError(t, err)
}

// --- DOCTOR-CHECK-SETUP-MARKER-e5 ---

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
	chdir(t, root)
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

// doctorChecksOf parses `ctxloom doctor`'s structured report out of what the
// command wrote.
//
// A test binary's stdout is never a terminal, so cliemit.Resolve hands doctor
// the machine-readable default and the report arrives as JSON — the same bytes
// every piped or scripted caller now gets. Reading the record beats scanning
// the rendered lines: operations.DoctorCheck.Status is pinned as a field rather than as a
// "[warn]" substring the renderer happened to place near a marker, and the
// human rendering keeps its own coverage in
// TestDoctorStatus_WireValuesAreUnchanged, which drives renderDoctorReport
// directly.
func doctorChecksOf(t *testing.T, out string) []operations.DoctorCheck {
	t.Helper()
	var report operations.DoctorReport
	require.NoErrorf(t, json.Unmarshal([]byte(out), &report),
		"doctor must emit a parseable report off a terminal:\n%s", out)
	require.NotEmptyf(t, report.Checks, "the report carried no checks at all:\n%s", out)
	return report.Checks
}

// doctorCheckNamed returns the ONE check carrying marker, failing when none or
// several do. A check that was never wired fails here as a missing RECORD,
// which is the distinction a whole-output substring search cannot make.
func doctorCheckNamed(t *testing.T, out, marker string) operations.DoctorCheck {
	t.Helper()
	var found []operations.DoctorCheck
	for _, c := range doctorChecksOf(t, out) {
		if c.Marker == marker {
			found = append(found, c)
		}
	}
	require.Lenf(t, found, 1, "expected exactly one %s check in:\n%s", marker, out)
	return found[0]
}

// doctorMarkersWithStatus lists every marker in the report reporting want —
// for the assertions that are about the report AS A WHOLE ("nothing warns",
// "something warns") rather than about one named check.
func doctorMarkersWithStatus(t *testing.T, out string, want operations.DoctorStatus) []string {
	t.Helper()
	var markers []string
	for _, c := range doctorChecksOf(t, out) {
		if c.Status == want {
			markers = append(markers, c.Marker)
		}
	}
	return markers
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
	sock := filepath.Join(testsupport.SocketDir(t, "agent.sock"), "agent.sock")
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
	// The default agent's seed profile is gone — a real misconfiguration
	// `doctor` DOES flag (as a "warn" line) — but the command itself stays
	// diagnostic-only per its documented contract: always exits 0, never
	// blocks. (Absent project-side hooks are NOT a misconfiguration: a
	// session carries its own, so HOOKS-TRUST reports that posture as ok.)
	require.NoError(t, os.Remove(filepath.Join(root, ".ctxloom", "profiles", operations.SeedProfileName+".yaml")))
	out, err := runDoctor(t, root)
	require.NoError(t, err, "`ctxloom doctor` must never fail the process even when it finds a misconfiguration")
	assert.Equal(t, operations.DoctorWarn, doctorCheckNamed(t, out, "DOCTOR-CHECK-AGENTS-b2").Status,
		"the misconfiguration must still be VISIBLE in the report:\n"+out)
	assert.Equal(t, operations.DoctorOK, doctorCheckNamed(t, out, "DOCTOR-CHECK-HOOKS-TRUST-d4").Status,
		"nothing project-side is the healthy delivery posture, never a warn:\n"+out)
}

func TestDoctorCmd_ReportsCleanOnRightState(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	stubLocalDefaultProfile(t, root)
	applyHooksHermetically(t, cfg, root, "claude-code")

	// A fully-wired project must show no warn lines at all, including the
	// host-dependent checks — so it needs a hermetic ssh-agent with a
	// resolvable sole identity and a real git identity — not the empty
	// defaults runDoctor/runDoctorWithSSHAgentSock otherwise force.
	// DOCTOR-CHECK-DEPS-a1 needs the same treatment for its two probes that
	// have no ambient presence in a bare container (unlike git/ssh/ssh-keygen,
	// which the devcontainer image itself provides): a fake "claude" binary
	// (doctorEngineBinaries["claude-code"]) and a fake "docker" — its `docker
	// info` reachability check (isolation.Docker.Available) only shells out to
	// whatever LookPath finds, so a no-op script satisfies it.
	sock := startFakeSSHAgent(t, "ben@abbitt.me")
	prependFakeBinToPath(t, "claude")
	prependFakeBinToPath(t, "docker")
	scaffoldLocalTierState(t, root)
	out, err := runDoctorClean(t, root, sock)
	require.NoError(t, err)
	for _, marker := range []string{
		"DOCTOR-CHECK-SETUP-MARKER-e5",
		"DOCTOR-CHECK-DEPS-a1",
		"DOCTOR-CHECK-SIGNKEY-k1",
		"DOCTOR-CHECK-GITIDENT-l2",
		"DOCTOR-CHECK-HOOKS-TRUST-d4",
		"DOCTOR-CHECK-LOCAL-STATE-p6",
	} {
		check := doctorCheckNamed(t, out, marker)
		assert.Equalf(t, operations.DoctorOK, check.Status, "%s must resolve ok on a fully-wired project: %s", marker, check.Detail)
	}
	assert.Empty(t, doctorMarkersWithStatus(t, out, operations.DoctorWarn),
		"a fully-wired project must produce no warn check at all:\n"+out)
}

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

// TestDoctorCmd_DepsFlag_ScopesToDepsAlone proves `ctxloom doctor --deps`
// runs ONLY the machine-capability probes — DOCTOR-CHECK-DEPS-a1,
// DOCTOR-CHECK-SIGNKEY-k1 and DOCTOR-CHECK-GITIDENT-l2 (signing-key and
// git-identity readiness both belong beside DEPS-a1: they're dep/capability
// questions too, true-or-false regardless of project setup) — on a project
// with an empty
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
	assert.Equal(t, 3, lines, "--deps must emit exactly the three machine-capability check lines")
	assert.Contains(t, out, "DOCTOR-CHECK-DEPS-a1")
	assert.Contains(t, out, "DOCTOR-CHECK-SIGNKEY-k1", "signing-key readiness is a dep/capability check, must be included in --deps scope")
	assert.Contains(t, out, "DOCTOR-CHECK-GITIDENT-l2", "git-identity readiness is a dep/capability check, must be included in --deps scope")
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
	assert.NotContains(t, out, "DOCTOR-CHECK-SETUP-MARKER-e5")
}

func TestDoctorCmd_DepsFlag_JSONShapeIsDepsSignKeyAndGitIdentity(t *testing.T) {
	root := t.TempDir()
	out, err := runDoctor(t, root, "--deps", "--format", "json")
	require.NoError(t, err)
	var report operations.DoctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Checks, 3)
	markers := make([]string, len(report.Checks))
	for i, c := range report.Checks {
		markers[i] = c.Marker
	}
	assert.Contains(t, markers, "DOCTOR-CHECK-DEPS-a1")
	assert.Contains(t, markers, "DOCTOR-CHECK-SIGNKEY-k1")
	assert.Contains(t, markers, "DOCTOR-CHECK-GITIDENT-l2")
}

func TestDoctorCmd_JSONShape(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	_, err := operations.ApplyHooks(context.Background(), engines.Registry(), operations.ApplyHooksRequest{
		Cfg: cfg, Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)

	out, err := runDoctor(t, root, "--format", "json")
	require.NoError(t, err)

	var report operations.DoctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report), "output must be valid JSON matching operations.DoctorReport")
	require.NotEmpty(t, report.Checks)

	markers := make([]string, 0, len(report.Checks))
	for _, c := range report.Checks {
		require.NotEmpty(t, c.Marker)
		require.Contains(t, []operations.DoctorStatus{operations.DoctorOK, operations.DoctorWarn, operations.DoctorInfo}, c.Status)
		markers = append(markers, c.Marker)
	}
	sort.Strings(markers)
	for _, want := range []string{
		"DOCTOR-CHECK-SETUP-MARKER-e5",
		"DOCTOR-CHECK-DEPS-a1",
		"DOCTOR-CHECK-SIGNKEY-k1",
		"DOCTOR-CHECK-GITIDENT-l2",
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
	root, cfg := setupProject(t, "claude-code")
	_, err := operations.ApplyHooks(context.Background(), engines.Registry(), operations.ApplyHooksRequest{
		Cfg: cfg, Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)

	before := hashTree(t, root)
	_, err = runDoctor(t, root)
	require.NoError(t, err)
	after := hashTree(t, root)
	assert.Equal(t, before, after, "`doctor` on a healthy project must not change a single byte on disk")

	// Also across the WARN path: remove the default agent's seed profile so
	// a real check fails, and confirm the failure report itself still writes
	// nothing.
	require.NoError(t, os.Remove(filepath.Join(root, ".ctxloom", "profiles", operations.SeedProfileName+".yaml")))
	before2 := hashTree(t, root)
	out, err := runDoctor(t, root)
	require.NoError(t, err)
	// the misconfiguration IS detected — named, not merely "something warned",
	// so a doctor that lost this check cannot satisfy the precondition...
	assert.Equal(t, operations.DoctorWarn, doctorCheckNamed(t, out, "DOCTOR-CHECK-AGENTS-b2").Status, out)
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

// TestDoctorStatus_WireValuesAreUnchanged pins the constraint: the three
// statuses are the vocabulary the "ctxloom-doctor" Agent Skill and every
// `doctor --format json` consumer read, so naming the type must not have moved a
// single byte on the wire.
func TestDoctorStatus_WireValuesAreUnchanged(t *testing.T) {
	assert.Equal(t, "ok", string(operations.DoctorOK))
	assert.Equal(t, "warn", string(operations.DoctorWarn))
	assert.Equal(t, "info", string(operations.DoctorInfo))

	data, err := json.Marshal(operations.DoctorCheck{Marker: "M", Status: operations.DoctorWarn, Detail: "d"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"marker":"M","status":"warn","detail":"d"}`, string(data),
		"a named string type must marshal exactly as the literal did")

	var buf bytes.Buffer
	require.NoError(t, renderDoctorReport(&buf, operations.DoctorReport{Checks: []operations.DoctorCheck{
		{Marker: "DOCTOR-CHECK-X", Status: operations.DoctorInfo, Detail: "d"},
	}}))
	assert.Contains(t, buf.String(), "DOCTOR-CHECK-X [info] d",
		"the human line must render the status the same way too")
}

// A doctor row's remedy renders as the one human fix line under its row, and
// as the "remedy" key in structured output; a row without one grows neither.
func TestRenderDoctorReport_RemedyIsTheFixLine(t *testing.T) {
	const fix = "ctxloom deps pull"
	var buf bytes.Buffer
	require.NoError(t, renderDoctorReport(&buf, operations.DoctorReport{Checks: []operations.DoctorCheck{
		{Marker: "DOCTOR-CHECK-X", Status: operations.DoctorWarn, Detail: "d", Remedy: fix},
		{Marker: "DOCTOR-CHECK-Y", Status: operations.DoctorWarn, Detail: "e"},
	}}))
	assert.Contains(t, buf.String(), "DOCTOR-CHECK-X [warn] d"+clifmt.FixLine("    ", fix)+"\n")
	assert.Contains(t, buf.String(), "DOCTOR-CHECK-Y [warn] e\n")
	assert.Equal(t, 1, strings.Count(buf.String(), "fix:"))

	data, err := json.Marshal(operations.DoctorCheck{Marker: "M", Status: operations.DoctorWarn, Detail: "d", Remedy: fix})
	require.NoError(t, err)
	assert.JSONEq(t, `{"marker":"M","status":"warn","detail":"d","remedy":"ctxloom deps pull"}`, string(data))
}
