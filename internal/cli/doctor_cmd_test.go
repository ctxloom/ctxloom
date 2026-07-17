package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
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
// operations.InitializeProject — the same call `ctxloom manage install`
// makes — under a fresh temp dir, and loads it back with config.Load. The
// scaffolded agent ("default") binds one profile ("default", the embedded
// seed profile InitializeProject writes) to the given engine label.
func setupProject(t *testing.T, engine string) (root string, cfg *config.Config) {
	t.Helper()
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

// --- DOCTOR-CHECK-SETUP-MARKER-e5 ---

func TestDoctorCheckSetupMarker_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupMarker(cfg, nil)
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Detail, cfg.AppPaths[0])
}

func TestDoctorCheckSetupMarker_WrongState_NoMarkerDir(t *testing.T) {
	check := doctorCheckSetupMarker(&config.Config{}, nil)
	assert.Equal(t, "warn", check.Status, "an empty AppPaths must fail loud, not silently pass")
	assert.Contains(t, check.Detail, "no .ctxloom marker directory found")
}

func TestDoctorCheckSetupMarker_WrongState_ConfigLoadError(t *testing.T) {
	check := doctorCheckSetupMarker(nil, assert.AnError)
	assert.Equal(t, "warn", check.Status)
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
	if check.Status == "ok" {
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
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Detail, "git", "a missing git must be named, not silently absorbed into a generic failure")
}

func TestDoctorDepBinaries_IncludesGit(t *testing.T) {
	assert.Contains(t, doctorDepBinaries, "git", "worktree isolation and remote pull hard-depend on git")
}

// --- DOCTOR-CHECK-AGENTS-b2: promoted to WARN on an empty roster ---

func TestDoctorCheckAgents_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckAgents(context.Background(), cfg, nil)
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Detail, "default")
}

func TestDoctorCheckAgents_WrongState_EmptyRoster(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg.Agents = map[string]agents.Agent{}

	check := doctorCheckAgents(context.Background(), cfg, nil)
	assert.Equal(t, "warn", check.Status, "an empty roster is an incomplete setup postcondition, not a neutral fact")
	assert.Contains(t, check.Detail, "no agents configured")
}

func TestDoctorCheckAgents_WrongState_UnresolvableProfile(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg.Agents = map[string]agents.Agent{
		"broken": {Name: "broken", Engine: "claude-code", Profiles: []string{"does-not-exist"}},
	}
	check := doctorCheckAgents(context.Background(), cfg, nil)
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Detail, "broken")
}

// --- DOCTOR-CHECK-SETUP-DEPS-h8 (lockfile + context assembly) ---

func TestDoctorCheckSetupLockAndAssembly_RightState(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Detail, "lockfile: 0 entries parse cleanly")
	assert.Contains(t, check.Detail, "context assembly: succeeds")
}

func TestDoctorCheckSetupLockAndAssembly_WrongState_CorruptLockfile(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	lockPath := filepath.Join(cfg.AppPaths[0], "lock.yaml")
	require.NoError(t, os.WriteFile(lockPath, []byte("not: [valid: yaml: at: all"), 0644))

	check := doctorCheckSetupLockAndAssembly(context.Background(), cfg, nil)
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Detail, "lockfile:")
}

// --- DOCTOR-CHECK-HOOKS-TRUST-d4: hooks AND MCP registration per backend ---

func TestDoctorCheckHooksTrust_RightState(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	_, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)
	t.Chdir(root) // HarnessStatus's default WorkDir path resolves off cwd

	check := doctorCheckHooksTrust(context.Background(), cfg, nil)
	assert.Equal(t, "ok", check.Status)
	assert.Contains(t, check.Detail, "hooks/MCP registered for: claude-code")
}

func TestDoctorCheckHooksTrust_WrongState_NotInstalled(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	t.Chdir(root) // no ApplyHooks call: hooks were never installed

	check := doctorCheckHooksTrust(context.Background(), cfg, nil)
	assert.Equal(t, "warn", check.Status)
	assert.Contains(t, check.Detail, "NOT registered")
	assert.Contains(t, check.Detail, "claude-code")
}

func TestDoctorCheckHooksTrust_NoEnginesConfigured(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	cfg.Agents = map[string]agents.Agent{}
	check := doctorCheckHooksTrust(context.Background(), cfg, nil)
	assert.Equal(t, "ok", check.Status, "nothing configured to check hooks for is not itself a failure")
	assert.Contains(t, check.Detail, "no engine is configured to check")
}

