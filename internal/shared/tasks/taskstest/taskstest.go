// Package taskstest provides shared isolation helpers so tests never read or
// write host or session state — neither the process environment, the user's
// ~/.ctxloom home (resolved via os.UserHomeDir), nor the working directory.
//
// Despite the package name, this is GENERAL-purpose test isolation, not
// task-store-specific: internal/testsupport.Isolate/ChangeDir delegate here
// (it must stay import-cycle-free of testsupport, so the shared tree owns
// the canonical body — see EnvKeys and ChangeDir's docs), and well over 50
// call sites across the repo use it directly for exactly that reason.
// Isolate clears every variable in EnvKeys, the full list
// production code reads — not merely what the task store itself touches.
//
// A test inheriting the ambient session's environment is non-deterministic:
// e.g. CTXLOOM_PROJECT_ID selects the live task log, so an un-isolated task
// test reads the running session's tasks instead of its own.
package taskstest

import (
	"os"
	"testing"
)

// EnvKeys is the canonical, complete set of host/session environment
// variables ctxloom (and taskloom) production code reads. Isolate clears
// each so a test inherits none of the ambient session's values.
// internal/testsupport.EnvKeys is this exact slice (not a copy) — taskstest's
// own copy once drifted to covering only 3 of the ~18 variables
// testsupport's covered, with no guard test to catch the drift: any of the
// 52+ callers of taskstest.Isolate (this package is used as GENERAL-purpose
// test isolation well beyond "the task store") believed
// itself isolated and was not. internal/testsupport.TestEnvKeysCoversProductionReads
// enforces that every CTXLOOM_* variable read in production appears here;
// since testsupport.EnvKeys is this slice, that guard now covers both names.
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
	// Read by cmd/validate to override the build stamp. Ambient in any shell
	// that exported it, and an unisolated test would then validate against the
	// HOST's stamp instead of its own fixture's.
	"CTXLOOM_VERSION_STAMP",
	// The reach-back trio: read via the sessions.Env* constants, not a
	// literal "CTXLOOM_..." string, so TestEnvKeysCoversProductionReads'
	// literal-string regex can't discover them itself — they must be listed
	// here by hand. An ambient trio (present whenever the test suite runs
	// inside a live ctxloom-coordinated session) would otherwise point an
	// isolated test's runner at the REAL coordinator.
	"CTXLOOM_COORD_URL",
	"CTXLOOM_COORD_CRED",
	"CTXLOOM_RUN_ID",
	// internal/adapters/isolation/traceprobe.go's probeTraceEnv const, read via
	// os.Getenv(probeTraceEnv) — same shape, same discovery.
	"CTXLOOM_ISOLATION_PROBE_TRACE_DIR",
	// The container launch-retry budget's operator overrides: read via the
	// coord.EnvLaunch* constants (os.LookupEnv(EnvLaunchMaxAttempts), not a
	// literal "CTXLOOM_..." string), same reason as the trio above — listed
	// here by hand.
	"CTXLOOM_LAUNCH_MAX_ATTEMPTS",
	"CTXLOOM_LAUNCH_BACKOFF_BASE",
	"CTXLOOM_LAUNCH_BACKOFF_MAX",
	// sessions.EnvRunnerOwnerLossWindow, read by runner.Main through the
	// constant and forwarded by spawn.StartRunner — an ambient value would
	// change how long every test's runner outlives its coordinator.
	"CTXLOOM_RUNNER_OWNER_LOSS_WINDOW",
	// procsec.EnvAllowProcessInspection, read at the top of main() to skip
	// same-uid /proc hardening for debugging. An ambient value would leave every
	// test process's environ peer-readable, so a test asserting the hardened
	// state would pass or fail on the developer's shell rather than the code.
	"CTXLOOM_ALLOW_PROCESS_INSPECTION",
	// Not production state: a CI-only knob read by
	// internal/testsupport/dockergate to turn "docker unreachable" from a
	// skip into a failure. Listed because TestEnvKeysCoversProductionReads
	// scans every non-_test.go file under internal/ and cmd/, dockergate.go
	// included, and an exception carved for one file is how the next real
	// variable goes missing. Clearing it here is harmless: dockergate reads
	// it once at package init, precisely so a test that isolates before it
	// gates cannot silently demote itself back to skipping.
	"CTXLOOM_REQUIRE_DOCKER",
	// Also not production state, and listed for the identical reason as
	// CTXLOOM_REQUIRE_DOCKER above: testsupport/sandbox.go is a non-_test.go
	// file under internal/, so TestEnvKeysCoversProductionReads sweeps it and
	// an exception carved for one file is how the next real variable goes
	// missing. It is testsupport.SandboxOffEnv, read ONCE in TestMain (before
	// any test, and therefore before any Isolate) by the self-test that proves
	// the sandbox guard can go red — clearing it here is harmless.
	"CTXLOOM_TEST_SANDBOX_OFF",
	// The container cell's two knobs (internal/testsupport/containercell,
	// internal/testsupport/dockergate), listed for the same reason as
	// CTXLOOM_REQUIRE_DOCKER above rather than because either is production
	// state. CTXLOOM_REQUIRE_RUNTIMES names the runtimes a CI lane claims to
	// cover, so an unreachable one FAILS instead of skipping green;
	// CTXLOOM_CELL_BINARY points the cell at a prebuilt static ctxloom. Both
	// are read ONCE at their package's init, precisely so clearing them here
	// cannot demote a lane's declared coverage back to a skip.
	"CTXLOOM_REQUIRE_RUNTIMES",
	"CTXLOOM_CELL_BINARY",
	// Same again for gitfixture.go's EnvAllowMissingGit, a non-_test.go file
	// under internal/ that the same sweep reaches. It is read ONCE at package
	// init for the identical reason dockergate reads its knob there: a fixture
	// consulted after a test has isolated would otherwise find it cleared and
	// silently demote a required git back to a skip.
	"CTXLOOM_ALLOW_MISSING_GIT",
	"GITHUB_TOKEN",
	"GH_TOKEN",
	// The mount-namespace shim's own protocol. mountns re-execs THIS BINARY
	// with these set, so a test that inherited one from an ambient environment
	// would take the shim path on startup instead of running the test —
	// silently, and only on a machine where they happened to be exported.
	"CTXLOOM_MOUNTNS_SHIM",
	"CTXLOOM_MOUNTNS_BINDS",
	"CTXLOOM_MOUNTNS_ARGV",
	"CTXLOOM_MOUNTNS_PROBING",
	"CTXLOOM_MOUNTNS_PROBE_WRITE",
	"CTXLOOM_MOUNTNS_READBACK",
	"EDITOR",
	"VISUAL",
	"PAGER",
	// Engine config-home and credential vars, which a developer running tests
	// inside a live engine session has set and a CI box does not: a test once
	// passed locally only because CLAUDE_CONFIG_DIR leaked in. The shared tree
	// cannot import the engines that declare these, so they are listed by
	// hand; internal/engines.TestEnvKeys_CoverEveryEngineDeclaredVar derives
	// every var the shipped engines declare and fails on any missing here.
	"CLAUDE_CONFIG_DIR",
	"CLAUDE_SECURESTORAGE_CONFIG_DIR",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
}

