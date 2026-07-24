package shell

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

func parse(t *testing.T, sh ir.Shell, src string) *ir.Script {
	t.Helper()
	s, err := New().Parse(context.Background(), sh, src)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", src, err)
	}
	if s == nil {
		t.Fatalf("Parse(%q) returned nil script", src)
	}
	return s
}

// programs lists argv[0] of every command, nested included, in walk order.
func programs(s *ir.Script) []string {
	var out []string
	for _, c := range s.Commands() {
		out = append(out, c.Program())
	}
	return out
}

func TestSimpleCommand(t *testing.T) {
	s := parse(t, ir.ShellBash, "go test ./...")
	if len(s.Pipelines) != 1 || len(s.Pipelines[0].Commands) != 1 {
		t.Fatalf("shape = %d pipelines", len(s.Pipelines))
	}
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"go", "test", "./..."}) {
		t.Errorf("argv = %v", c.Argv)
	}
}

func TestAssignmentPrefix(t *testing.T) {
	s := parse(t, ir.ShellBash, "FOO=bar CGO_ENABLED=0 go build")
	c := s.Pipelines[0].Commands[0]
	want := []ir.Assignment{{Name: "FOO", Value: "bar"}, {Name: "CGO_ENABLED", Value: "0"}}
	if !reflect.DeepEqual(c.Assignments, want) {
		t.Errorf("assignments = %+v", c.Assignments)
	}
	if c.Program() != "go" {
		t.Errorf("program = %q, want go", c.Program())
	}
}

func TestConnectors(t *testing.T) {
	s := parse(t, ir.ShellBash, "a && b || c ; d")
	if got := programs(s); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("programs = %v", got)
	}
	var conns []ir.Connector
	for _, p := range s.Pipelines {
		conns = append(conns, p.Connector)
	}
	want := []ir.Connector{ir.ConnNone, ir.ConnAnd, ir.ConnOr, ir.ConnSeq}
	if !reflect.DeepEqual(conns, want) {
		t.Errorf("connectors = %v, want %v", conns, want)
	}
}

func TestPipeline(t *testing.T) {
	s := parse(t, ir.ShellBash, "cat x | grep y | wc -l")
	if len(s.Pipelines) != 1 {
		t.Fatalf("want 1 pipeline, got %d", len(s.Pipelines))
	}
	cmds := s.Pipelines[0].Commands
	if len(cmds) != 3 {
		t.Fatalf("want 3 piped commands, got %d", len(cmds))
	}
	got := []string{cmds[0].Program(), cmds[1].Program(), cmds[2].Program()}
	if !reflect.DeepEqual(got, []string{"cat", "grep", "wc"}) {
		t.Errorf("piped programs = %v", got)
	}
}

func TestCommandSubstitutionNested(t *testing.T) {
	s := parse(t, ir.ShellBash, "echo $(go build ./...)")
	if got := programs(s); !reflect.DeepEqual(got, []string{"echo", "go"}) {
		t.Fatalf("programs = %v, want [echo go] (command substitution captured as nested)", got)
	}
}

func TestBacktickSubstitutionNested(t *testing.T) {
	s := parse(t, ir.ShellBash, "echo `go build`")
	if got := programs(s); !reflect.DeepEqual(got, []string{"echo", "go"}) {
		t.Fatalf("programs = %v, want [echo go]", got)
	}
}

func TestProcessSubstitutionNested(t *testing.T) {
	s := parse(t, ir.ShellBash, "diff <(go test) f")
	found := false
	for _, p := range programs(s) {
		if p == "go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected `go` inside process substitution, programs = %v", programs(s))
	}
}

func TestBackgroundAndSequence(t *testing.T) {
	s := parse(t, ir.ShellBash, "go test & echo done")
	if got := programs(s); !reflect.DeepEqual(got, []string{"go", "echo"}) {
		t.Errorf("programs = %v, want [go echo]", got)
	}
	if !s.Pipelines[0].Background {
		t.Error("first pipeline should be marked Background")
	}
}

