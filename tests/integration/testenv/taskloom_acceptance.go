//go:build integration || acceptance

package testenv

import (
	"bytes"
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
var (
	taskloomBuildOnce sync.Once
	taskloomBinPath   string
	taskloomBuildErr  error
)

// TaskloomBinary builds the taskloom binary once per test process and returns
// its path. Acceptance's own `just test-acceptance` only builds ctxloom (see
// justfile's build/_run build), so there is no pre-built taskloom binary to
// find on disk the way TestEnvironment.findAppBinary locates ctxloom —
// building it here, on demand, is the harness-side fix for that gap.
func TaskloomBinary() (string, error) {
	taskloomBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "taskloom-bin-*")
		if err != nil {
			taskloomBuildErr = err
			return
		}
		taskloomBinPath = filepath.Join(dir, "taskloom")
		cmd := exec.Command("go", "build", "-o", taskloomBinPath, "github.com/ctxloom/ctxloom/cmd/taskloom")
		cmd.Dir = findProjectRoot()
		out, err := cmd.CombinedOutput()
		if err != nil {
			taskloomBuildErr = fmt.Errorf("build taskloom binary: %v\n%s", err, out)
		}
	})
	return taskloomBinPath, taskloomBuildErr
}

// RunTaskloom executes the taskloom binary in e's project directory with e's
// SAME isolated environment (HOME/XDG/session-scrubbed) that ctxloom's
// Run/Command use — so a scenario's taskloom store lives under the identical
// fake HOME its ctxloom-facing assertions already trust, reusing that store
// isolation rather than inventing a second one. extraEnv entries ("KEY=VALUE")
// are appended last and win, matching Command's convention. Safe to call
// concurrently (builds no shared mutable state beyond the one-time binary
// build), so cross-process concurrency scenarios can drive it from goroutines.
func (e *TestEnvironment) RunTaskloom(extraEnv []string, args ...string) (stdout, stderr string, err error) {
	bin, berr := TaskloomBinary()
	if berr != nil {
		return "", "", berr
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = e.ProjectDir
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
