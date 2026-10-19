package testenv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// Every child this suite starts goes through one gate, AssertChildEnvSandboxed,
// because isolation held by convention did not hold: harness cells once pointed
// a child's HOME and CLAUDE_CONFIG_DIR at the developer's real home, and a real
// engine run from there wrote the developer's own config, sessions and
// project registry. Nothing about building an *exec.Cmd stops that, so the
// refusal sits at the one place every child passes: the start.

// sandboxExemptEnv names the only variables a child's environment may point
// into the real home. Each is here because the child cannot do its job without
// the real location, and none names state an engine or ctxloom writes. Every
// other variable is refused by default; there is no list of names presumed
// safe.
var sandboxExemptEnv = map[string]bool{
	// Every program a child runs — sh, git, the go tool, an installed engine —
	// is found through PATH, and developer machines install them under the
	// home (~/.local/bin, ~/go/bin). PATH names programs to execute, not a
	// place a child keeps state.
	"PATH": true,
	// The go tool's module and build caches. A fixture build reuses the real
	// ones (GoCombinedOutput); resolved under a sandbox home they would pull
	// the whole module graph into a directory every scenario deletes.
	"GOPATH":     true,
	"GOMODCACHE": true,
	"GOCACHE":    true,
	// The go tool's own config file. It selects the toolchain the module builds
	// with (GOTOOLCHAIN); without it a fixture build runs on whichever go is
	// first on PATH, which need not be the version go.mod asks for.
	"GOENV": true,
	// The go tool's scratch directory. A developer points it at disk because a
	// tmpfs /tmp runs out of space when the linker writes under parallel builds
	// (the justfile's go_tmp), so a fixture build that lost it fails for that
	// reason alone.
	"GOTMPDIR": true,
}

// realProcessEnv snapshots this process's environment at package init, before
// any scenario's Setup rebinds HOME and XDG_CONFIG_HOME for the WHOLE process
// (godog runs every scenario serially in one binary). It is read for exactly two
// facts the rebinding would otherwise hide: where the go toolchain keeps its
// caches and config (goToolchainEnv), and which home this process started in
// (guardedHome's fallback). It is never handed to a child.
var realProcessEnv = os.Environ()

// guardedHome is the home AssertChildEnvSandboxed protects: the account's home
// from the passwd database, which no TestEnvironment can move. Setup rebinds
// $HOME to the scenario's fake home for the whole process, so guarding $HOME
// would guard the sandbox and leave the real home open.
//
// An account the passwd database does not know — a container started under an
// arbitrary uid — has no passwd home; the home its programs use is then the
// $HOME it started with, read from realProcessEnv. A home of "/" guards
// nothing: every absolute path lies under it, so treating it as a home would
// refuse every child rather than protect anything.
var guardedHome = sync.OnceValue(func() string {
	home := ""
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
	}
	if home == "" {
		home = envValue(realProcessEnv, "HOME")
	}
	home = filepath.Clean(home)
	if home == "." || home == string(filepath.Separator) {
		return ""
	}
	return home
})

// guardedHomeForms is guardedHome and, when it differs, its symlink-resolved
// form: a value can name the home either way.
func guardedHomeForms() []string {
	home := guardedHome()
	if home == "" {
		return nil
	}
	forms := []string{home}
	if real, err := filepath.EvalSymlinks(home); err == nil && real != home {
		forms = append(forms, real)
	}
	return forms
}

// errNilChildEnv refuses a child whose Env is nil.
var errNilChildEnv = errors.New("refusing to start a child with a nil environment: exec hands such a child this process's whole environment, " +
	"the real home and every ambient variable with it. Build the command from a TestEnvironment (Command, HostCommand, GitCommand) or set Env explicitly")

// AssertChildEnvSandboxed refuses env when it is nil, or when any variable
// outside sandboxExemptEnv has a value that is, or lies under, the real home
// (guardedHome) — as the whole value, as one entry of a list, or embedded in a
// longer string such as a URL or a command line, since each steers the child
// at the real home alike. The refusal names the offending variables and never
// their values, which may be credentials.
func AssertChildEnvSandboxed(env []string) error {
	if env == nil {
		return errNilChildEnv
	}
	homes := guardedHomeForms()
	var leaked []string
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if sandboxExemptEnv[key] {
			continue
		}
		if namesAnyHome(value, homes) {
			leaked = append(leaked, key)
		}
	}
	if len(leaked) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start a child whose environment points into the real home %s: %s. "+
		"A child handed the real home writes the developer's own engine and ctxloom state; point these at the TestEnvironment's sandbox instead",
		homes[0], strings.Join(leaked, ", "))
}