func TestSubshell(t *testing.T) {
	s := parse(t, ir.ShellBash, "(cd build && go test)")
	if got := programs(s); !reflect.DeepEqual(got, []string{"cd", "go"}) {
		t.Errorf("programs = %v", got)
	}
}

func TestCompoundCommandFindsInnerCommands(t *testing.T) {
	s := parse(t, ir.ShellBash, "if true; then go test; fi")
	found := false
	for _, p := range programs(s) {
		if p == "go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected to find `go` inside if-clause, programs = %v", programs(s))
	}
}

func TestVariableResolutionFromScript(t *testing.T) {
	// An in-script assignment resolves in later commands.
	s := parse(t, ir.ShellBash, "t=test; go $t ./...")
	c := s.Pipelines[len(s.Pipelines)-1].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"go", "test", "./..."}) {
		t.Errorf("argv = %v, want [go test ./...] ($t resolved from assignment)", c.Argv)
	}
}

func TestVariableResolutionFromEnv(t *testing.T) {
	t.Setenv("SUBCMD", "test")
	s := parse(t, ir.ShellBash, "go $SUBCMD")
	c := s.Pipelines[0].Commands[0]
	if !reflect.DeepEqual(c.Argv, []string{"go", "test"}) {
		t.Errorf("argv = %v, want [go test] ($SUBCMD from env)", c.Argv)
	}
}

func TestUnknownVariableResolvesEmpty(t *testing.T) {
	s := parse(t, ir.ShellBash, "go $NOPE_UNDEFINED")
	c := s.Pipelines[0].Commands[0]
	// $NOPE_UNDEFINED expands to "" and drops out of argv (not blocked).
	if !reflect.DeepEqual(c.Argv, []string{"go"}) {
		t.Errorf("argv = %v, want [go] (unknown var → empty)", c.Argv)
	}
}

func TestRedirect(t *testing.T) {
	s := parse(t, ir.ShellBash, "go test > out.txt")
	c := s.Pipelines[0].Commands[0]
	if len(c.Redirects) != 1 || c.Redirects[0].Target != "out.txt" {
		t.Fatalf("redirects = %+v", c.Redirects)
	}
}

// A bare redirection (`> file`, `>> file`, `< file`) is a valid statement with
// NO command word: mvdan.cc/sh parses it to a *syntax.Stmt whose Cmd is nil.
// Lowering used to fall through to lowerCompound and hand that nil to
// syntax.Walk, which panics ("syntax.Walk: unexpected node type <nil>") — a
// guard that CRASHES on valid input blocks the command outright, which is
// strictly worse than missing a rule.
func TestBareRedirectionHasNoCommandWord(t *testing.T) {
	for _, src := range []string{"> out.txt", ">> out.txt", "< in.txt", "2> err.txt"} {
		t.Run(src, func(t *testing.T) {
			s := parse(t, ir.ShellBash, src)
			cmds := s.Commands()
			if len(cmds) != 1 {
				t.Fatalf("Parse(%q) commands = %d, want 1", src, len(cmds))
			}
			c := cmds[0]
			if c.Program() != "" {
				t.Errorf("Program() = %q, want \"\" (no command word)", c.Program())
			}
			if len(c.Argv) != 0 {
				t.Errorf("Argv = %v, want empty", c.Argv)
			}
			if len(c.Redirects) != 1 {
				t.Fatalf("Redirects = %+v, want 1", c.Redirects)
			}
		})
	}
}

// The bare redirection must not poison the pipelines around it either: the
// statement it sits next to still has to lower normally.
func TestBareRedirectionInSequence(t *testing.T) {
	s := parse(t, ir.ShellBash, "> out.txt; go build ./...")
	if got, want := programs(s), []string{"", "go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("programs = %#v, want %#v", got, want)
	}
}