// Isolate roots HOME at a fresh temp dir and unsets every EnvKeys variable for
// the duration of the test, returning the temp home. Because it uses t.Setenv
// (which restores prior values on cleanup and rejects t.Parallel), the calling
// test must not be parallel.
//
// It then REFUSES to continue if the environment it just installed still lets
// ctxloom's app-directory resolution reach outside the OS temp root — see
// requireIsolatedAppDir for what that means and why rooting HOME alone does
// not achieve it. Isolate covering only half the resolution, silently, is how
// a test run rewrote a developer's real global config.
func Isolate(t *testing.T) string {
	t.Helper()
	home := isolateEnv(t)
	requireIsolatedAppDir(t, callerPackage())
	runIsolateHooks(t)
	return home
}

// isolateHooks run at every Isolate and ChangeDir, and again at the test's
// cleanup: a package whose process-wide state is keyed to the environment
// or the working directory (the CLI's composition, opened from cwd)
// registers its reset here, so a test that re-roots the process never
// inherits the composition a previous test opened.
var isolateHooks []func()

// RegisterIsolateHook adds fn to the hooks Isolate and ChangeDir run.
func RegisterIsolateHook(fn func()) { isolateHooks = append(isolateHooks, fn) }

func runIsolateHooks(t *testing.T) {
	t.Helper()
	for _, fn := range isolateHooks {
		fn()
	}
	t.Cleanup(func() {
		for _, fn := range isolateHooks {
			fn()
		}
	})
}

