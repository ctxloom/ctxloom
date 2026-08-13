package testenv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
)

// mcpStdinGrace is the CEILING RunWithStdin will keep stdin open after
// writing the request(s), for a child that never answers them. The MCP
// server (SDK stdio transport) dispatches requests asynchronously; if stdin
// closes immediately after the write, its read loop hits EOF and tears the
// server down before the handler writes its response. A real MCP client keeps
// stdin open until it has its responses — this models that.
//
// It is a ceiling and nothing else: RunWithStdin returns the moment every
// request it wrote has been ANSWERED (waitForStdinResponses), so raising it
// costs a passing test nothing. It buys the case that matters — a child whose
// second answer is genuinely slow, e.g. assemble_context on a box where every
// other package's tests are running — the room to finish rather than being
// cut off mid-work.
const mcpStdinGrace = 20 * time.Second

// stdinPollInterval is how often waitForStdinResponses checks the child's
// accumulated output.
const stdinPollInterval = 10 * time.Millisecond

// TestEnvironment manages isolated test environments with fake home and project directories.
type TestEnvironment struct {
	// Root temporary directory containing all test artifacts
	Root string

	// HomeDir is the fake home directory (~)
	HomeDir string

	// ProjectDir is the fake project directory (a git repo)
	ProjectDir string

	// AppBinary is the path to the ctxloom binary to test
	AppBinary string

	// originalEnv stores original environment variables for restoration
	originalEnv map[string]string

	// childEnv holds variables forced onto every spawned ctxloom process
	// AFTER the ambient-session scrub. See SetChildEnv for why SetEnv cannot
	// serve this purpose.
	childEnv map[string]string

	// runs is the ordered history of every CLI invocation (Run / RunWithStdin),
	// oldest first — replacing a single mutable lastOutput/lastError/
	// lastExitCode slot. A single slot forced any scenario that
	// ran two commands to lose the first's output the moment the second
	// ran; five acceptance journeys had invented private snapshot fields to
	// work around exactly that. LastOutput/LastExitCode/LastError read the
	// newest entry; NthLastOutput(n) reaches back further.
	runs []RunRecord
}

// RunRecord captures everything TestEnvironment observed about one CLI
// invocation: the argv, its combined stdout+stderr, exit code, and any error.
type RunRecord struct {
	Args   []string
	Output string // combined stdout+stderr
	// Stdout is the MACHINE stream on its own. A `--format json` assertion
	// that parses Output cannot work: any stderr line the command also
	// emitted (a companion advisory, a withheld-content notice) is
	// concatenated onto the JSON and the parse fails, which pushed those
	// scenarios back onto substring matching — the exact weakness that let a
	// json-flagged command pass while rendering human text.
	Stdout   string
	ExitCode int
	Err      error
}

// recordRun derives a RunRecord's ExitCode from err (the same classification
// Run/RunWithStdin used to do inline: 0 on success, -1 for a non-ExitError
// failure, else the process's real exit code) and appends it to runs.
func (e *TestEnvironment) recordRun(args []string, output string, err error) {
	rec := RunRecord{
		Args:   append([]string(nil), args...),
		Output: output,
		Err:    err,
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		rec.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		rec.ExitCode = -1
	} else {
		rec.ExitCode = 0
	}
	e.runs = append(e.runs, rec)
}

// recordRunSplit records a run whose stdout and stderr were captured
// separately, keeping both the concatenated view every existing assertion
// reads and the machine stream on its own (see RunRecord.Stdout).
//
// This is the single funnel both Run and RunWithStdin pass through AFTER
// cmd.Run()/cmd.Wait() has returned — i.e. after the process has genuinely
// been started. Deliberately not hooked into recordRun instead:
// environment_test.go calls recordRun directly with synthetic args ("first
// command") to pin RunRecord's own bookkeeping.
func (e *TestEnvironment) recordRunSplit(args []string, stdout, stderr string, err error) {
	e.recordRun(args, stdout+stderr, err)
	e.runs[len(e.runs)-1].Stdout = stdout
}

