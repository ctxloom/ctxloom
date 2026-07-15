package testenv

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// mcpStdinGrace is how long RunWithStdin keeps stdin open after writing the
// request(s). The MCP server (SDK stdio transport) dispatches requests
// asynchronously; if stdin closes immediately after the write, its read loop
// hits EOF and tears the server down before the handler writes its response.
// A real MCP client keeps stdin open until it has its responses — this grace
// models that. Responses arrive in milliseconds; the window is generous so the
// suite stays reliable on slow CI.
const mcpStdinGrace = 2 * time.Second

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

	// lastOutput stores the output from the last command
	lastOutput string

	// lastError stores the error from the last command
	lastError error

	// lastExitCode stores the exit code from the last command
	lastExitCode int

	// runCount is a monotonic count of CLI invocations (Run / RunWithStdin)
	// whose output landed in lastOutput. It lets a caller distinguish "a
	// command ran this step and happened to produce output identical to the
	// previous step's" from "no command ran, lastOutput is just stale" — a
	// difference lastOutput alone cannot express. Used by the @doc capture
	// sidecar; harmless otherwise.
	runCount int
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
	cwd, _ := os.Getwd()
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
func (e *TestEnvironment) Setup() error {
	// Store and override HOME
	e.storeAndSetEnv("HOME", e.HomeDir)

	// On Windows, also set USERPROFILE
	if runtime.GOOS == "windows" {
		e.storeAndSetEnv("USERPROFILE", e.HomeDir)
	}

	// Clear any existing MLCM config paths
	e.storeAndSetEnv("XDG_CONFIG_HOME", filepath.Join(e.HomeDir, ".config"))

	return nil
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

	// Remove temp directory
	if e.Root != "" {
		return os.RemoveAll(e.Root)
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
		env = append(env, v)
	}

	// Add our isolated paths
	for key, value := range replacements {
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
	return cmd
}

// Run executes ctxloom with the given arguments in the project directory.
func (e *TestEnvironment) Run(args ...string) error {
	cmd := exec.Command(e.AppBinary, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.isolatedEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	e.lastOutput = stdout.String() + stderr.String()
	e.lastError = err
	e.runCount++

	if exitErr, ok := err.(*exec.ExitError); ok {
		e.lastExitCode = exitErr.ExitCode()
	} else if err != nil {
		e.lastExitCode = -1
	} else {
		e.lastExitCode = 0
	}

	return err
}

// LastOutput returns the combined stdout/stderr from the last command.
func (e *TestEnvironment) LastOutput() string {
	return e.lastOutput
}

// LastExitCode returns the exit code from the last command.
func (e *TestEnvironment) LastExitCode() int {
	return e.lastExitCode
}

// LastError returns the error from the last command.
func (e *TestEnvironment) LastError() error {
	return e.lastError
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

// GitBranch creates and checks out a new branch.
func (e *TestEnvironment) GitBranch(name string) error {
	cmd := exec.Command("git", "checkout", "-b", name)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b failed: %s: %w", output, err)
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

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	_, _ = stdinPipe.Write([]byte(stdin))
	// Give the server time to respond, then close stdin for a clean shutdown.
	time.Sleep(mcpStdinGrace)
	_ = stdinPipe.Close()

	err = cmd.Wait()
	e.lastOutput = stdout.String() + stderr.String()
	e.lastError = err
	e.runCount++

	if exitErr, ok := err.(*exec.ExitError); ok {
		e.lastExitCode = exitErr.ExitCode()
	} else if err != nil {
		e.lastExitCode = -1
	} else {
		e.lastExitCode = 0
	}

	return err
}

// RunCount returns the monotonic number of CLI invocations whose output landed
// in lastOutput. A change between two observations means a command actually ran
// in the interim (see the runCount field).
func (e *TestEnvironment) RunCount() int { return e.runCount }
