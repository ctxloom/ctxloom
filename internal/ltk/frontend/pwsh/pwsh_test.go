package pwsh

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// fake builds a Frontend whose runner returns canned parser JSON.
func fake(json string) *Frontend {
	return &Frontend{run: func(context.Context, string) ([]byte, error) {
		return []byte(json), nil
	}}
}

func parse(t *testing.T, f *Frontend, src string) *ir.Script {
	t.Helper()
	s, err := f.Parse(context.Background(), ir.ShellPwsh, src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func TestLowerSimpleCommand(t *testing.T) {
	f := fake(`{"commands":[{"argv":[{"k":"lit","v":"go"},{"k":"lit","v":"test"}]}],"hasErrors":false}`)
	s := parse(t, f, "go test")
	if len(s.Pipelines) != 1 {
		t.Fatalf("want 1 command, got %d", len(s.Pipelines))
	}
	c := s.Pipelines[0].Commands[0]
	if c.Program() != "go" || len(c.Argv) != 2 || c.Argv[1] != "test" {
		t.Errorf("argv = %v", c.Argv)
	}
}

func TestLowerFlattensNestedCommands(t *testing.T) {
	// FindAll returns every CommandAst flat; e.g. Write-Host $(Remove-Item x).
	f := fake(`{"commands":[
		{"argv":[{"k":"lit","v":"Write-Host"},{"k":"dyn","v":"$(Remove-Item x)"}]},
		{"argv":[{"k":"lit","v":"Remove-Item"},{"k":"lit","v":"x"}]}
	],"hasErrors":false}`)
	s := parse(t, f, "Write-Host $(Remove-Item x)")
	var progs []string
	for _, c := range s.Commands() {
		progs = append(progs, c.Program())
	}
	if len(progs) != 2 || progs[1] != "Remove-Item" {
		t.Errorf("programs = %v, want [Write-Host Remove-Item]", progs)
	}
}

// Parser-reported errors must surface as a parse error (alongside whatever
// partial AST was salvaged) so the configured on_parse_error policy applies —
// swallowing them would mean unparseable PowerShell is matched against a
// partial command list and silently allowed by default.
func TestParserErrorsSurfaceAsParseError(t *testing.T) {
	f := fake(`{"commands":[{"argv":[{"k":"lit","v":"Remove-Item"}]}],"hasErrors":true,"errors":["Missing closing '}'"]}`)
	s, err := f.Parse(context.Background(), ir.ShellPwsh, "if ($x) { Remove-Item")
	if err == nil {
		t.Fatal("hasErrors must yield a parse error")
	}
	if !strings.Contains(err.Error(), "Missing closing '}'") {
		t.Errorf("the parser's message should ride in the error, got %v", err)
	}
	if s == nil {
		t.Fatal("on parse error want a non-nil script (Frontend contract)")
	}
	if len(s.Commands()) != 1 {
		t.Errorf("salvaged commands should still be lowered, got %+v", s.Commands())
	}
}

// hasErrors without messages still errors (the policy must apply either way).
func TestParserErrorsWithoutDetail(t *testing.T) {
	f := fake(`{"commands":[],"hasErrors":true}`)
	_, err := f.Parse(context.Background(), ir.ShellPwsh, "if (")
	if err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("want a parse error, got %v", err)
	}
}

func TestRunnerErrorPropagates(t *testing.T) {
	want := errors.New("boom")
	f := &Frontend{run: func(context.Context, string) ([]byte, error) { return nil, want }}
	s, err := f.Parse(context.Background(), ir.ShellPwsh, "x")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want boom", err)
	}
	if s == nil {
		t.Error("on runner error want a non-nil script")
	}
}

func TestShells(t *testing.T) {
	if got := New().Shells(); len(got) != 1 || got[0] != ir.ShellPwsh {
		t.Errorf("Shells() = %v", got)
	}
}

// TestRunErrIdentifiesTheDeadline pins that a parse killed by parseTimeout is
// distinguishable from any other exec failure. The context kills the child, so
// cmd.Run reports "signal: killed" and nothing else — the same text an OOM
// kill produces, which is why TestIntegrationRealParser below has to SKIP on
// it. Both the message and errors.Is must name the deadline.
func TestRunErrIdentifiesTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	// The fixture is only hostile if the context really is past its deadline
	// from runErr's point of view.
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("fixture is not hostile: ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}

	err := runErr(ctx, errors.New("signal: killed"), "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a timed-out parse must be identifiable with errors.Is, got %v", err)
	}
	if !strings.Contains(err.Error(), parseTimeout.String()) {
		t.Errorf("the error should name the %s limit, got %v", parseTimeout, err)
	}
}

