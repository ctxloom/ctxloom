package cmd

import (
	"context"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

func parse(t *testing.T, src string) *ir.Script {
	t.Helper()
	s, err := New().Parse(context.Background(), ir.ShellCmd, src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return s
}

func programs(s *ir.Script) []string {
	var out []string
	for _, c := range s.Commands() {
		out = append(out, c.Program())
	}
	return out
}

func TestSimpleCommand(t *testing.T) {
	s := parse(t, "git tag v1.0")
	if len(s.Pipelines) != 1 || len(s.Pipelines[0].Commands) != 1 {
		t.Fatalf("shape: %d pipelines", len(s.Pipelines))
	}
	if got := s.Pipelines[0].Commands[0].Argv; !reflect.DeepEqual(got, []string{"git", "tag", "v1.0"}) {
		t.Errorf("argv = %v", got)
	}
}

func TestSequenceOperators(t *testing.T) {
	s := parse(t, "cd src & git tag v1 && echo ok || echo fail")
	if got := programs(s); !reflect.DeepEqual(got, []string{"cd", "git", "echo", "echo"}) {
		t.Fatalf("programs = %v", got)
	}
	var conns []ir.Connector
	for _, p := range s.Pipelines {
		conns = append(conns, p.Connector)
	}
	want := []ir.Connector{ir.ConnNone, ir.ConnSeq, ir.ConnAnd, ir.ConnOr}
	if !reflect.DeepEqual(conns, want) {
		t.Errorf("connectors = %v, want %v", conns, want)
	}
}

func TestPipe(t *testing.T) {
	s := parse(t, "dir | sort | more")
	if len(s.Pipelines) != 1 || len(s.Pipelines[0].Commands) != 3 {
		t.Fatalf("want one 3-command pipeline, got %+v", s.Pipelines)
	}
}

func TestParenGroupFlattened(t *testing.T) {
	s := parse(t, "(echo a & git tag v1)")
	if got := programs(s); !reflect.DeepEqual(got, []string{"echo", "git"}) {
		t.Errorf("programs = %v", got)
	}
}

func TestQuotedArgument(t *testing.T) {
	s := parse(t, `cmd /c "git tag v1"`)
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"cmd", "/c", "git tag v1"}) {
		t.Errorf("argv = %v", c.Argv)
	}
}

func TestCaretEscapeNotAnOperator(t *testing.T) {
	s := parse(t, "echo a^&b")
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"echo", "a&b"}) {
		t.Errorf("argv = %v, want [echo a&b] (the ^& is literal)", c.Argv)
	}
}

func TestPercentExpansionKeptLiteral(t *testing.T) {
	s := parse(t, "echo %PATH%")
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"echo", "%PATH%"}) {
		t.Errorf("argv = %v (%%VAR%% kept literal; cmd resolution out of scope)", c.Argv)
	}
}

func TestRedirection(t *testing.T) {
	s := parse(t, "dir > out.txt")
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"dir"}) {
		t.Errorf("argv = %v (redirect target leaked into argv)", c.Argv)
	}
	if len(c.Redirects) != 1 || c.Redirects[0].Target != "out.txt" {
		t.Errorf("redirects = %+v", c.Redirects)
	}
}

func TestFileDescriptorRedirNotInArgv(t *testing.T) {
	s := parse(t, "dir 2>&1")
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"dir"}) {
		t.Errorf("argv = %v, want [dir] (the 2 fd should not be an arg)", c.Argv)
	}
}

func TestShells(t *testing.T) {
	if got := New().Shells(); len(got) != 1 || got[0] != ir.ShellCmd {
		t.Errorf("Shells() = %v", got)
	}
}
