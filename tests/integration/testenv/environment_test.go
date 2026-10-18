package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// ── binary freshness ──────────────────────────────────────────────────────
//
// These pin the guard that stops this package's own suites from measuring a
// binary that predates the source under edit. The defect they exist for
// (taskloom pregnant-laurel) was invisible precisely BECAUSE it was green:
// `just test-pkg ./tests/integration/ -tags integration` execed whatever
// ctxloom was lying around, so a change to cmd/ or internal/ could be verified
// green having never been executed. Anything that quietly turns
// checkBinaryFreshness back into "return nil" must be caught here, because by
// construction no integration test can catch it — the whole failure mode is
// that they keep passing.

// fakeSourceTree writes a minimal project root: one compiled source file, one
// test file, and go.mod, each stamped with an explicit mtime so the comparison
// under test is deterministic rather than a race with the filesystem clock.
func fakeSourceTree(t *testing.T, srcMod, testMod time.Time) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, mod time.Time) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
		return path
	}
	write("internal/thing/thing.go", srcMod)
	write("internal/thing/thing_test.go", testMod)
	write("go.mod", srcMod)
	return root
}

// fakeBinary writes an executable stamped with mod.
func fakeBinary(t *testing.T, mod time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctxloom")
	if err := os.WriteFile(path, []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes binary: %v", err)
	}
	return path
}

func TestCheckBinaryFreshness_RefusesABinaryOlderThanTheSourceCompiledIntoIt(t *testing.T) {
	built := time.Date(2026, 8, 18, 18, 53, 17, 0, time.UTC)
	edited := built.Add(72 * time.Hour)
	root := fakeSourceTree(t, edited, built)
	bin := fakeBinary(t, built)

	err := checkBinaryFreshness(bin, root)
	if err == nil {
		t.Fatal("checkBinaryFreshness accepted a binary three days older than its source; " +
			"that is the blind gate this guard exists to close")
	}
	msg := err.Error()
	// The refusal has to be actionable cold, at 3am, in a CI log: WHICH binary,
	// WHICH file outran it, and what to do. A bare "stale binary" sends the
	// reader hunting.
	for _, want := range []string{
		bin,
		filepath.Join(root, "internal", "thing", "thing.go"),
		// .Local(): the refusal formats os.FileInfo.ModTime(), which the
		// filesystem hands back in the machine's zone whatever zone the
		// fixture named.
		built.Local().Format(time.RFC3339),
		edited.Local().Format(time.RFC3339),
		"just build",
		allowStaleBinaryEnv,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q; got:\n%s", want, msg)
		}
	}
}

func TestCheckBinaryFreshness_AcceptsABinaryNewerThanItsSource(t *testing.T) {
	edited := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	root := fakeSourceTree(t, edited, edited)
	bin := fakeBinary(t, edited.Add(time.Second))

	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("refused a binary built AFTER its source: %v", err)
	}
}

func TestCheckBinaryFreshness_IgnoresTestFileEditsBecauseTheyNeverEnterTheBinary(t *testing.T) {
	built := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	// Only the _test.go file is newer than the binary. A test file is compiled
	// into the TEST process, never into the subprocess under test, so treating
	// it as staleness would refuse every run where someone edited a test —
	// which is every run.
	root := fakeSourceTree(t, built.Add(-time.Hour), built.Add(time.Hour))
	bin := fakeBinary(t, built)

	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("a newer _test.go must not count as staleness: %v", err)
	}
}

func TestCheckBinaryFreshness_WaivedByTheOptOutEnvVar(t *testing.T) {
	built := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	root := fakeSourceTree(t, built.Add(72*time.Hour), built)
	bin := fakeBinary(t, built)

	if err := checkBinaryFreshness(bin, root); err == nil {
		t.Fatal("precondition: this pair must be judged stale without the waiver")
	}
	t.Setenv(allowStaleBinaryEnv, "1")
	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("%s did not waive the refusal: %v", allowStaleBinaryEnv, err)
	}
}