// forceRemoveAll removes dir even when it contains read-only files or
// directories. This matters here specifically because of a bug this package
// used to have (see taskloom_acceptance.go's realProcessEnv comment): a `go
// build` run with HOME pointed at a scenario's fake home populated a full
// GOMODCACHE tree under it, and the Go toolchain deliberately marks module
// cache directories 0555 to guard cached module sources against accidental
// edits. Plain os.RemoveAll cannot unlink through a read-only directory and
// returns an error — an error every current caller discards (Cleanup's
// return value is thrown away as `_ = env.Cleanup()` at every call site), so
// a scenario that ever produced such a tree left a permanent partial
// directory behind even on a normal, non-killed test run. That was the
// primary, always-reproducible mechanism behind the observed /tmp leak, not
// a killed-process edge case: every current call site already wires Cleanup
// via t.Cleanup/ctx.After, but a silently-discarded error means Cleanup runs,
// reports nothing, and removes nothing. Walking the tree first and restoring
// owner-write everywhere makes the subsequent RemoveAll unconditional, so
// Cleanup actually finishes the job even if something upstream misbehaves
// the same way again.
//
// Deliberately NOT paired with a "sweep other stale ctxloom-integration-*
// dirs on startup" step: multiple test processes legitimately run
// concurrently on this box (parallel agent worktrees, a live acceptance
// suite), each with its own live directory under the same /tmp prefix. A
// blind glob-and-remove has no reliable way to tell "orphaned by a SIGKILLed
// run" apart from "another process's directory, still very much in use" —
// getting that wrong turns a cheap, cosmetic disk leak into an
// impossible-to-reproduce cross-run flake (one suite deletes another's
// project/home mid-scenario). This forceRemoveAll fix is sufficient on its
// own: it's what makes every EXISTING Cleanup call site (which already
// fires on every current entry point) actually succeed, so no leak
// accumulates going forward without adding that cross-process risk.
func forceRemoveAll(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // let RemoveAll below surface the real error
		}
		if info, ierr := d.Info(); ierr == nil && info.Mode().Perm()&0200 == 0 {
			_ = os.Chmod(path, info.Mode().Perm()|0700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// NewTestEnvironment creates a new isolated test environment.
func NewTestEnvironment() (*TestEnvironment, error) {
	// Create root temp directory
	root, err := os.MkdirTemp("", "ctxloom-integration-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp root: %w", err)
	}

	env := &TestEnvironment{
		Root:        root,
		HomeDir:     filepath.Join(root, "home"),
		ProjectDir:  filepath.Join(root, "project"),
		originalEnv: make(map[string]string),
	}

	// Create home directory structure
	if err := os.MkdirAll(filepath.Join(env.HomeDir, ".ctxloom", "bundles"), 0755); err != nil {
		_ = env.Cleanup()
		return nil, fmt.Errorf("failed to create home .ctxloom/bundles: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(env.HomeDir, ".ctxloom", "profiles"), 0755); err != nil {
		_ = env.Cleanup()
		return nil, fmt.Errorf("failed to create home .ctxloom/profiles: %w", err)
	}

	// Create project directory
	if err := os.MkdirAll(env.ProjectDir, 0755); err != nil {
		_ = env.Cleanup()
		return nil, fmt.Errorf("failed to create project dir: %w", err)
	}

	// Find the ctxloom binary
	env.AppBinary, err = env.findAppBinary()
	if err != nil {
		_ = env.Cleanup()
		return nil, fmt.Errorf("failed to find ctxloom binary: %w", err)
	}

	return env, nil
}

// findAppBinary locates the ctxloom binary to test.
func (e *TestEnvironment) findAppBinary() (string, error) {
	// First, check if CTXLOOM_BINARY is set (for CI or custom builds)
	if bin := os.Getenv("CTXLOOM_BINARY"); bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}

	if abs, ok := firstExistingBinary(candidateBinaryPaths()); ok {
		return abs, nil
	}

	// Try PATH lookup
	if path, err := exec.LookPath("ctxloom"); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("ctxloom binary not found; set CTXLOOM_BINARY or ensure ctxloom is in PATH")
}

// candidateBinaryPaths lists the common locations the built ctxloom binary may
// live in, with a .exe suffix applied on Windows.
func candidateBinaryPaths() []string {
	locations := []string{
		// Built binary in project root (found by walking up)
		filepath.Join(findProjectRoot(), "ctxloom"),
		// Built binary in current dir
		"./ctxloom",
		// Go install location
		filepath.Join(os.Getenv("GOPATH"), "bin", "ctxloom"),
		filepath.Join(os.Getenv("HOME"), "go", "bin", "ctxloom"),
		// Local bin
		filepath.Join(os.Getenv("HOME"), ".local", "bin", "ctxloom"),
	}

	if runtime.GOOS == "windows" {
		for i, loc := range locations {
			if !strings.HasSuffix(loc, ".exe") {
				locations[i] = loc + ".exe"
			}
		}
	}
	return locations
}

// findProjectRoot walks up from the current directory looking for go.mod,
// falling back to the current directory if none is found.
func findProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		// os.Getwd's error used to be silently discarded and root
		// walked from "" -- filepath.Join("", "go.mod") happens to resolve
		// relative to the process's real cwd via the OS anyway, but
		// filepath.Dir("") is "." (not ""), so the loop's own root/parent
		// comparison behaves subtly differently than intended on this path.
		// An unreadable cwd (deleted out from under the process) is rare but
		// real; "." is the same "no discoverable root, use something
		// relative" fallback the loop's own last line already returns on a
		// genuine miss, just reached without the Stat calls that would
		// otherwise silently resolve against the wrong implicit base.
		return "."
	}
	root := cwd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			return cwd
		}
		root = parent
	}
}

