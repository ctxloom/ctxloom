package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/cli"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport/formatparity"
)

// runMain re-executes this test binary as ctxloom (TestMain's mainArgvEnv
// branch) and returns the process's exit status and stderr.
func runMain(t *testing.T, args ...string) (int, string) {
	t.Helper()
	return runMainIn(t, "", args...)
}

// runMainIn is runMain with the child's working directory set to dir (""
// inherits this process's).
func runMainIn(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary to re-exec: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), mainArgvEnv+"="+strings.Join(args, " "))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	runErr := cmd.Run()
	if runErr == nil {
		return 0, stderr.String()
	}
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		t.Fatalf("re-executing the test binary as ctxloom: %v", runErr)
	}
	return ee.ExitCode(), stderr.String()
}

// ctxloom's --format plumbing and process tail are the family's: the same
// help, completion, default, human error line and structured envelopes as
// every other binary (formatparity.Check), reached through main itself.
func TestFormatParity(t *testing.T) {
	formatparity.Check(t, formatparity.Binary{
		Prog: "ctxloom",
		Root: func() *cobra.Command { return cli.GetRootCmd(compose(&report.Collector{}, safefs.New())) },
		Run:  runMain,
		// bundle show takes exactly one argument; omitting it fails in
		// argument validation, before any store is opened.
		FailingArgs: []string{"bundle", "show"},
		Refusal:     refusedConfig(t),
	})
}

// refusedConfig runs ctxloom in a project whose config carries one unknown
// key: the config gate refuses it with one finding naming one fix.
func refusedConfig(t *testing.T) func(*testing.T, ...string) (int, string) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := []byte("schema_version: 7\nzz_unknown_key: 1\n")
	if err := os.WriteFile(filepath.Join(dir, ".ctxloom", "config.yaml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	return func(t *testing.T, extra ...string) (int, string) {
		return runMainIn(t, dir, append([]string{"profile", "list"}, extra...)...)
	}
}

// The one failure only ctxloom's entrypoint can produce: a verb that slipped
// through cobra's --help short-circuit is refused AFTER the tree ran (the
// dispatch step), and that refusal still ends through the family's tail.
func TestStrayVerbUnderHelp_EndsThroughTheFamilyTail(t *testing.T) {
	code, stderr := runMain(t, "mcp", "--help", "bogus")
	if code != 1 || !strings.HasPrefix(stderr, `ctxloom: unknown command "bogus" for "ctxloom mcp"`) {
		t.Errorf("exit %d, stderr %q; want 1 and the family line naming the stray verb", code, stderr)
	}
	code, stderr = runMain(t, "mcp", "--help", "bogus", "--format", "json")
	if code != 1 || !strings.HasPrefix(stderr, "{") || !strings.Contains(stderr, `"error"`) {
		t.Errorf("--format json: exit %d, stderr %q; want 1 and an error envelope", code, stderr)
	}
}
