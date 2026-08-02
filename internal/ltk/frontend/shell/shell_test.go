package shell

import (
	"context"
	"reflect"
	"slices"
	"strings"
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
	// ir.Pipeline.Background was dropped: it had no reader outside
	// this now-removed assertion, and Match.matches never consulted it. What
	// still matters here — and is what this test pins — is that `&`
	// sequencing produces two distinct commands, both visible to the matcher.
	s := parse(t, ir.ShellBash, "go test & echo done")
	if got := programs(s); !reflect.DeepEqual(got, []string{"go", "echo"}) {
		t.Errorf("programs = %v, want [go echo]", got)
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

// isKnownLoweringCrasher is the allowlist consulted wherever this file checks
// Parse's error for the "internal error lowering" marker (TestParseNeverPanics
// and FuzzParse below). Parse's recover boundary (shell.go:75) means a panic
// during lowering no longer propagates as a panic — it comes back as an
// ordinary error carrying that marker — so a bare `recover()` in the test, or
// a fuzz body that only checks for a non-nil script, can no longer tell "no
// panic happened" from "a panic happened and was swallowed". That is the
// guard hiding the thing it guards against, one level up.
//
// The fix is to open the marker back up: any error containing it fails the
// test/fuzz UNLESS the (shell, src) pair is catalogued here as an already-known
// crasher. Matching on the panic's message text instead would not work — the
// Go runtime's "invalid memory address or nil pointer dereference" string is
// identical for every nil-deref regardless of cause, so it can't distinguish
// "the one bug we know about" from "a brand new one that happens to also be a
// nil-deref". Matching on input is precise enough to keep doing its job.
func isKnownLoweringCrasher(sh ir.Shell, src string) bool {
	// mvdan.cc/sh v3.13.1 nil-derefs on an empty parameter name, `${}`, under
	// the zsh variant only (expand/param.go:57) — filed upstream separately;
	// see TestExpanderPanicBecomesParseError below and the swift-print task.
	// Not deduplicated here by exact string: the fuzzer will find other
	// spellings that hit the same underlying nil-deref (e.g. "x${}y"), and all
	// of them are this one known bug, not a new one.
	return sh == ir.ShellZsh && strings.Contains(src, "${}")
}

// failOnUncatalogedLoweringPanic fails t if err is a recovered internal
// lowering panic that isn't already catalogued in isKnownLoweringCrasher — see
// its doc comment for why this check exists at all.
func failOnUncatalogedLoweringPanic(t *testing.T, sh ir.Shell, src string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "internal error lowering") {
		return
	}
	if isKnownLoweringCrasher(sh, src) {
		return
	}
	t.Fatalf("Parse(%s, %q) hit an uncatalogued internal lowering panic (recovered as: %v) — "+
		"this is a NEW crash the recover boundary is hiding; fix the root cause, or if it's a "+
		"duplicate of an already-filed bug, extend isKnownLoweringCrasher", sh, src, err)
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
				// A parse ERROR is fine (dialects differ); a RECOVERED panic is
				// not, unless it's already catalogued — see
				// failOnUncatalogedLoweringPanic.
				s, err := New().Parse(context.Background(), sh, src)
				failOnUncatalogedLoweringPanic(t, sh, src, err)
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
			s, err := New().Parse(context.Background(), sh, src)
			failOnUncatalogedLoweringPanic(t, sh, src, err)
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

// An empty parameter name — `${}` — is REJECTED by the bash/POSIX parsers but
// accepted by the zsh one, and then nil-derefs inside mvdan.cc/sh's expander
// (v3.13.1, expand/param.go:57). Found by FuzzParse, and a second live crash
// on the same hook path as the bare-redirection one. The frontend must turn it
// into a parse error, not a panic, so on_parse_error decides.
func TestExpanderPanicBecomesParseError(t *testing.T) {
	s, err := New().Parse(context.Background(), ir.ShellZsh, "00${}0")
	if err == nil {
		t.Fatal("want a parse error for ${}, got nil")
	}
	if !strings.Contains(err.Error(), "internal error lowering") {
		t.Errorf("error = %v, want it flagged as an internal lowering failure", err)
	}
	if s == nil {
		t.Fatal("want a non-nil script alongside the error (Frontend contract)")
	}
	if len(s.Commands()) != 0 {
		t.Errorf("a failed lowering must yield an empty script, got %+v", s.Commands())
	}
}
