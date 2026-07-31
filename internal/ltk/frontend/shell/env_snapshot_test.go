package shell_test

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// U071-F08 calls the process-environment snapshot taken in New() a connascence
// of TIMING with the composition root. It is refuted by the deployment, not by
// the code: `ltk evaluate` and `ltk check` are one-shot CLI processes that
// build the App (cmd/ltk/wiring.go newDecider) and parse exactly one command
// before exiting, and nothing on ltk's analysis path calls os.Setenv. There is
// no window between New and Parse for the environment to change in, so the
// row's remedy — snapshotting at Parse time — cannot be driven red here. That
// is stated rather than papered over.
//
// What IS worth maintaining is the contract the snapshot exists to serve: the
// frontend resolves variables against the environment of the process whose
// command it is guarding. These pins hold that, and go red if variable
// resolution is dropped or rebound to something other than the process
// environment.

// TestFrontendResolvesFromProcessEnvironment asserts by RESOLVED ARGV only —
// never by inspecting or printing the environment, which on this machine
// carries live credentials.
func TestFrontendResolvesFromProcessEnvironment(t *testing.T) {
	t.Setenv("LTK_TEST_PROGRAM", "git")

	s, err := shell.New().Parse(context.Background(), ir.ShellBash, `$LTK_TEST_PROGRAM push --force`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cmds := s.Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands = %d, want 1", len(cmds))
	}
	if got := cmds[0].Program(); got != "git" {
		t.Errorf("program = %q, want git: the frontend must resolve variables from the process environment", got)
	}
}

// TestInScriptAssignmentOverridesProcessEnvironment pins the other half of the
// resolution order the package documents: assignments seen earlier in the same
// script win over the inherited environment.
func TestInScriptAssignmentOverridesProcessEnvironment(t *testing.T) {
	t.Setenv("LTK_TEST_PROGRAM", "echo")

	s, err := shell.New().Parse(context.Background(), ir.ShellBash, `LTK_TEST_PROGRAM=git; $LTK_TEST_PROGRAM push --force`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var progs []string
	for _, c := range s.Commands() {
		progs = append(progs, c.Program())
	}
	found := false
	for _, p := range progs {
		if p == "git" {
			found = true
		}
	}
	if !found {
		t.Errorf("programs = %v; an in-script assignment must win over the inherited environment", progs)
	}
}