func TestCheckBinaryFreshness_HasNoOpinionOnATreeWithNoCompiledSource(t *testing.T) {
	// A checkout with none of binaryFreshnessDirs (or one this process cannot
	// read) must degrade to silence, not fail suites for a reason unrelated to
	// what they test.
	bin := fakeBinary(t, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := checkBinaryFreshness(bin, t.TempDir()); err != nil {
		t.Fatalf("empty source tree must yield no verdict: %v", err)
	}
}

func TestFindAppBinary_AppliesTheFreshnessVerdictToWhateverItResolved(t *testing.T) {
	// End-to-end through the real resolution path against the REAL repo root,
	// so a future refactor that resolves a binary without consulting
	// checkBinaryFreshness fails here. CTXLOOM_BINARY is resolveAppBinary's
	// first branch, which makes the binary under judgement deterministic.
	env := &TestEnvironment{}

	t.Run("stale", func(t *testing.T) {
		t.Setenv("CTXLOOM_BINARY", fakeBinary(t, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)))
		if got, err := env.findAppBinary(); err == nil {
			t.Fatalf("findAppBinary returned %q for a binary from 1990", got)
		}
	})

	t.Run("fresh", func(t *testing.T) {
		bin := fakeBinary(t, time.Now().Add(time.Hour))
		t.Setenv("CTXLOOM_BINARY", bin)
		got, err := env.findAppBinary()
		if err != nil {
			t.Fatalf("findAppBinary refused a binary newer than every source file: %v", err)
		}
		if got != bin {
			t.Fatalf("findAppBinary returned %q, want %q", got, bin)
		}
	})
}

// ── binary resolution ─────────────────────────────────────────────────────
//
// The binary under test is the one built from THIS checkout, or one named
// explicitly through appBinaryEnv — never an installed copy. The defect these
// pin: the harness used to fall back to $GOPATH/bin, $HOME/go/bin,
// $HOME/.local/bin and PATH, so a tree with no build silently tested whatever
// ctxloom happened to be installed (a stale 0.6.4 leaked into ~/.local/bin by
// another tool's tests would have become the binary under test).

// plantInstalledDecoys puts an executable ctxloom in every place an installed
// copy could live — a fake HOME's ~/.local/bin and ~/go/bin, a fake GOPATH's
// bin, and a PATH entry — and points the process at them.
func plantInstalledDecoys(t *testing.T) []string {
	t.Helper()
	home, gopath, pathDir := t.TempDir(), t.TempDir(), t.TempDir()
	var decoys []string
	for _, dir := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(gopath, "bin"),
		pathDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		bin := filepath.Join(dir, "ctxloom")
		if err := os.WriteFile(bin, []byte("#!/bin/false\n"), 0o755); err != nil {
			t.Fatalf("write decoy: %v", err)
		}
		decoys = append(decoys, bin)
	}
	t.Setenv("HOME", home)
	t.Setenv("GOPATH", gopath)
	t.Setenv("PATH", pathDir)
	t.Setenv(appBinaryEnv, "")
	return decoys
}

func TestResolveAppBinaryIn_NeverFallsBackToAnInstalledCopy(t *testing.T) {
	decoys := plantInstalledDecoys(t)
	root := t.TempDir() // a checkout with no build in it

	got, err := resolveAppBinaryIn(root)
	if err == nil {
		t.Fatalf("resolved %q for a checkout with no build; installed copies must never stand in for the tree's own", got)
	}
	if !errors.Is(err, errAppBinaryNotFound) {
		t.Fatalf("error is not errAppBinaryNotFound: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{treeBinaryPath(root), appBinaryEnv, "just build"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not name %q; got:\n%s", want, msg)
		}
	}
	for _, d := range decoys {
		if got == d {
			t.Errorf("resolved the installed decoy %s", d)
		}
	}
}

func TestResolveAppBinaryIn_UsesTheTreeBuildOverInstalledCopies(t *testing.T) {
	plantInstalledDecoys(t)
	root := t.TempDir()
	want := treeBinaryPath(root)
	if err := os.WriteFile(want, []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatalf("write tree build: %v", err)
	}

	got, err := resolveAppBinaryIn(root)
	if err != nil {
		t.Fatalf("refused the tree's own build: %v", err)
	}
	if got != want {
		t.Fatalf("resolved %q, want the tree build %q", got, want)
	}
}