// firstExistingBinary returns the absolute path of the first location that
// exists on disk.
func firstExistingBinary(locations []string) (string, bool) {
	for _, loc := range locations {
		if _, err := os.Stat(loc); err != nil {
			continue
		}
		if abs, err := filepath.Abs(loc); err == nil {
			return abs, true
		}
	}
	return "", false
}

// Setup configures the environment variables for isolated testing.
//
// The error return can never be non-nil today (storeAndSetEnv returns
// nothing) — kept deliberately as a documented future-proofing
// seam rather than dropped, since every one of Setup's five call sites
// already checks it and a future storeAndSetEnv-shaped failure (a real
// os.Setenv error, say) should be able to surface through the same path
// without a signature change rippling through all five.
func (e *TestEnvironment) Setup() error {
	// Store and override HOME
	e.storeAndSetEnv("HOME", e.HomeDir)

	// On Windows, also set USERPROFILE
	if runtime.GOOS == "windows" {
		e.storeAndSetEnv("USERPROFILE", e.HomeDir)
	}

	// Clear any existing MLCM config paths
	e.storeAndSetEnv("XDG_CONFIG_HOME", filepath.Join(e.HomeDir, ".config"))

	e.grantAmbientCompanionConsent()

	return nil
}

// grantAmbientCompanionConsent records exec consent, in this scenario's fresh
// HOME, for every companion binary that is ALREADY on the developer's PATH
// when the environment is built.
//
// It exists to remove a machine dependency, not to add one. Before exec
// consent landed, a locally-installed `ltk` or `taskloom` was simply executed
// by every ctxloom the suite spawned; several tests quietly rely on that (the
// `session-bind` hook the bundle apply tests assert on ships in
// TASKLOOM's loadout, not in an embedded bundle). Without this, the same
// binaries would instead be skipped as unconfirmed — and on a real pty, where
// the session IS interactive, ctxloom would correctly stop and ASK, hanging
// the run on a question no test answers. Granting here reproduces the previous
// behavior exactly: what is installed still decides, and CI — which installs
// none of them — is unaffected either way.
//
// It deliberately does NOT cover companions installed LATER by
// InstallFakeCompanion (that helper records its own consent) or by a scenario
// that wants to observe the refusal (steps_companion_consent.go), because it
// runs once, here, before either exists.
func (e *TestEnvironment) grantAmbientCompanionConsent() {
	for _, bin := range config.DiscoverCompanions() {
		if _, err := config.SetCompanionConsent(bin, true); err != nil {
			continue // not installed, or unhashable — nothing to grant
		}
	}
}

