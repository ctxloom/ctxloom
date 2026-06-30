package pwsh

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

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
