// Package testsupport provides shared isolation helpers so tests never read or
// write host or session state — neither the process environment, the user's
// ~/.ctxloom home (resolved via os.UserHomeDir), nor the working directory.
//
// Every test that exercises code reading those should route through Isolate or
// ProjectDir rather than reimplementing the isolation. A test inheriting the
// ambient session's environment is non-deterministic: e.g. CTXLOOM_PROJECT_ID
// selects the live task log, so an un-isolated task test reads the running
// session's tasks instead of its own.
package testsupport

import (
	"os"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// EnvKeys is the canonical set of host/session environment variables ctxloom
// production code reads. Isolate clears each so a test inherits none of the
// ambient session's values. TestEnvKeysCoversProductionReads enforces that every
// CTXLOOM_* variable read in production appears here.
var EnvKeys = []string{
	"CTXLOOM_SESSION_HARP",
	"CTXLOOM_PROJECT_ID",
	"CTXLOOM_RESUMED_FROM",
	"CTXLOOM_RESUMED_PARTS",
	"CTXLOOM_ROOT",
	"CTXLOOM_DEBUG_HTTP",
	"CTXLOOM_DEGRADED",
	"CTXLOOM_VERBOSE",
	"CTXLOOM_NO_COMPANIONS",
	// The agentcoord runner-handshake vars: read via the coord.Env* constants
	// (os.Getenv(coord.EnvMCPSocket), not a literal "CTXLOOM_..." string), so
	// TestEnvKeysCoversProductionReads' literal-string regex can't discover
	// them itself — they must be listed here by hand. Confirmed missing
	// 2026-07-13: an ambient CTXLOOM_MCP_SOCKET (present whenever the test
	// suite runs inside a live ctxloom-coordinated session) silently flips
	// `ctxloom mcp serve` into forward-mode, proxying every acceptance-suite
	// MCP call to the REAL coordinator instead of the isolated test project.
	"CTXLOOM_MCP_SOCKET",
	"CTXLOOM_COORD_URL",
	"CTXLOOM_COORD_CRED",
	"CTXLOOM_RUN_ID",
	"CTXLOOM_CELL_WORKDIR",
	// coord.EnvAgentCoordinator, read via os.Getenv(coord.EnvAgentCoordinator)
	// in internal/cli/llm_runner_common.go — same const-read shape as the
	// quintet above. Found by U142-F01's widened
	// TestFindUncoveredEnvReads_CatchesConstantIdentifierReads sweep, which
	// now resolves identifier reads back to their declaring constant instead
	// of requiring a literal "CTXLOOM_..." string.
	"CTXLOOM_AGENT_COORDINATOR",
	// internal/lm/isolation/traceprobe.go's probeTraceEnv const, read via
	// os.Getenv(probeTraceEnv) — same shape, same discovery.
	"CTXLOOM_ISOLATION_PROBE_TRACE_DIR",
	// The container launch-retry budget's operator overrides (lunar-boat
	// item 1): read via the coord.EnvLaunch* constants
	// (os.LookupEnv(EnvLaunchMaxAttempts), not a literal "CTXLOOM_..."
	// string), same reason as the trio above — listed here by hand.
	"CTXLOOM_LAUNCH_MAX_ATTEMPTS",
	"CTXLOOM_LAUNCH_BACKOFF_BASE",
	"CTXLOOM_LAUNCH_BACKOFF_MAX",
	// Not production state: a CI-only knob read by
	// internal/testsupport/dockergate to turn "docker unreachable" from a
	// skip into a failure. Listed because TestEnvKeysCoversProductionReads
	// scans every non-_test.go file under internal/ and cmd/, dockergate.go
	// included, and an exception carved for one file is how the next real
	// variable goes missing. Clearing it here is harmless: dockergate reads
	// it once at package init, precisely so a test that isolates before it
	// gates cannot silently demote itself back to skipping.
	"CTXLOOM_REQUIRE_DOCKER",
	"GITHUB_TOKEN",
	"GH_TOKEN",
	"CODEX_HOME",
	"EDITOR",
	"VISUAL",
	"PAGER",
}

// Isolate roots HOME at a fresh temp dir and clears every EnvKeys variable for
// the duration of the test, returning the temp home. Because it uses t.Setenv
// (which restores prior values on cleanup and rejects t.Parallel), the calling
// test must not be parallel.
func Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home, for os.UserHomeDir parity
	for _, k := range EnvKeys {
		t.Setenv(k, "")
	}
	// taskstest.ResetProcessOverrides clears confload's process-wide env/CLI
	// override capture (internal/config.SetOverrides wraps it;
	// internal/cli/root.go's PersistentPreRun installs it once per real
	// invocation) — state that outlives any single t.Setenv-scoped var and is
	// shared across every test in this binary. Delegated to taskstest (not
	// called via internal/config.ResetOverrides) so internal/config's own
	// in-package test files — which import this package — don't form an
	// import cycle; see taskstest.ResetProcessOverrides' doc.
	taskstest.ResetProcessOverrides(t)
	return home
}

// ProjectDir isolates the environment (see Isolate) and switches the working
// directory to a fresh temp dir, restoring the original cwd on cleanup. It
// returns the project directory.
func ProjectDir(t *testing.T) string {
	t.Helper()
	Isolate(t)
	dir := t.TempDir()
	ChangeDir(t, dir)
	return dir
}

// ChangeDir switches the working directory to dir for the duration of the
// test, restoring the original on cleanup. It is ProjectDir's os.Chdir
// wrapper, exposed directly for callers that need to chdir into a directory
// they built themselves (e.g. a git-worktree fixture) rather than the fresh
// temp dir ProjectDir would mint. It does not isolate the environment — call
// Isolate (or ProjectDir, for a fresh dir) alongside it when a test needs
// that too. golangci-lint's forbidigo rule forbids os.Chdir directly in test
// files precisely so callers route through here instead.
//
// The body lives in taskstest: the internal/shared tree is self-contained
// (never imports non-shared internal packages, so it can split back out to
// the companion module), which forces the canonical helper shared-side;
// this side delegates rather than duplicating it (reprise).
func ChangeDir(t *testing.T, dir string) {
	t.Helper()
	taskstest.ChangeDir(t, dir)
}

// ScrubbedEnv returns an environment slice for a subprocess spawned by a test:
// the current environment with HOME (and USERPROFILE) rooted at a fresh temp dir
// and every EnvKeys variable removed, so the child does not inherit the host or
// session HOME/env. Use it as exec.Cmd.Env — it is the subprocess analog of
// Isolate, which only governs the in-process environment.
func ScrubbedEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	drop := map[string]bool{"HOME": true, "USERPROFILE": true}
	for _, k := range EnvKeys {
		drop[k] = true
	}
	env := []string{"HOME=" + home, "USERPROFILE=" + home}
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		env = append(env, kv)
	}
	return env
}