// isolateEnv is Isolate without the isolation CHECK: it installs the temp
// HOME and clears the environment, and reports nothing.
//
// It is split out for an ordering reason, not as a way to opt out. ProjectDir
// isolates the environment BEFORE it changes the working directory, and the
// check reads the working directory — so a check inside this body would fire
// on every ProjectDir caller for a cwd ProjectDir is about to replace. Both
// exported helpers run the check; they differ only in when.
func isolateEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows home, for os.UserHomeDir parity
	// UNSET, not set-empty: a CI box has these absent, and for several of
	// them absent and empty mean different things (an empty
	// CLAUDE_SECURESTORAGE_CONFIG_DIR is HOME/.claude; an absent one falls
	// back to the config dir). t.Setenv first registers the restore and the
	// no-parallel guard; the Unsetenv then removes the var for the test.
	for _, k := range EnvKeys {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
	return home
}

// ProjectDir isolates the environment (see Isolate) and switches the working
// directory to a fresh temp dir, restoring the original cwd on cleanup. It
// returns the project directory.
//
// The isolation check runs AFTER the chdir, not before: the fresh temp dir is
// precisely what makes the working-directory route safe here, so checking
// first would fail on a cwd this function is about to discard.
func ProjectDir(t *testing.T) string {
	t.Helper()
	isolateEnv(t)
	dir := t.TempDir()
	ChangeDir(t, dir)
	requireIsolatedAppDir(t, callerPackage())
	return dir
}

// ChangeDir switches the working directory to dir for the duration of the
// test, restoring the original on cleanup. It is ProjectDir's os.Chdir
// wrapper, exposed directly for callers that need to chdir into a directory
// they built themselves rather than the fresh temp dir ProjectDir would mint.
// It does not isolate the environment — call Isolate (or ProjectDir, for a
// fresh dir) alongside it when a test needs that too. This is the CANONICAL
// body: internal/testsupport.ChangeDir delegates here, because the shared
// tree must stay self-contained (it cannot import testsupport) while
// testsupport may import shared — one body, no duplicate (reprise).
func ChangeDir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("taskstest: getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("taskstest: chdir: %v", err)
	}
	t.Cleanup(func() { restoreDir(t, orig, dir) })
	runIsolateHooks(t)
}

// errorReporter is the one method restoreDir needs from *testing.T. It exists
// so the failure branch below is reachable from a test: with the reporter
// hardwired to t, a cleanup that reports can only be observed by failing the
// very test that would assert it.
type errorReporter interface {
	Errorf(format string, args ...any)
}

// restoreDir returns the process to orig, reporting rather than discarding a
// failure.
//
// A failed restore is not one test's problem: the working directory is
// process-global, so every LATER test in the binary runs from a directory it
// never chose — typically one another cleanup has just removed. Those failures
// land arbitrarily far away and read as unrelated, while the one test that
// could have named the cause stayed green. dir is named in the message because
// it is where the process is actually left standing.
func restoreDir(rep errorReporter, orig, dir string) {
	if err := os.Chdir(orig); err != nil {
		rep.Errorf("taskstest: restoring cwd to %s failed, leaving every later test in this "+
			"binary running from %s: %v", orig, dir, err)
	}
}