// The shape of the real 2026-07-24 incident: a bare redirection next to a
// `while` loop. The bare redirect is valid POSIX (it truncates the file), so
// the whole command must lower cleanly and match nothing.
func TestBareRedirectBesideCompoundCommand(t *testing.T) {
	s := parse(t, ir.ShellBash, "> /tmp/fsp.txt; while read -r p; do echo \"$p\" >> /tmp/fsp.txt; done < list")
	if got := programs(s); len(got) == 0 || got[0] != "" {
		t.Fatalf("programs = %#v, want the bare redirect first with no program word", got)
	}
	if !slices.Contains(programs(s), "echo") {
		t.Errorf("programs = %#v, want the loop body's `echo` to survive lowering", programs(s))
	}
}

// noPanicCorpus is a spread of valid-but-unusual constructs. The specific
// nil-Cmd guard fixes one input; this corpus is what says whether it has
// siblings. Any panic here is the bug this file exists to prevent — a guard
// that crashes blocks the command it was meant to merely redirect.
var noPanicCorpus = []string{
	"> f", ">> f", "< f", "2> f", "&> f", ">| f", "<> f", ">&2", "2>&1",
	"> f < g 2>> h", "> f &", "! > f", "{ > f; }", "( > f )",
	"> f | cat", "cat | > f", "> f && > g", "> f || > g", "> f; > g",
	"if true; then > f; fi", "while :; do > f; done", "for x in a; do > f; done",
	"case x in a) > f;; esac", "fn() { > f; }", "until :; do > f; done",
	"$(> f)", "`> f`", "<(> f)", "x=$(> f) y", "> $HOME/f", "> ${UNSET}/f",
	"echo hi", "", " ", ";", "\n", "#comment only",
	"a=1", "a=1 b=2", "a=1; echo $a", "declare -a x", "time echo hi",
	"coproc x { echo hi; }", "select x in a; do > f; done",
	"[[ -f x ]] && > f", "((1+1)); > f", "let x=1", "trap '> f' EXIT",
	"exec > f", "exec 3< f", "echo a >&-", "> f 2>&1 | tee g",
}

func TestParseNeverPanics(t *testing.T) {
	for _, sh := range []ir.Shell{ir.ShellSh, ir.ShellBash, ir.ShellZsh, ir.ShellMksh} {
		for _, src := range noPanicCorpus {
			t.Run(string(sh)+"/"+src, func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Parse(%s, %q) panicked: %v", sh, src, r)
					}
				}()
				// A parse ERROR is fine (dialects differ); a panic is not.
				s, _ := New().Parse(context.Background(), sh, src)
				if s == nil {
					t.Fatalf("Parse(%s, %q) returned a nil script", sh, src)
				}
				_ = s.Commands()
			})
		}
	}
}

// FuzzParse looks for the siblings the corpus above cannot enumerate. Run it
// with -fuzz=FuzzParse; the seeded corpus alone runs on every `go test`.
func FuzzParse(f *testing.F) {
	for _, src := range noPanicCorpus {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		for _, sh := range []ir.Shell{ir.ShellSh, ir.ShellBash, ir.ShellZsh, ir.ShellMksh} {
			s, _ := New().Parse(context.Background(), sh, src)
			if s == nil {
				t.Fatalf("Parse(%s, %q) returned a nil script", sh, src)
			}
			_ = s.Commands()
		}
	})
}

func TestShellsCovered(t *testing.T) {
	got := New().Shells()
	want := []ir.Shell{ir.ShellSh, ir.ShellBash, ir.ShellZsh, ir.ShellMksh}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shells() = %v, want %v", got, want)
	}
}

func TestParseErrorReturnsNonNilScript(t *testing.T) {
	s, err := New().Parse(context.Background(), ir.ShellBash, "for do done $(")
	if err == nil {
		t.Skip("input parsed without error; nothing to assert")
	}
	if s == nil {
		t.Error("on parse error want a non-nil script alongside the error")
	}
}