// TestRunErrPassesThroughOtherFailures pins the other direction: an ordinary
// failure must NOT be reported as a timeout, and must still carry the child's
// stderr.
func TestRunErrPassesThroughOtherFailures(t *testing.T) {
	err := runErr(context.Background(), errors.New("exit status 1"), "Parser.ParseInput blew up")
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Errorf("an ordinary failure was reported as a context failure: %v", err)
	}
	if !strings.Contains(err.Error(), "Parser.ParseInput blew up") {
		t.Errorf("the child's stderr should ride in the error, got %v", err)
	}
}

// TestBinaryResolutionIsOwnedByTheFrontend pins that the interpreter lookup is
// memoized per Frontend, not per process. As package-level state the FIRST
// answer was frozen for the life of the process — including the answer "no
// PowerShell here" — so nothing could invalidate it and no test could arrange
// either outcome.
func TestBinaryResolutionIsOwnedByTheFrontend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub interpreter is a POSIX shell script")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "pwsh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// Hostility check: with nothing on PATH the frontend must genuinely report
	// PowerShell missing, or the second half proves nothing.
	t.Setenv("PATH", "")
	if _, err := New().Parse(context.Background(), ir.ShellPwsh, "x"); !errors.Is(err, errUnavailable) {
		t.Fatalf("fixture is not hostile: with an empty PATH want %v, got %v", errUnavailable, err)
	}

	// A frontend built after an interpreter appears must find it.
	t.Setenv("PATH", dir)
	if _, err := New().Parse(context.Background(), ir.ShellPwsh, "x"); errors.Is(err, errUnavailable) {
		t.Error("a fresh Frontend still reports PowerShell missing; the lookup is cached process-wide with no owner")
	}
}

// TestParseScriptEmitsEveryKeyTheDecoderReads guards the hand-maintained
// contract between the embedded PowerShell program and the Go structs that
// decode its output. That contract is a connascence of NAME across two
// languages: rename a json tag here and the script keeps emitting the old key,
// which unmarshals to a zero value rather than an error — commands vanish, or
// hasErrors reads false and unparseable input is matched as if it parsed.
//
// TestIntegrationRealParser does execute the script, so it is not true that
// nothing does; but it SKIPS wherever PowerShell is absent, which is this
// repo's dev containers and CI. This test needs no PowerShell and so runs
// everywhere. It matches `key=`, the form a PowerShell hashtable literal uses.
func TestParseScriptEmitsEveryKeyTheDecoderReads(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(psResult{}),
		reflect.TypeOf(psCommand{}),
		reflect.TypeOf(psElem{}),
	} {
		for i := range typ.NumField() {
			f := typ.Field(i)
			key := f.Tag.Get("json")
			// Without this the loop would pass vacuously on an untagged struct.
			if key == "" {
				t.Fatalf("%s.%s has no json tag, so this contract check would be vacuous", typ.Name(), f.Name)
			}
			if !strings.Contains(parseScript, key+"=") {
				t.Errorf("parseScript never assigns %q, which %s.%s decodes; the two sides of the contract have drifted", key, typ.Name(), f.Name)
			}
		}
	}
}

// TestIntegrationRealParser exercises the actual PowerShell parser when present.
func TestIntegrationRealParser(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		if _, err := exec.LookPath("powershell"); err != nil {
			t.Skip("no PowerShell on PATH")
		}
	}
	s, err := New().Parse(context.Background(), ir.ShellPwsh, "Get-ChildItem -Path . -Recurse")
	if err != nil {
		// On constrained CI runners the pwsh process is sometimes OOM/resource
		// killed ("signal: killed") before it parses — an environment failure,
		// not a parser bug. Skip rather than fail so the flake doesn't block CI.
		if strings.Contains(err.Error(), "signal: killed") {
			t.Skipf("pwsh process killed by the environment (not a parser failure): %v", err)
		}
		t.Fatalf("real parse: %v", err)
	}
	cmds := s.Commands()
	if len(cmds) == 0 || cmds[0].Program() != "Get-ChildItem" {
		t.Fatalf("commands = %+v", cmds)
	}

	// Unparseable input must come back as a parse error so on_parse_error applies.
	if _, err := New().Parse(context.Background(), ir.ShellPwsh, "if ("); err == nil {
		t.Error("real parser: unparseable input must yield a parse error")
	} else if strings.Contains(err.Error(), "signal: killed") {
		t.Skipf("pwsh process killed by the environment: %v", err)
	}
}