func TestResolveAppBinaryIn_AnExplicitBinaryThatIsMissingFailsRatherThanFallingBack(t *testing.T) {
	plantInstalledDecoys(t)
	root := t.TempDir()
	if err := os.WriteFile(treeBinaryPath(root), []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatalf("write tree build: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "ctxloom")
	t.Setenv(appBinaryEnv, missing)

	got, err := resolveAppBinaryIn(root)
	if err == nil {
		t.Fatalf("resolved %q though %s named a binary that does not exist; an explicit choice must not be silently swapped", got, appBinaryEnv)
	}
	if !errors.Is(err, errAppBinaryNotFound) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("want errAppBinaryNotFound naming %s, got: %v", missing, err)
	}
}

func TestResolveAppBinaryIn_AnExplicitBinaryWins(t *testing.T) {
	plantInstalledDecoys(t)
	root := t.TempDir()
	if err := os.WriteFile(treeBinaryPath(root), []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatalf("write tree build: %v", err)
	}
	explicit := fakeBinary(t, time.Now())
	t.Setenv(appBinaryEnv, explicit)

	got, err := resolveAppBinaryIn(root)
	if err != nil || got != explicit {
		t.Fatalf("resolved (%q, %v), want the explicit %q", got, err, explicit)
	}
}

// ── ambient-session scrub ─────────────────────────────────────────────────

// childEnvValue returns the value env carries for key, and whether it carries
// one at all.
func childEnvValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v, true
		}
	}
	return "", false
}

// TestIsolatedEnv_ScrubsAmbientButKeepsWhatTheScenarioSet pins both halves of
// the scrub. A scrubbed key the HOST exported must not reach the child. The
// same kind of key set on purpose through SetEnv must reach it, because a
// value the scenario chose can never be the ambient one. An API-key scenario
// that exports the key has to reach the engine with it.
func TestIsolatedEnv_ScrubsAmbientButKeepsWhatTheScenarioSet(t *testing.T) {
	const ambient, chosen = "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"
	for _, k := range []string{ambient, chosen} {
		if !scrubbedEnvKeys[k] {
			t.Fatalf("%s is not a scrubbed key, so this test proves nothing about the scrub", k)
		}
	}
	t.Setenv(ambient, "host-leak")
	t.Setenv(chosen, "host-leak")

	e := &TestEnvironment{HomeDir: t.TempDir(), originalEnv: map[string]string{}}
	t.Cleanup(func() { _ = e.Cleanup() })
	e.SetEnv(chosen, "scenario-chosen")

	env := e.isolatedEnv()
	if v, ok := childEnvValue(env, ambient); ok {
		t.Errorf("ambient %s reached the child as %q; the scrub must drop it", ambient, v)
	}
	if v, ok := childEnvValue(env, chosen); !ok || v != "scenario-chosen" {
		t.Errorf("scenario-set %s reached the child as %q (present=%v), want %q", chosen, v, ok, "scenario-chosen")
	}
}

// TestIsolatedEnv_DropsTheHostSSHAgentButKeepsAHermeticOne: the developer's
// ssh-agent must never reach a child — git-over-ssh would authenticate as the
// developer — while
// an agent a scenario stood up itself and forced through SetChildEnv must.
func TestIsolatedEnv_DropsTheHostSSHAgentButKeepsAHermeticOne(t *testing.T) {
	const hostSock, hermeticSock = "/host/agent.sock", "/scenario/agent.sock"
	t.Setenv(sshAuthSockEnv, hostSock)

	bare := &TestEnvironment{HomeDir: t.TempDir(), originalEnv: map[string]string{}}
	if v, ok := childEnvValue(bare.isolatedEnv(), sshAuthSockEnv); ok {
		t.Errorf("the host's %s reached the child as %q; the scrub must drop it", sshAuthSockEnv, v)
	}

	hermetic := &TestEnvironment{HomeDir: t.TempDir(), originalEnv: map[string]string{}}
	hermetic.SetChildEnv(sshAuthSockEnv, hermeticSock)
	if v, ok := childEnvValue(hermetic.isolatedEnv(), sshAuthSockEnv); !ok || v != hermeticSock {
		t.Errorf("the scenario's %s reached the child as %q (present=%v), want %q", sshAuthSockEnv, v, ok, hermeticSock)
	}
}