// namesAnyHome reports whether value names any of homes (see namesHome).
func namesAnyHome(value string, homes []string) bool {
	for _, h := range homes {
		if namesHome(value, h) {
			return true
		}
	}
	return false
}

// namesHome reports whether value contains home as a whole path: an
// occurrence not glued to a longer path name on either side. /home/u/x and
// file:///home/u name /home/u; /home/u2 and /srv/home/u do not.
func namesHome(value, home string) bool {
	for from := 0; from <= len(value); {
		i := strings.Index(value[from:], home)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(home)
		if (start == 0 || !isPathNameByte(value[start-1])) && (end == len(value) || !isPathNameByte(value[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

// isPathNameByte reports whether b can continue a path element's name, so that
// a home found next to it is part of some other path.
func isPathNameByte(b byte) bool {
	return b == '.' || b == '_' || b == '-' ||
		('0' <= b && b <= '9') || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}

// dropHomeEnv returns env without the variables AssertChildEnvSandboxed would
// refuse. It is the builder's half of the gate: the ambient environment of
// anyone running this suite from a checkout under their home carries such
// variables (PWD at the least), and a sandbox drops them rather than inherit
// them, so the gate refuses only what a test deliberately put back.
func dropHomeEnv(env []string) []string {
	homes := guardedHomeForms()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if !sandboxExemptEnv[key] && namesAnyHome(value, homes) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// sandboxBaseEnv is the environment every child is built from: the process's,
// with git's repository-locating variables removed (gitutil.SanitizedEnviron),
// the ambient session state scrubbed (scrubAmbientEnv) and every pointer into
// the real home dropped (dropHomeEnv).
func sandboxBaseEnv() []string {
	return dropHomeEnv(scrubAmbientEnv(gitutil.SanitizedEnviron()))
}

// Sandbox is an environment around a home the caller already has, for a test
// that starts a child but needs nothing NewTestEnvironment builds: no project,
// no binary under test, no Setup. Its children get the same scrubbed,
// real-home-free environment, with HOME and the XDG directories at home. Only
// HostCommand, GitCommand and the Start family are meaningful on it.
func Sandbox(home string) *TestEnvironment {
	return &TestEnvironment{HomeDir: home}
}

// HostCommand builds a command for bin — any program other than the binary
// under test, which Command builds: a container runtime, an engine CLI, a stub,
// a fixture server — to run in ProjectDir under the same isolated environment
// Command gives ctxloom. A caller whose child needs a different sandbox (a live
// cell's own HOME and config dir) replaces Env wholesale. ctx bounds the run as
// exec.CommandContext's does; context.Background() bounds nothing.
func (e *TestEnvironment) HostCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = e.ProjectDir
	cmd.Env = e.isolatedEnv()
	return cmd
}

// GitCommand builds a git invocation in dir under the isolated environment,
// made hermetic for git by taskstest.HermeticGitEnv: git reads the sandbox's
// HOME rather than the developer's, and its repository discovery cannot climb
// out of the temp root.
func (e *TestEnvironment) GitCommand(dir string, args ...string) *exec.Cmd {
	cmd := taskstest.GitCmd(dir, nil, args...)
	cmd.Env = taskstest.HermeticGitEnv(e.isolatedEnv())
	return cmd
}

// Start starts cmd. It is the only sanctioned way for these suites to start a
// child: it refuses, before anything runs, a cmd whose environment
// AssertChildEnvSandboxed refuses.
func (e *TestEnvironment) Start(cmd *exec.Cmd) error { return startSandboxed(cmd) }

// RunCmd is Start followed by Wait: exec.Cmd.Run through the gate.
func (e *TestEnvironment) RunCmd(cmd *exec.Cmd) error { return runSandboxed(cmd) }

// Output is exec.Cmd.Output through the gate.
func (e *TestEnvironment) Output(cmd *exec.Cmd) ([]byte, error) { return outputSandboxed(cmd) }

// CombinedOutput is exec.Cmd.CombinedOutput through the gate.
func (e *TestEnvironment) CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	return combinedOutputSandboxed(cmd)
}

// startSandboxed is the gate itself, for this package's own children, which are
// not all started on behalf of a TestEnvironment.
func startSandboxed(cmd *exec.Cmd) error {
	if err := AssertChildEnvSandboxed(cmd.Env); err != nil {
		return fmt.Errorf("%s: %w", commandLine(cmd), err)
	}
	return cmd.Start()
}

func runSandboxed(cmd *exec.Cmd) error {
	if err := startSandboxed(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}

// outputSandboxed captures stdout and, when the caller left Stderr unset, keeps
// stderr on the *exec.ExitError as exec.Cmd.Output does.
func outputSandboxed(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	captureErr := cmd.Stderr == nil
	if captureErr {
		cmd.Stderr = &stderr
	}
	err := runSandboxed(cmd)
	var exitErr *exec.ExitError
	if captureErr && errors.As(err, &exitErr) {
		exitErr.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

func combinedOutputSandboxed(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := runSandboxed(cmd)
	return b.Bytes(), err
}

// GoCombinedOutput runs the go tool — a fixture's `go build`, a `go run` — at
// the module root and returns its combined output. Its environment is the
// sandbox base with HOME and the XDG directories removed and the toolchain's
// own locations set explicitly (goToolchainEnv), so the build uses the
// developer's toolchain and caches and nothing else of the real home reaches
// it. extraEnv entries ("KEY=VALUE") are appended last.
func GoCombinedOutput(extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = findProjectRoot()
	cmd.Env = goCommandEnv(extraEnv)
	return combinedOutputSandboxed(cmd)
}

// goCommandEnv is GoCombinedOutput's environment.
func goCommandEnv(extraEnv []string) []string {
	return append(append(withoutEnvKeys(sandboxBaseEnv(), goStrippedEnv), goToolchainEnv()...), extraEnv...)
}

// goStrippedEnv are the variables a go tool child never inherits: the home and
// XDG directories, whose only use to the go tool is deriving the locations
// goToolchainEnv sets outright, and those locations' own ambient values.
var goStrippedEnv = []string{
	"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	"GOENV", "GOPATH", "GOMODCACHE", "GOCACHE", "GOTMPDIR",
}

// goToolchainEnv resolves, once, where the go toolchain this process was
// started under keeps its config and caches, the way the go tool resolves them:
// an explicit variable first, then the go env file, then the default derived
// from the home. It reads realProcessEnv, never the live environment, which a
// running scenario has rebound to its fake home.
//
// Resolving in-process rather than asking `go env` keeps every child behind the
// gate: `go env` would need the real HOME to answer.
var goToolchainEnv = sync.OnceValue(func() []string {
	get := func(key string) string { return envValue(realProcessEnv, key) }
	home := get("HOME")
	configHome := firstNonEmpty(get("XDG_CONFIG_HOME"), joinUnder(home, ".config"))
	cacheHome := firstNonEmpty(get("XDG_CACHE_HOME"), joinUnder(home, ".cache"))

	goenv := firstNonEmpty(get("GOENV"), joinUnder(configHome, "go", "env"))
	file := readGoEnvFile(goenv)
	resolve := func(key, def string) string { return firstNonEmpty(get(key), file[key], def) }

	gopath := resolve("GOPATH", joinUnder(home, "go"))
	firstGopath, _, _ := strings.Cut(gopath, string(os.PathListSeparator))
	vars := map[string]string{
		"GOENV":      goenv,
		"GOPATH":     gopath,
		"GOMODCACHE": resolve("GOMODCACHE", joinUnder(firstGopath, "pkg", "mod")),
		"GOCACHE":    resolve("GOCACHE", joinUnder(cacheHome, "go-build")),
		"GOTMPDIR":   resolve("GOTMPDIR", ""),
	}
	var out []string
	for _, key := range goStrippedEnv {
		if v := vars[key]; v != "" {
			out = append(out, key+"="+v)
		}
	}
	return out
})

// readGoEnvFile returns the KEY=VALUE settings of the go env file at path
// (`go env -w` writes it), or none when it is absent or unreadable.
func readGoEnvFile(path string) map[string]string {
	settings := map[string]string{}
	if path == "" || path == "off" {
		return settings
	}
	f, err := os.Open(path) //nolint:gosec // the go env file this process's toolchain reads
	if err != nil {
		return settings
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok && k != "" {
			settings[k] = v
		}
	}
	return settings
}

// withoutEnvKeys returns env without any entry for keys.
func withoutEnvKeys(env []string, keys []string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	return out
}

// envValue is the value getenv would return for key in env: the FIRST match.
func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// joinUnder joins elems under base, or returns "" when base is unknown, so an
// unset HOME yields no default rather than a relative path.
func joinUnder(base string, elems ...string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elems...)...)
}