// --- DOCTOR-CHECK-SETUP-COMPANIONS-i9 / AUTHPING-j0: reporting-only ---

func TestDoctorCheckSetupCompanions_NeverWarns(t *testing.T) {
	_, cfg := setupProject(t, "claude-code")
	check := doctorCheckSetupCompanions(cfg, nil)
	assert.NotEqual(t, "warn", check.Status, "companions are optional add-ons, never a doctor failure")
}

func TestDoctorCheckSetupAuthPing_AlwaysInfoAndNamesTheGap(t *testing.T) {
	check := doctorCheckSetupAuthPing()
	assert.Equal(t, "info", check.Status)
	assert.Contains(t, check.Detail, "no auth-ping surface")
}

// --- full command wiring: JSON shape, back-compat "always exits 0", read-only ---

// runDoctor executes the real doctorCmd (not a hand-rolled reimplementation)
// with the given args, in root, returning its stdout and any RunE error.
// doctorCmd's OWN FlagSet (currently just --deps) is added by reference, so
// --deps here binds the SAME doctorDepsOnlyFlag var doctorCmd.RunE reads;
// t.Cleanup resets it so one test's --deps never bleeds into the next.
func runDoctor(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(root)
	t.Cleanup(func() { doctorDepsOnlyFlag = false })
	buf := &bytes.Buffer{}
	c := &cobra.Command{Use: "doctor", RunE: doctorCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	c.Flags().AddFlagSet(doctorCmd.Flags())
	c.Flags().String("format", formatText, "")
	c.Flags().Bool("degraded", false, "")
	c.Flags().Bool("no-companions", false, "")
	c.SetOut(buf)
	c.SetContext(context.Background())
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
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
	_, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)

	out, err := runDoctor(t, root)
	require.NoError(t, err)
	assert.Contains(t, out, "DOCTOR-CHECK-SETUP-MARKER-e5 [ok]")
	assert.Contains(t, out, "DOCTOR-CHECK-HOOKS-TRUST-d4 [ok]")
	assert.NotContains(t, out, "[warn]", "a fully-wired project must show no warn lines")
}

// TestDoctorCmd_DepsFlag_ScopesToDepsAlone proves `ctxloom doctor --deps`
// runs ONLY DOCTOR-CHECK-DEPS-a1: on a project with an empty agent roster
// (which unscoped `doctor` reports as a WARN — see
// TestDoctorCheckAgents_WrongState_EmptyRoster), the scoped invocation must
// show none of that noise, matching what init's PRIME/setup skill's phase 1
// need — a clean machine-capability check before anything is configured yet.
func TestDoctorCmd_DepsFlag_ScopesToDepsAlone(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	cfg.Agents = map[string]agents.Agent{} // would otherwise WARN unscoped

	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)

	lines := 0
	for _, line := range bytes.Split([]byte(out), []byte("\n")) {
		if bytes.Contains(line, []byte("DOCTOR-CHECK-")) {
			lines++
		}
	}
	assert.Equal(t, 1, lines, "--deps must emit exactly one check line")
	assert.Contains(t, out, "DOCTOR-CHECK-DEPS-a1")
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
	assert.NotContains(t, out, "DOCTOR-CHECK-SETUP-MARKER-e5")
}

func TestDoctorCmd_DepsFlag_JSONShapeIsSingleCheck(t *testing.T) {
	root := t.TempDir()
	out, err := runDoctor(t, root, "--deps", "--format", "json")
	require.NoError(t, err)
	var report DoctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	require.Len(t, report.Checks, 1)
	assert.Equal(t, "DOCTOR-CHECK-DEPS-a1", report.Checks[0].Marker)
}

func TestDoctorCmd_JSONShape(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	_, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend: "claude-code", WorkDir: root,
	})
	require.NoError(t, err)

	out, err := runDoctor(t, root, "--format", "json")
	require.NoError(t, err)

	var report DoctorReport
	require.NoError(t, json.Unmarshal([]byte(out), &report), "output must be valid JSON matching DoctorReport")
	require.NotEmpty(t, report.Checks)

	markers := make([]string, 0, len(report.Checks))
	for _, c := range report.Checks {
		require.NotEmpty(t, c.Marker)
		require.Contains(t, []string{"ok", "warn", "info"}, c.Status)
		markers = append(markers, c.Marker)
	}
	sort.Strings(markers)
	for _, want := range []string{
		"DOCTOR-CHECK-SETUP-MARKER-e5",
		"DOCTOR-CHECK-DEPS-a1",
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
	_, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
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