// TestIsolatedEnv_DropsTheHostZDOTDIR: zsh reads its startup files from
// ZDOTDIR rather than HOME, so an inherited one points a child's login shell
// at the DEVELOPER's rc files despite the fake HOME. ctxloom probes exactly
// that shell for PATH (shellenv.Resolve's fallback), so the developer's
// installed engines would leak into what a scenario sees as installed.
func TestIsolatedEnv_DropsTheHostZDOTDIR(t *testing.T) {
	t.Setenv(zdotdirEnv, "/home/developer")

	e := &TestEnvironment{HomeDir: t.TempDir(), originalEnv: map[string]string{}}
	if v, ok := childEnvValue(e.isolatedEnv(), zdotdirEnv); ok {
		t.Errorf("the host's %s reached the child as %q; the scrub must drop it", zdotdirEnv, v)
	}
}

// TestIsolatedEnv_DropsTheHostClaudeConfigDir: claude reads its whole config
// home — settings, MCP servers, credentials, trusted projects — from
// CLAUDE_CONFIG_DIR before HOME, and the developer's shell exports it. An
// inherited one points every claude a scenario launches, a delegated child
// included, at the DEVELOPER's real config dir despite the fake HOME. The key
// is scrubbed because testsupport.EnvKeys lists it; this pins that it reaches
// scrubbedEnvKeys, the list a child's environment is actually filtered by. A
// config home a scenario stands up itself still reaches the child.
func TestIsolatedEnv_DropsTheHostClaudeConfigDir(t *testing.T) {
	const claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"
	const hostDir, scenarioDir = "/home/developer/.claude-real", "/scenario/claude-home"
	t.Setenv(claudeConfigDirEnv, hostDir)

	bare := &TestEnvironment{HomeDir: t.TempDir(), originalEnv: map[string]string{}}
	if v, ok := childEnvValue(bare.isolatedEnv(), claudeConfigDirEnv); ok {
		t.Errorf("the host's %s reached the child as %q; the scrub must drop it", claudeConfigDirEnv, v)
	}

	chosen := &TestEnvironment{HomeDir: t.TempDir(), originalEnv: map[string]string{}}
	t.Cleanup(func() { _ = chosen.Cleanup() })
	chosen.SetEnv(claudeConfigDirEnv, scenarioDir)
	if v, ok := childEnvValue(chosen.isolatedEnv(), claudeConfigDirEnv); !ok || v != scenarioDir {
		t.Errorf("the scenario's %s reached the child as %q (present=%v), want %q", claudeConfigDirEnv, v, ok, scenarioDir)
	}
}

// NonTTYFormat reads back the non_tty_format this environment hands its
// children, json when it hands none: the harness's notion of the derived
// default and the child's are the same value.
func TestTestEnvironment_NonTTYFormat_ReadsTheChildEnv(t *testing.T) {
	var e TestEnvironment
	if got := e.NonTTYFormat(); got != clifmt.FormatJSON {
		t.Fatalf("unset: %q, want json", got)
	}
	e.SetChildEnv("CTXLOOM_CONFIG_NON_TTY_FORMAT", "text")
	if got := e.NonTTYFormat(); got != clifmt.FormatText {
		t.Fatalf("set to text: %q", got)
	}
}

// TestNewTestEnvironment_RefusesAProcessHoldingSessionVariables: a TestMain
// that never scrubbed hands the host session's variables to in-process code
// and to any child started with a nil Env; the environment refuses to start
// and names the variable rather than letting that go unnoticed.
func TestNewTestEnvironment_RefusesAProcessHoldingSessionVariables(t *testing.T) {
	t.Setenv("CTXLOOM_DIAGNOSTICS_LOG", "/host/session/diagnostics.log")
	env, err := NewTestEnvironment()
	if err == nil {
		_ = env.Cleanup()
		t.Fatal("NewTestEnvironment started with a session variable in the process environment")
	}
	if !strings.Contains(err.Error(), "CTXLOOM_DIAGNOSTICS_LOG") {
		t.Errorf("the refusal must name the variable, got: %v", err)
	}
}

// TestScrubProcessEnv_ClearsEverySessionVariable is the other half: after the
// TestMain scrub the refusal has nothing to fire on.
func TestScrubProcessEnv_ClearsEverySessionVariable(t *testing.T) {
	t.Setenv("CTXLOOM_DIAGNOSTICS_LOG", "/host/session/diagnostics.log")
	ScrubProcessEnv()
	if err := refuseAmbientSessionEnv(os.LookupEnv); err != nil {
		t.Errorf("after ScrubProcessEnv: %v", err)
	}
}