// storeAndSetEnv stores the original value and sets a new one. The original
// is captured FIRST-WRITE-WINS: a key already recorded in originalEnv (a
// second or third storeAndSetEnv call on the SAME key within one scenario —
// e.g. a PATH scrub followed by an InstallFakeCompanion prepend, both of
// which touch "PATH") is left alone, so Cleanup always restores the value
// that was really there before THIS scenario touched it at all, never an
// intermediate value this scenario itself produced. Capturing unconditionally
// on every call was a real bug: the second call would read back the FIRST
// call's already-modified value and record THAT as "original", so Cleanup
// restored an already-mutated value — for PATH specifically, a scenario that
// scrubbed a directory and then prepended a fake-companion directory would
// permanently lose the scrubbed directory from every LATER scenario in the
// same test process, since godog runs all scenarios in one binary.
func (e *TestEnvironment) storeAndSetEnv(key, value string) {
	if _, already := e.originalEnv[key]; !already {
		if orig, exists := os.LookupEnv(key); exists {
			e.originalEnv[key] = orig
		} else {
			e.originalEnv[key] = "\x00" // Marker for "was not set"
		}
	}
	_ = os.Setenv(key, value)
}

// SetEnv sets an environment variable on THIS PROCESS (restored by Cleanup,
// same bookkeeping Setup uses for HOME/XDG). Every subprocess this
// environment spawns (Run/RunWithStdin/RunPTY/Command) builds its env from
// os.Environ() at call time, so this is the general seam for handing a
// spawned `ctxloom` process — and anything IT in turn spawns with a nil/empty
// explicit env (e.g. the self-invoked `ctxloom llm serve <backend>` plugin
// subprocess, which inherits its parent's environment when dialLLMConnection
// is given no per-spawn env) — a variable it will see.
func (e *TestEnvironment) SetEnv(key, value string) {
	e.storeAndSetEnv(key, value)
}

// SetChildEnv forces key=value onto every ctxloom process this environment
// spawns, overriding the ambient-session scrub.
//
// SetEnv cannot do this. isolatedEnv drops every testsupport.EnvKeys variable
// — CTXLOOM_SESSION_HARP, CTXLOOM_PROJECT_ID and the rest — before handing the
// child its environment, so that a suite run from inside a real ctxloom
// session never inherits that session's identity. That scrub is correct and
// stays: it is why these tests do not silently pass by picking up the
// developer's own harp.
//
// But it also meant NO scenario could exercise anything a session variable
// gates, because the only channel those variables travel on was closed in both
// directions. The hidden hook callbacks are the clearest case — session-bind
// and stamp-plan read CTXLOOM_SESSION_HARP and do nothing without it — and
// they went uncovered for exactly this reason. This is the deliberate,
// per-scenario door through the scrub: the value is one the scenario CHOSE, so
// it can never be the ambient one.
func (e *TestEnvironment) SetChildEnv(key, value string) {
	if e.childEnv == nil {
		e.childEnv = map[string]string{}
	}
	e.childEnv[key] = value
}

// ChildEnv returns the value SetChildEnv forced for key, or "" when the
// scenario never set one.
//
// It exists so a later step can seed a fixture against the SAME identity the
// scenario handed the child, rather than repeating the literal. Repeating it
// is how a fixture and the process it is meant to describe drift apart: the
// step that changes the harp is not the step that seeds the file, and nothing
// would report the mismatch — the tool would simply answer about a session
// nobody wrote to, which is a passing "no samples" result.
func (e *TestEnvironment) ChildEnv(key string) string {
	return e.childEnv[key]
}

// Cleanup removes the test environment and restores original env vars.
func (e *TestEnvironment) Cleanup() error {
	// Restore original environment
	for key, value := range e.originalEnv {
		if value == "\x00" {
			_ = os.Unsetenv(key)
		} else {
			_ = os.Setenv(key, value)
		}
	}

	// Remove temp directory. forceRemoveAll (not a bare os.RemoveAll) because
	// this Root can end up containing a read-only GOMODCACHE tree — see its
	// doc comment for why that isn't just theoretical.
	if e.Root != "" {
		return forceRemoveAll(e.Root)
	}
	return nil
}

