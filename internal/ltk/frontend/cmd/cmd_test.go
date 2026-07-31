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

// TestUnterminatedQuoteKeepsRestOfLineLiteral pins the behaviour U069-F03
// reads as a defect: an unterminated `"` swallows the rest of the line into
// one word, so `del` is not surfaced as a further command.
//
// That is what cmd.exe itself does. Its parser tracks quote state across the
// whole line, and `&` `|` `(` `)` `<` `>` are not special while that state is
// open, so an odd number of quotes leaves the remainder literal and no second
// command ever runs. Splitting here would make ltk match rules against a
// command cmd.exe will not execute — a false denial invented by ltk's own
// parser. Do not "fix" this by re-lexing the tail; see U069-F03.
func TestUnterminatedQuoteKeepsRestOfLineLiteral(t *testing.T) {
	s := parse(t, `echo "hi & del /f /q important-file`)
	if got := programs(s); !reflect.DeepEqual(got, []string{"echo"}) {
		t.Fatalf("programs = %v, want [echo]: the unterminated quote keeps the tail literal in cmd.exe too", got)
	}
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"echo", "hi & del /f /q important-file"}) {
		t.Errorf("argv = %v, want the whole tail as one word", c.Argv)
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

// TestSameLineSetIsNotVariableIndirection records what U069-F04's cited
// evasion actually does. cmd.exe expands every %VAR% on a line when it READS
// the line, before running any part of it, so `%X%` here takes X's value from
// before `set X=go` ran — and an unset name is left verbatim rather than
// expanding to nothing. The line therefore runs `set X=go` and then the
// literal `%X% test`, which is not `go test` by any route.
//
// ltk keeping %X% literal is the same outcome, so this particular string is
// not a live bypass. Variable resolution in the cmd frontend is a real gap
// (the sibling shell frontend resolves), but it is a gap about names ALREADY
// set in the environment, not about same-line indirection.
func TestSameLineSetIsNotVariableIndirection(t *testing.T) {
	s := parse(t, "set X=go& %X% test")
	if got := programs(s); !reflect.DeepEqual(got, []string{"set", "%X%"}) {
		t.Errorf("programs = %v, want [set %%X%%]", got)
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

// TestUnmatchedCloseParenErrors pins U069-F01/F02: a stray ')' at the top
// level (no enclosing '(') must be reported as a parse error, not silently
// swallow every token that follows it. Before the fix, Parse never returned
// an error at all, and parseSequence simply stopped at the ')', dropping
// `del /f /q important-file` from the IR with no trace — a deny rule
// targeting `del` never even saw it, while real cmd.exe has no such grouping
// there and would still run it.
func TestUnmatchedCloseParenErrors(t *testing.T) {
	s, err := New().Parse(context.Background(), ir.ShellCmd, "echo hi) & del /f /q important-file")
	if err == nil {
		t.Fatal("an unmatched ')' at the top level must be a parse error, not silently accepted")
	}
	// What was salvaged before the error must still be the leading command
	// (fail-safe direction: don't lose what WAS understood).
	if got := programs(s); len(got) != 1 || got[0] != "echo" {
		t.Errorf("salvaged programs = %v, want [echo]", got)
	}
}
