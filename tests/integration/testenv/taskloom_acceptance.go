//go:build integration || acceptance

package testenv

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// taskloom binary build state, mirrored on tests/taskloom/crossprocess_test.go's
// tasksBinary: build the real `taskloom` binary once per test process rather
// than once per scenario, since every acceptance scenario gets a fresh
// TestEnvironment (and so a fresh isolated HOME/project) but there is no
// reason to pay a `go build` per scenario for a binary whose source doesn't
// change mid-run.
//
// taskloomBinDir is that build's MkdirTemp directory, tracked separately
// rather than recomputed with filepath.Dir(taskloomBinPath) at teardown:
// filepath.Dir("") is ".", so a teardown that ran before any build would
// RemoveAll the process's working directory. See RemoveTaskloomBinary.
var (
	taskloomBuildOnce sync.Once
	taskloomBinDir    string
	taskloomBinPath   string
	taskloomBuildErr  error
)

// realProcessEnv snapshots this process's environment at package-init time —
// before any scenario's TestEnvironment.Setup() has had a chance to run.
// Setup() overwrites HOME and XDG_CONFIG_HOME on the WHOLE process (godog
// runs every scenario serially in one binary), not just for a spawned
// subprocess, so any later `exec.Command` that leaves cmd.Env nil inherits
// whatever fake home the CURRENTLY ACTIVE scenario happens to have set. The
// `go build` below is exactly that: because TaskloomBinary() builds lazily
// (sync.Once, first call wins) and can therefore fire mid-scenario, an
// unguarded build resolves GOPATH/GOMODCACHE (and the go env config file
// itself, via XDG_CONFIG_HOME) underneath that scenario's ephemeral
// /tmp/ctxloom-integration-* home instead of the real one — pulling a fresh
// ~150MB module cache into a directory that (a) is normally short-lived but
// (b) becomes a much bigger-than-usual orphan if this process is ever killed
// before the scenario's Cleanup runs (mutation-test timeouts, Ctrl-C). Using
// the real, pre-scenario environment keeps the build on the machine's real
// module cache, exactly like every other `go build` invocation.
var realProcessEnv = os.Environ()

// TaskloomBinary builds the taskloom binary once per test process and returns
// its path. Acceptance's own `just test-acceptance` only builds ctxloom (see
// justfile's build/_run build), so there is no pre-built taskloom binary to
// find on disk the way TestEnvironment.findAppBinary locates ctxloom —
// building it here, on demand, is the harness-side fix for that gap.
func TaskloomBinary() (string, error) {
	taskloomBuildOnce.Do(func() {
		// Build the output binary under GOTMPDIR when set, not the default
		// system temp. On hosts where /tmp is a small tmpfs, the linker's mmap
		// of the ~30MB output file ENOSPCs under the suite's parallel builds;
		// GOTMPDIR (which `go build` also honors for its own intermediates)
		// points at a disk-backed dir. Empty GOTMPDIR falls back to os.TempDir.
		dir, err := os.MkdirTemp(os.Getenv("GOTMPDIR"), "taskloom-bin-*")
		if err != nil {
			taskloomBuildErr = err
			return
		}
		taskloomBinDir = dir
		taskloomBinPath = filepath.Join(dir, "taskloom")
		cmd := exec.Command("go", "build", "-o", taskloomBinPath, "github.com/ctxloom/ctxloom/cmd/taskloom")
		cmd.Dir = findProjectRoot()
		cmd.Env = realProcessEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			taskloomBuildErr = fmt.Errorf("build taskloom binary: %v\n%s", err, out)
		}
	})
	return taskloomBinPath, taskloomBuildErr
}

// RemoveTaskloomBinary deletes the ~30MB build directory TaskloomBinary
// created. Call it ONCE, from a TestMain after m.Run() has returned —
// tests/acceptance/acceptance_test.go is the only caller, because
// tests/acceptance is the only package that calls TaskloomBinary/RunTaskloom.
// It must not be a per-test t.Cleanup: the binary lives behind a sync.Once and
// is shared by every scenario in the process, so removing it mid-run would
// yank the binary out from under later scenarios.
//
// Without this the directory outlived the process forever, once per test
// BINARY (not per test): 304 orphans / 8.5G of /tmp measured on this machine,
// which took a 16G tmpfs to 90% and made `just test-acceptance` fail with
// "link: mapping output file failed: no space left on device" reported as a
// container build defect. See task smashing-olive.
//
// Idempotent, and deliberately silent on failure: a suite that goes red
// because it could not unlink a temp directory is worse than the leak. A
// second call is a no-op, and a call before any build never touches the disk.
func RemoveTaskloomBinary() {
	if taskloomBinDir == "" {
		return
	}
	dir := taskloomBinDir
	taskloomBinDir = ""
	taskloomBinPath = ""
	// Fail loud rather than silently hand back a path that no longer exists:
	// any TaskloomBinary() call after teardown is a wiring bug (the sync.Once
	// has already fired, so it would otherwise return the deleted path, nil).
	taskloomBuildErr = errors.New("taskloom binary was removed by RemoveTaskloomBinary: teardown ran while the binary was still in use")
	_ = os.RemoveAll(dir)
}

// RunTaskloom executes the taskloom binary in e's project directory with e's
// SAME isolated environment (HOME/XDG/session-scrubbed) that ctxloom's
// Run/Command use — so a scenario's taskloom store lives under the identical
// fake HOME its ctxloom-facing assertions already trust, reusing that store
// isolation rather than inventing a second one. extraEnv entries ("KEY=VALUE")
// are appended last and win, matching Command's convention. Safe to call
// concurrently (builds no shared mutable state beyond the one-time binary
// build), so cross-process concurrency scenarios can drive it from goroutines.
// Delegates to RunTaskloomIn(e.ProjectDir, ...) so this remains the common
// case while callers that need a DIFFERENT cwd (e.g. a linked git worktree —
// see the J16 worktree-task-store journey) have a seam of their own.
func (e *TestEnvironment) RunTaskloom(extraEnv []string, args ...string) (stdout, stderr string, err error) {
	return e.RunTaskloomIn(e.ProjectDir, extraEnv, args...)
}

// RunTaskloomIn is RunTaskloom, but runs the binary in dir instead of e's
// project directory — the seam a scenario driving taskloom from a LINKED git
// worktree (or any other directory distinct from e.ProjectDir) needs, since
// which directory taskloom runs in is exactly what determines which
// task-store identity it resolves (internal/taskloom/workdir).
func (e *TestEnvironment) RunTaskloomIn(dir string, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	bin, berr := TaskloomBinary()
	if berr != nil {
		return "", "", berr
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(e.isolatedEnv(), extraEnv...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// StartTaskloomMCP spawns `taskloom mcp` in e's project directory with the
// same isolated environment StartMCP uses for `ctxloom mcp`, so the
// agent-instruction-surface journey observes the SAME session-scrubbed home
// as every other MCP-facing scenario. Reuses startMCPProcess (mcpclient.go)
// rather than re-implementing the stdio JSON-RPC process/reader plumbing —
// additive: StartMCP's signature and behavior are untouched.
func (e *TestEnvironment) StartTaskloomMCP(extraEnv ...string) (*MCPClient, error) {
	bin, err := TaskloomBinary()
	if err != nil {
		return nil, err
	}
	return startMCPProcess(bin, []string{"mcp"}, e.ProjectDir, e.isolatedEnv(), extraEnv...)
}