// InitGitRepo initializes the project directory as a git repository.
func (e *TestEnvironment) InitGitRepo() error {
	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init failed: %s: %w", output, err)
	}

	// Configure git user for commits
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config email failed: %s: %w", output, err)
	}

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config name failed: %s: %w", output, err)
	}

	return nil
}

// GitConfigLocal sets a repository-local git config value in the project
// checkout, through this environment's own isolated environment (gitEnv —
// HOME/XDG rooted at e.HomeDir and every testsupport.EnvKeys variable
// scrubbed), so no caller has to hand-roll HOME redirection to make git read
// the fake home instead of the developer's.
//
// It exists for `user.signingkey`: J001600 drives ctxloom's zero-config
// key-discovery chain (internal/signing/agentkey step 2, `git config
// user.signingkey`), which reads the REPOSITORY's own .git/config, so the
// fixture must write there — repository-local, never global, never the host's.
func (e *TestEnvironment) GitConfigLocal(key, value string) error {
	cmd := exec.Command("git", "config", "--local", key, value)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config --local %s failed: %s: %w", key, output, err)
	}
	return nil
}

// AddGitWorktree creates a LINKED git worktree of e.ProjectDir on a fresh
// branch named name, checked out under e.Root/worktrees/<name>, via a real
// `git worktree add` (mirroring taskstest.RealGitWorktreeFixture's shape, but
// through TestEnvironment's own isolated env/dir plumbing so callers land in
// the same isolated HOME/XDG every other acceptance helper trusts). Requires
// e.ProjectDir to already be an initialized git repo with at least one commit
// (InitGitRepo + GitCommit) — `git worktree add -b` needs a valid HEAD to
// branch from. Returns the new worktree's absolute path.
func (e *TestEnvironment) AddGitWorktree(name string) (string, error) {
	dir := filepath.Join(e.Root, "worktrees", name)
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return "", fmt.Errorf("create worktrees parent dir: %w", err)
	}
	cmd := exec.Command("git", "worktree", "add", "-q", "-b", name, dir)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add %q failed: %s: %w", name, output, err)
	}
	return dir, nil
}

// isolatedEnv returns environment variables with home directory properly
// isolated and ambient ctxloom session state scrubbed. Replacing HOME alone is
// not enough: when the suite itself runs inside a ctxloom session, inherited
// vars like CTXLOOM_ROOT (the authoritative project root) and
// CTXLOOM_SESSION_HARP would steer the spawned binary at the LIVE repo's
// .ctxloom instead of the fake project. scrubSessionEnv drops the canonical
// testsupport.EnvKeys set so Run/RunWithStdin/Command/StartMCP are all
// isolated the same way.
func (e *TestEnvironment) isolatedEnv() []string {
	// Variables to replace with our test paths
	replacements := map[string]string{
		"HOME":            e.HomeDir,
		"USERPROFILE":     e.HomeDir, // Windows
		"XDG_CONFIG_HOME": filepath.Join(e.HomeDir, ".config"),
		"XDG_DATA_HOME":   filepath.Join(e.HomeDir, ".local", "share"),
	}

	var env []string
	for _, v := range scrubSessionEnv(os.Environ()) {
		key := strings.SplitN(v, "=", 2)[0]
		if _, shouldReplace := replacements[key]; shouldReplace {
			continue // Skip, we'll add our own
		}
		if _, forced := e.childEnv[key]; forced {
			continue // SetChildEnv wins; appended below
		}
		env = append(env, v)
	}

	// Add our isolated paths
	for key, value := range replacements {
		env = append(env, key+"="+value)
	}

	// Scenario-forced variables last, and only after their ambient namesakes
	// were filtered out above: a duplicate key in a child's environment is
	// resolved by the C library, not by Go, and glibc's getenv returns the
	// FIRST match — so appending without that filter would silently lose to
	// whatever the host already had set.
	for key, value := range e.childEnv {
		env = append(env, key+"="+value)
	}

	return env
}

// gitEnv returns environment variables for git commands.
func (e *TestEnvironment) gitEnv() []string {
	return e.isolatedEnv()
}

// CreateProjectConfig creates the .ctxloom directory structure in the project.
func (e *TestEnvironment) CreateProjectConfig() error {
	dirs := []string{
		filepath.Join(e.ProjectDir, ".ctxloom", "content", "bundles"),
		filepath.Join(e.ProjectDir, ".ctxloom", "profiles"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}
	return nil
}

// WriteFile writes content to a file relative to the project directory.
func (e *TestEnvironment) WriteFile(relPath, content string) error {
	fullPath := filepath.Join(e.ProjectDir, relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}

// WriteHomeFile writes content to a file relative to the home directory.
func (e *TestEnvironment) WriteHomeFile(relPath, content string) error {
	fullPath := filepath.Join(e.HomeDir, relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}

// ReadFile reads a file relative to the project directory.
func (e *TestEnvironment) ReadFile(relPath string) (string, error) {
	fullPath := filepath.Join(e.ProjectDir, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// FileExists checks if a file exists relative to the project directory.
func (e *TestEnvironment) FileExists(relPath string) bool {
	fullPath := filepath.Join(e.ProjectDir, relPath)
	_, err := os.Stat(fullPath)
	return err == nil
}

// ReadHomeFile reads a file relative to the home directory.
func (e *TestEnvironment) ReadHomeFile(relPath string) (string, error) {
	fullPath := filepath.Join(e.HomeDir, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// HomeFileExists checks if a file exists relative to the home directory.
func (e *TestEnvironment) HomeFileExists(relPath string) bool {
	fullPath := filepath.Join(e.HomeDir, relPath)
	_, err := os.Stat(fullPath)
	return err == nil
}

// Command builds an *exec.Cmd for the ctxloom binary with the isolated
// environment, ready to run in the project directory. Unlike Run, it does not
// touch the shared last-output state, so it is safe to build and run many of
// these concurrently (e.g. cross-process store-contention tests). extraEnv
// entries ("KEY=VALUE") are appended after the isolated environment, so they
// win over any inherited value.
func (e *TestEnvironment) Command(extraEnv []string, args ...string) *exec.Cmd {
	cmd := exec.Command(e.AppBinary, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = append(e.isolatedEnv(), extraEnv...)
	cmd.SysProcAttr = pdeathsigSysProcAttr()
	return cmd
}

// Run executes ctxloom with the given arguments in the project directory.
func (e *TestEnvironment) Run(args ...string) error {
	cmd := exec.Command(e.AppBinary, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.isolatedEnv()
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	e.recordRunSplit(args, stdout.String(), stderr.String(), err)
	return err
}

// LastOutput returns the combined stdout/stderr from the last command.
func (e *TestEnvironment) LastOutput() string {
	return e.NthLastOutput(0)
}

// LastStdout returns ONLY the last command's stdout — the stream a
// `--format json` assertion has to parse (see RunRecord.Stdout).
func (e *TestEnvironment) LastStdout() string {
	if len(e.runs) == 0 {
		return ""
	}
	return e.runs[len(e.runs)-1].Stdout
}

// NthLastOutput returns the combined stdout/stderr of the n-th most recent
// command (n=0 is the same value LastOutput returns, n=1 the command before
// that, and so on), or "" if fewer than n+1 commands have run yet. Lets a
// scenario recover an EARLIER command's output after a later command has
// run and LastOutput now reflects that one instead — the exact need five
// acceptance journeys previously met by inventing a private snapshot field.
func (e *TestEnvironment) NthLastOutput(n int) string {
	idx := len(e.runs) - 1 - n
	if idx < 0 || idx >= len(e.runs) {
		return ""
	}
	return e.runs[idx].Output
}

// LastExitCode returns the exit code from the last command.
func (e *TestEnvironment) LastExitCode() int {
	if len(e.runs) == 0 {
		return 0
	}
	return e.runs[len(e.runs)-1].ExitCode
}

// LastError returns the error from the last command.
func (e *TestEnvironment) LastError() error {
	if len(e.runs) == 0 {
		return nil
	}
	return e.runs[len(e.runs)-1].Err
}

// GitCommit creates a git commit with the given message.
func (e *TestEnvironment) GitCommit(message string) error {
	// Add all files
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", output, err)
	}

	// Commit
	cmd = exec.Command("git", "commit", "-m", message, "--allow-empty")
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %s: %w", output, err)
	}

	return nil
}

// RunWithStdin executes ctxloom with stdin input and returns the output. Stdin
// is held open for a short grace period after the write before being closed,
// so a long-lived stdio server (e.g. `ctxloom mcp`) can dispatch and respond
// before it sees EOF. See mcpStdinGrace.
func (e *TestEnvironment) RunWithStdin(stdin string, args ...string) error {
	cmd := exec.Command(e.AppBinary, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.isolatedEnv()
	cmd.SysProcAttr = pdeathsigSysProcAttr()

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	// exec.Cmd starts a background goroutine to copy the child's stdout/stderr
	// into whatever Writer is assigned here as soon as Start() runs; a bare
	// bytes.Buffer is not safe for that concurrent write racing against the
	// polling read in waitForStdinResponses below, so both are a mutex-guarded
	// syncBuffer instead.
	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	_, _ = stdinPipe.Write([]byte(stdin))
	waitForStdinResponses(&stdout, jsonrpcRequestCount(stdin))
	_ = stdinPipe.Close()

	err = cmd.Wait()
	e.recordRunSplit(args, stdout.String(), stderr.String(), err)
	return err
}

// waitForStdinResponses holds stdin open until the child has answered every
// request that was written to it, bounded by mcpStdinGrace.
//
// The completion signal is the ANSWER, never a lull in the output. Its
// predecessor returned as soon as stdout had written something and then gone
// quiet for one poll interval, which is not what "the client has its
// responses" means for a request/response protocol carrying more than one
// request: the server answers `initialize` immediately, and the pause while
// it does the SECOND request's real work (assemble_context loads bundles and
// profiles off disk, and the same startup syncs remotes) reads as exactly
// that lull. Stdin then closed underneath the running handler and the server
// tore down mid-request — the observed "Error: server is closing: EOF" with
// the tool result never written. Nothing was wrong with the server; the test
// client hung up on it, and it did so more often the busier the box was.
//
// want == 0 means the input was not JSON-RPC (nothing to count), so this
// falls back to waiting out the ceiling rather than returning immediately.
func waitForStdinResponses(out *syncBuffer, want int) {
	deadline := time.Now().Add(mcpStdinGrace)
	for time.Now().Before(deadline) {
		if want > 0 && jsonrpcResponseCount(out.String()) >= want {
			return
		}
		time.Sleep(stdinPollInterval)
	}
}

// jsonrpcRequestCount reports how many JSON-RPC requests s carries: one per
// newline-delimited object with both an id and a method. A notification (no
// id) is deliberately not counted — nothing answers it, so waiting for a
// response to it would burn the whole ceiling on every call.
func jsonrpcRequestCount(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if len(msg.ID) > 0 && string(msg.ID) != "null" && msg.Method != "" {
			n++
		}
	}
	return n
}

// jsonrpcResponseCount reports how many JSON-RPC responses s currently holds:
// objects carrying an id and either a result or an error. A half-written line
// simply fails to parse and is counted on a later poll.
func jsonrpcResponseCount(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if len(msg.ID) > 0 && string(msg.ID) != "null" && (len(msg.Result) > 0 || len(msg.Error) > 0) {
			n++
		}
	}
	return n
}

// syncBuffer is a mutex-guarded byte accumulator, safe to read (Len/String)
// from one goroutine while exec.Cmd's own copy goroutine is still writing to
// it -- unlike a bare bytes.Buffer, which guarantees nothing under concurrent
// use.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// RunCount returns the monotonic number of CLI invocations recorded so far
// (see runs). A change between two observations means a command actually ran
// in the interim.
func (e *TestEnvironment) RunCount() int { return len(e.runs) }
