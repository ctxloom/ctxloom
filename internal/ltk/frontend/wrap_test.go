package frontend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// fakeFrontend parses by recording the (shell, src) it was given and returning a
// single-command script whose program is the first whitespace token of src.
type fakeFrontend struct {
	shells []ir.Shell
	seen   []string // "shell|src" for each Parse call
}

func (f *fakeFrontend) Shells() []ir.Shell { return f.shells }

func (f *fakeFrontend) Parse(_ context.Context, shell ir.Shell, src string) (*ir.Script, error) {
	f.seen = append(f.seen, string(shell)+"|"+src)
	return &ir.Script{
		Shell:     shell,
		Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: strings.Fields(src)}}}},
	}, nil
}

// cmdScript builds a one-command script with the given argv.
func cmdScript(shell ir.Shell, argv ...string) *ir.Script {
	return &ir.Script{Shell: shell, Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: argv}}}}}
}

func newReg(f Frontend) *Registry {
	r := NewRegistry()
	r.Register(f)
	return r
}

func nestedPrograms(s *ir.Script) []string {
	var out []string
	for _, c := range s.Commands() {
		out = append(out, c.Program())
	}
	return out
}

func TestExpandWrappers_BashDashC(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	// bash -c "go test" — the inner "go test" is a single argv token.
	s := cmdScript(ir.ShellBash, "bash", "-c", "go test")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "go") {
		t.Fatalf("inner `go` not surfaced; programs=%v seen=%v", got, f.seen)
	}
}

// TestExpandWrappers_MixedCaseProgramName pins that wrapper detection matches
// argv[0] regardless of case, for BOTH wrapper families: wrappedCommand
// (interpreter wrappers like bash -c) and prefixWrapped (argv-prepending
// wrappers like env). This is the behavior containsFold's own case-folding is
// there to preserve (U068-F10) — argv[0] is already lowercased via
// strings.ToLower(path.Base(argv[0])) before either family's program-list
// lookup runs, so a simplification of that lookup must not regress this.
func TestExpandWrappers_MixedCaseProgramName(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "BASH", "-c", "go test")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "go") {
		t.Fatalf("mixed-case interpreter wrapper %q not surfaced; programs=%v seen=%v", "BASH", got, f.seen)
	}

	f2 := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r2 := newReg(f2)
	s2 := cmdScript(ir.ShellBash, "Env", "git", "push")
	r2.ExpandWrappers(context.Background(), s2)
	if got := nestedPrograms(s2); !contains(got, "git") {
		t.Fatalf("mixed-case prefix wrapper %q not surfaced; programs=%v", "Env", got)
	}
}

func TestExpandWrappers_Eval(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "eval", "git", "tag", "v1")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Fatalf("eval inner not surfaced; programs=%v", got)
	}
}

func TestExpandWrappers_CmdSlashC(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellCmd}}
	r := newReg(f)
	s := cmdScript(ir.ShellCmd, "cmd", "/c", "git tag v1")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Fatalf("cmd /c inner not surfaced; programs=%v", got)
	}
}

func TestExpandWrappers_PwshCommand(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellPwsh}}
	r := newReg(f)
	// PowerShell joins everything after -Command into one command line, so the
	// inner command must be re-parsed with all its arguments, not just the first.
	s := cmdScript(ir.ShellPwsh, "pwsh", "-Command", "rm", "-rf", "build")
	r.ExpandWrappers(context.Background(), s)
	want := "pwsh|rm -rf build"
	if !contains(f.seen, want) {
		t.Errorf("inner command should carry all args; want parse of %q, seen=%v", want, f.seen)
	}
}

func TestExpandWrappers_PwshDashCAbbreviation(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellPwsh}}
	r := newReg(f)
	// pwsh documents -c as shorthand for -Command; agents use it routinely.
	s := cmdScript(ir.ShellPwsh, "pwsh", "-c", "git", "tag", "v1")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Errorf("pwsh -c inner not surfaced; programs=%v", got)
	}
}

func TestExpandWrappers_CmdUpperSlashC(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellCmd}}
	r := newReg(f)
	// cmd switches are case-insensitive: /C must work like /c.
	s := cmdScript(ir.ShellCmd, "cmd", "/C", "git tag v1")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Errorf("cmd /C inner not surfaced; programs=%v", got)
	}
}

// POSIX shells bundle short options: `bash -ec '…'` is `bash -e -c '…'`, and
// the command string is the first operand after the options regardless of the
// cluster's letter order. Skipping clusters would let `bash -ec 'denied'`
// bypass every rule.
func TestExpandWrappers_PosixShortOptionClusters(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string // inner program that must surface; "" = no expansion
	}{
		{"bash -ec", []string{"bash", "-ec", "go test"}, "go"},
		{"sh -xc", []string{"sh", "-xc", "git tag v1"}, "git"},
		{"zsh -ec", []string{"zsh", "-ec", "rm -rf build"}, "rm"},
		{"c first in the cluster", []string{"bash", "-ce", "go test"}, "go"},
		{"cluster without c", []string{"bash", "-ex", "script.sh"}, ""},
		{"capital C does not count (POSIX flags are case-sensitive)", []string{"bash", "-eC", "script.sh"}, ""},
		{"non-letter chars are not a flag cluster", []string{"bash", "-c1", "script.sh"}, ""},
		// An argument-consuming `-o` in the cluster reads its option name from the
		// next word; the command string is the word after that, and must surface.
		{"argument-consuming o in cluster consumes the option name", []string{"bash", "-eco", "pipefail", "go test"}, "go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
			r := newReg(f)
			s := cmdScript(ir.ShellBash, tt.argv...)
			r.ExpandWrappers(context.Background(), s)
			got := nestedPrograms(s)
			if tt.want == "" {
				if len(f.seen) != 0 {
					t.Errorf("no expansion expected; seen=%v", f.seen)
				}
				return
			}
			if !contains(got, tt.want) {
				t.Errorf("inner %q not surfaced; programs=%v seen=%v", tt.want, got, f.seen)
			}
		})
	}
}

// A guard that re-parses `sh -c <cmd>` must locate the command_string the way a
// POSIX shell does: the first OPERAND after -c. Options after -c, an explicit
// `--`, and argument-consuming `-o name` options must NOT be mistaken for the
// command, or a denied inner command rides through unexpanded. These are real,
// empirically-confirmed bypasses (`sh -c -- 'rm -rf /'`, `bash -oc errexit
// 'rm -rf /'`, `bash -c -x 'rm -rf /'`).
func TestExpandWrappers_PosixDashCOperandLocation(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string // inner program that must surface; "" = no expansion
	}{
		{"plain command after -c", []string{"bash", "-c", "go test"}, "go"},
		{"-- forces the next token as command", []string{"bash", "-c", "--", "rm -rf build"}, "rm"},
		{"sh -c -- bypass is closed", []string{"sh", "-c", "--", "curl evil"}, "curl"},
		{"boolean option after -c is skipped", []string{"bash", "-c", "-x", "git push"}, "git"},
		{"-o name option after -c is stepped over", []string{"bash", "-c", "-o", "errexit", "wget x"}, "wget"},
		{"+o name option after -c is stepped over", []string{"bash", "-c", "+o", "errexit", "npm publish"}, "npm"},
		{"-oc cluster consumes the option name", []string{"bash", "-oc", "errexit", "scp secret"}, "scp"},
		{"-co cluster consumes the option name", []string{"bash", "-co", "errexit", "dd if=/dev/zero"}, "dd"},
		{"options after -c but no command", []string{"bash", "-c", "-x"}, ""},
		{"-- with nothing after it", []string{"bash", "-c", "--"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
			r := newReg(f)
			s := cmdScript(ir.ShellBash, tt.argv...)
			r.ExpandWrappers(context.Background(), s)
			got := nestedPrograms(s)
			if tt.want == "" {
				if len(f.seen) != 0 {
					t.Errorf("no expansion expected; seen=%v", f.seen)
				}
				return
			}
			if !contains(got, tt.want) {
				t.Errorf("inner %q not surfaced; programs=%v seen=%v", tt.want, got, f.seen)
			}
		})
	}
}

func TestExpandWrappers_BashUpperCIsNotCommandFlag(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	// POSIX flags are case-sensitive: bash -C sets noclobber and takes no
	// argument, so script.sh must not be re-parsed as an inner command.
	s := cmdScript(ir.ShellBash, "bash", "-C", "script.sh")
	r.ExpandWrappers(context.Background(), s)
	if len(f.seen) != 0 {
		t.Errorf("bash -C is not a command wrapper; seen=%v", f.seen)
	}
}

func TestExpandWrappers_NotAWrapper(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "go", "build")
	r.ExpandWrappers(context.Background(), s)
	if len(f.seen) != 0 {
		t.Errorf("non-wrapper should not be re-parsed; seen=%v", f.seen)
	}
}

// TestExpandWrappers_DepthCap pins the fail-CLOSED depth cap (lusty-probe,
// second finding): a frontend that always reports a nested wrapper would
// recurse forever without the cap, so first ensure it terminates, then assert
// the cap is reported as truncated=true — the signal app.Decide uses to deny
// the command rather than silently evaluating an incomplete expansion.
func TestExpandWrappers_DepthCap(t *testing.T) {
	f := &recursiveWrapper{}
	r := NewRegistry()
	r.Register(f)
	s := cmdScript(ir.ShellBash, "bash", "-c", "bash -c x")
	truncated, _ := r.ExpandWrappers(context.Background(), s) // must return (depth-capped)
	if !truncated {
		t.Error("hitting maxWrapDepth must report truncated=true (fail closed), not silently stop")
	}
}

// TestExpandWrappers_NotTruncatedWhenShallow is the negative control: normal,
// shallow wrapping must NOT report truncated.
func TestExpandWrappers_NotTruncatedWhenShallow(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "bash", "-c", "go test")
	if truncated, _ := r.ExpandWrappers(context.Background(), s); truncated {
		t.Error("ordinary shallow wrapping must not report truncated")
	}
}

// recursiveWrapper always returns `bash -c x`, to exercise the depth cap.
type recursiveWrapper struct{}

func (recursiveWrapper) Shells() []ir.Shell {
	return []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}
}
func (recursiveWrapper) Parse(_ context.Context, shell ir.Shell, _ string) (*ir.Script, error) {
	return cmdScript(shell, "bash", "-c", "bash -c x"), nil
}

// ---- argv-prepending wrappers (lusty-probe) --------------------------------
//
// Unlike the interpreter wrappers above (whose inner command is a STRING that
// must be re-parsed), these wrap by PREPENDING themselves to an otherwise
// intact argv: `env git commit --no-verify` really runs `git commit
// --no-verify`. Before the fix, ExpandWrappers had no handling for this
// class at all, so a rule targeting `git commit --no-verify` never saw past
// the outer `env`/`timeout`/`command` program — a demonstrated, empirically
// confirmed deny-rule bypass (`allowed=true` for all three).

// TestExpandWrappers_EnvPrefix is the first demonstrated bypass: `env git
// commit --no-verify` must surface `git` (with its full argv) as a nested
// command so a `[git, commit, --no-verify]` deny rule can match it.
func TestExpandWrappers_EnvPrefix(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "env", "git", "commit", "--no-verify")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Fatalf("`env git commit --no-verify` must surface inner `git`; programs=%v", got)
	}
}

// TestExpandWrappers_TimeoutPrefix is the second demonstrated bypass:
// `timeout 5 git commit --no-verify` must skip the mandatory DURATION operand
// and surface `git`.
func TestExpandWrappers_TimeoutPrefix(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "timeout", "5", "git", "commit", "--no-verify")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Fatalf("`timeout 5 git commit --no-verify` must surface inner `git`; programs=%v", got)
	}
}

// TestExpandWrappers_CommandPrefix is the third demonstrated bypass:
// `command git commit --no-verify` must surface `git`.
func TestExpandWrappers_CommandPrefix(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "command", "git", "commit", "--no-verify")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Fatalf("`command git commit --no-verify` must surface inner `git`; programs=%v", got)
	}
}

// TestExpandWrappers_EnvAssignmentsAndOptions exercises the harder env case:
// leading KEY=VAL assignments plus an argument-taking option (-u NAME) before
// the real command, both of which must be stepped over (not mistaken for the
// inner program).
func TestExpandWrappers_EnvAssignmentsAndOptions(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "env", "FOO=bar", "-u", "X", "git", "commit", "--no-verify")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Fatalf("`env FOO=bar -u X git commit --no-verify` must surface inner `git`; programs=%v", got)
	}
}

// TestExpandWrappers_PrefixWrapperSet covers the remaining wrappers in the
// Claude-Code-aligned set (setsid, nice, nohup, stdbuf, time, xargs), each
// with a representative option form that must be skipped en route to the
// inner command.
func TestExpandWrappers_PrefixWrapperSet(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"setsid plain", []string{"setsid", "git", "push"}, "git"},
		{"setsid -w", []string{"setsid", "-w", "git", "push"}, "git"},
		{"nice plain", []string{"nice", "git", "push"}, "git"},
		{"nice -n ADJ", []string{"nice", "-n", "10", "git", "push"}, "git"},
		{"nice bare -N", []string{"nice", "-10", "git", "push"}, "git"},
		{"nohup plain", []string{"nohup", "git", "push"}, "git"},
		{"stdbuf -o", []string{"stdbuf", "-o0", "git", "push"}, "git"}, // glued -o0: skipped as an unrecognized option, inner command still located
		{"stdbuf -o L", []string{"stdbuf", "-o", "L", "git", "push"}, "git"},
		{"time plain", []string{"time", "git", "push"}, "git"},
		{"time -p", []string{"time", "-p", "git", "push"}, "git"},
		{"xargs plain", []string{"xargs", "git", "push"}, "git"},
		{"xargs -n 1", []string{"xargs", "-n", "1", "git", "push"}, "git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
			r := newReg(f)
			s := cmdScript(ir.ShellBash, tt.argv...)
			r.ExpandWrappers(context.Background(), s)
			got := nestedPrograms(s)
			if tt.want == "" {
				if contains(got, "git") {
					t.Errorf("unexpected surfacing of `git`; programs=%v", got)
				}
				return
			}
			if !contains(got, tt.want) {
				t.Errorf("inner %q not surfaced; programs=%v", tt.want, got)
			}
		})
	}
}

// TestExpandWrappers_PrefixWrapperTargetableItself is a regression guard for
// the "keep both surfaces evaluable" design: appending the inner command as
// Nested (rather than rewriting Argv in place) must leave the OUTER wrapper
// command itself still visible too, so a rule targeting e.g. `timeout` still
// fires on its own.
func TestExpandWrappers_PrefixWrapperTargetableItself(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "timeout", "5", "git", "push")
	r.ExpandWrappers(context.Background(), s)
	got := nestedPrograms(s)
	if !contains(got, "timeout") {
		t.Errorf("outer `timeout` must still be visible after expansion; programs=%v", got)
	}
	if !contains(got, "git") {
		t.Errorf("inner `git` must also be visible after expansion; programs=%v", got)
	}
}

// TestExpandWrappers_PrefixWrapperNoInnerCommand is a bounds check: a
// wrapper invocation with nothing left after its options must not crash or
// fabricate an inner command.
func TestExpandWrappers_PrefixWrapperNoInnerCommand(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "env", "-i")
	r.ExpandWrappers(context.Background(), s)
	if len(f.seen) != 0 || len(nestedPrograms(s)) != 1 {
		t.Errorf("`env -i` with nothing after it must not surface a fabricated inner command; programs=%v seen=%v", nestedPrograms(s), f.seen)
	}
}

// TestExpandWrappers_PrefixNotAWrapper is a regression guard: an ordinary,
// non-wrapper program must not be touched by the prefix-strip pass.
func TestExpandWrappers_PrefixNotAWrapper(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "git", "push")
	r.ExpandWrappers(context.Background(), s)
	if got := nestedPrograms(s); len(got) != 1 || got[0] != "git" {
		t.Errorf("non-wrapper command must not gain a nested surface; programs=%v", got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// erroringFrontend simulates a frontend that cannot parse its input at all
// (a genuine syntax error) — mirrors the real frontends' contract (shell.go,
// pwsh.go): a non-nil, empty Script alongside a non-nil error.
type erroringFrontend struct {
	shells []ir.Shell
}

func (f *erroringFrontend) Shells() []ir.Shell { return f.shells }

func (f *erroringFrontend) Parse(_ context.Context, shell ir.Shell, _ string) (*ir.Script, error) {
	return &ir.Script{Shell: shell}, fmt.Errorf("simulated parse failure")
}

// TestExpandWrappers_NestedParseFailureReportsUnanalyzed pins U066-F01 /
// U068-F01 / U070-F01 / U071-F01: a wrapper whose inner command string fails
// to parse must not be silently dropped from the IR with no trace. Before the
// fix, expandWrappers discarded the error from the nested r.Parse call
// entirely (`nested, _ := r.Parse(...)`), so a `bash -c "<unparseable>"` was
// indistinguishable from an ordinary, fully-understood allow — the nested
// command was simply invisible, on_parse_error included. This is the
// adversarial case: the bypass is real when the OUTER shell's wrapper
// detection is honest but the inner frontend cannot verify what is inside.
func TestExpandWrappers_NestedParseFailureReportsUnanalyzed(t *testing.T) {
	f := &erroringFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "bash", "-c", "whatever this is")
	_, unanalyzed := r.ExpandWrappers(context.Background(), s)
	if !unanalyzed {
		t.Fatal("a nested command that failed to parse must report unanalyzed=true, not silently vanish")
	}
}

// TestExpandWrappers_UnsupportedNestedShellReportsUnanalyzed: the inner
// wrapper names a dialect this build has no frontend for at all (Parse
// returns ErrUnsupportedShell, nil Script). That must ALSO surface as
// unanalyzed, not a silent no-op — an operator who typed `on_parse_error:
// deny` should not have that policy quietly skipped for anything wrapped.
func TestExpandWrappers_UnsupportedNestedShellReportsUnanalyzed(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}} // no cmd/pwsh registered
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "cmd.exe", "/c", "del /f important-file")
	_, unanalyzed := r.ExpandWrappers(context.Background(), s)
	if !unanalyzed {
		t.Fatal("a nested command in an unregistered shell dialect must report unanalyzed=true, not silently vanish")
	}
}

// TestJoinWords_PreservesAlreadyQuotedMultiWordToken pins U069-F10: a wrapper
// whose inner command is reconstructed from ALREADY-TOKENIZED argv must not
// lose word boundaries that were only visible before that tokenization.
// `cmd.exe /c bash -c "go test"` is parsed by the OUTER shell first, which
// already collapses `"go test"` into ONE argv element; a bare
// strings.Join(rest, " ") then hands the cmd frontend the flat text
// `bash -c go test`, which re-splits into FOUR words instead of three — the
// nested wrapper (bash -c) then extracts only `go` as the command string
// (POSIX -c takes the first operand), silently dropping `test`. Real cmd.exe
// suffers no such loss (it never re-tokenizes what bash already parsed), so
// this was a genuine rule-matching gap: a deny rule for `go test` (but not
// bare `go`) missed a command that really does run `go test`.
func TestJoinWords_PreservesAlreadyQuotedMultiWordToken(t *testing.T) {
	got := joinWords([]string{"bash", "-c", "go test"})
	want := `bash -c "go test"`
	if got != want {
		t.Fatalf("joinWords(%q) = %q, want %q (must re-quote the already-tokenized multi-word element)",
			[]string{"bash", "-c", "go test"}, got, want)
	}
}

// TestJoinWords_LoneWordPassesThroughUnquoted is the negative control: a
// SINGLE already-tokenized word (e.g. the whole `cmd /c "git tag v1"` payload
// handed over as one element for the destination frontend's OWN tokenizer to
// split) must not be wrapped in quotes — that would turn three words into
// one instead of preserving three.
func TestJoinWords_LoneWordPassesThroughUnquoted(t *testing.T) {
	if got := joinWords([]string{"git tag v1"}); got != "git tag v1" {
		t.Fatalf("joinWords(single word) = %q, want unquoted pass-through", got)
	}
}

// TestExpandWrappers_WindowsPathProgramName pins that argv[0] is reduced to a
// basename with BOTH separators, not just '/'. A Windows-style invocation names
// the interpreter with backslashes (`C:\Windows\System32\cmd.exe /c …`), and a
// slash-only basename leaves the whole path as the "program name", so the
// wrapper table never matches and the inner command is never re-parsed — the
// redirect silently fails to fire on a command it was written to catch.
func TestExpandWrappers_WindowsPathProgramName(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellCmd}}
	r := newReg(f)
	s := cmdScript(ir.ShellCmd, `C:\Windows\System32\cmd.exe`, "/c", "git commit --no-verify")
	r.ExpandWrappers(context.Background(), s)
	if len(f.seen) == 0 {
		t.Fatalf("backslash-pathed cmd.exe was not recognized as a wrapper; its inner command was never re-parsed")
	}
	if got := nestedPrograms(s); !contains(got, "git") {
		t.Errorf("nested programs = %v, want the inner `git` surfaced", got)
	}
}

// TestPrefixWrapped_WindowsPathProgramName is the same defect on the
// argv-prepending table: `C:\tools\timeout.exe 5 git push` really runs `git
// push`, and a slash-only basename hides it.
func TestPrefixWrapped_WindowsPathProgramName(t *testing.T) {
	inner, ok := prefixWrapped([]string{`C:\tools\timeout`, "5", "git", "push"})
	if !ok {
		t.Fatalf("backslash-pathed timeout was not recognized as an argv-prepending wrapper")
	}
	if len(inner) == 0 || inner[0] != "git" {
		t.Errorf("inner = %v, want it to start at `git`", inner)
	}
}

// TestExpandWrappers_ShellDialectDrivesWrapperRecognition pins that the set of
// programs treated as `-c` interpreter wrappers is the SAME set shellenv
// recognizes as shells. The two lists had diverged: shellenv resolves ash,
// busybox, ksh93, loksh, oksh and pdksh (and any `.exe` spelling) to a POSIX
// dialect ltk can parse, but the wrapper table named only six of them, so
// `ash -c "git commit --no-verify"` was never re-parsed and the deny rule
// never saw the command that actually runs.
func TestExpandWrappers_ShellDialectDrivesWrapperRecognition(t *testing.T) {
	for _, prog := range []string{"ash", "busybox", "ksh93", "loksh", "oksh", "pdksh", "bash.exe"} {
		t.Run(prog, func(t *testing.T) {
			f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
			r := newReg(f)
			s := cmdScript(ir.ShellBash, prog, "-c", "git commit --no-verify")
			r.ExpandWrappers(context.Background(), s)
			if len(f.seen) == 0 {
				t.Fatalf("%s -c was not recognized as a wrapper; shellenv resolves it to a shell ltk parses", prog)
			}
			if got := nestedPrograms(s); !contains(got, "git") {
				t.Errorf("nested programs = %v, want the inner `git` surfaced", got)
			}
		})
	}
}

// TestExpandWrappers_EnvSplitString pins `env -S` / `env --split-string` as an
// INTERPRETER wrapper. GNU env's -S takes one STRING and splits it into the
// command to run, so the string must be re-parsed. It had been classified as
// just another option whose argument is stepped over, which left the inner
// command out of the IR entirely: `env -S "git commit --no-verify"` produced
// no nested command at all, and every rule missed it.
func TestExpandWrappers_EnvSplitString(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"separate", []string{"env", "-S", "git commit --no-verify"}},
		{"glued", []string{"env", "-Sgit commit --no-verify"}},
		{"long-separate", []string{"env", "--split-string", "git commit --no-verify"}},
		{"long-equals", []string{"env", "--split-string=git commit --no-verify"}},
		{"after-assignments", []string{"env", "FOO=1", "-i", "-S", "git commit --no-verify"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
			r := newReg(f)
			s := cmdScript(ir.ShellBash, tc.argv...)
			r.ExpandWrappers(context.Background(), s)
			if got := nestedPrograms(s); !contains(got, "git") {
				t.Errorf("nested programs = %v, want the inner `git` surfaced (seen=%v)", got, f.seen)
			}
		})
	}
}

// TestPrefixWrapped_GluedShortOptions pins the unrecognized-option fallback on
// every prefix wrapper that has options at all. Without it, a short option
// written with its argument glued on (`stdbuf -oL`, `nice -n5`) or a bundled
// cluster (`setsid -wf`, `command -pv`) matches none of the exact-token cases,
// the scan stops there, and the OPTION token becomes the inner command's
// argv[0]. The real program is then never argv[0] of anything and no rule
// matches it — the redirect silently fails to fire.
func TestPrefixWrapped_GluedShortOptions(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"stdbuf-glued", []string{"stdbuf", "-oL", "git", "push"}},
		{"nice-glued", []string{"nice", "-n5", "git", "push"}},
		{"setsid-cluster", []string{"setsid", "-wf", "git", "push"}},
		{"command-cluster", []string{"command", "-pv", "git", "push"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, ok := prefixWrapped(tc.argv)
			if !ok {
				t.Fatalf("prefixWrapped(%v) reported no inner command", tc.argv)
			}
			if inner[0] != "git" {
				t.Errorf("inner = %v, want it to start at `git` (the option token became argv[0])", inner)
			}
		})
	}
}

// TestPrefixWrapped_GluedOptionDoesNotEatTheProgram is the negative control for
// the fallback: skipping an unrecognized option by ITSELF must not also swallow
// the program name, or the wrapper would strip too much.
func TestPrefixWrapped_GluedOptionDoesNotEatTheProgram(t *testing.T) {
	inner, ok := prefixWrapped([]string{"nice", "-n", "5", "git", "push"})
	if !ok || len(inner) != 2 || inner[0] != "git" {
		t.Fatalf("prefixWrapped(nice -n 5 git push) = %v, %v; want [git push]", inner, ok)
	}
}

// TestExpandWrappers_NestedArgvDoesNotAliasParent pins that a prefix wrapper's
// nested command OWNS its argv. The inner command was taken as a sub-slice of
// the outer command's own argv, so the two shared one backing array: writing
// through either one silently rewrote the other's view of what runs. Nothing
// in the tree mutates an Argv element today, so this is a latent hazard rather
// than a live bypass — but an IR node whose contents can change under a
// caller it does not know about is exactly the shape of a guard that reports
// on a command different from the one it decided about.
func TestExpandWrappers_NestedArgvDoesNotAliasParent(t *testing.T) {
	f := &fakeFrontend{shells: []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}}
	r := newReg(f)
	s := cmdScript(ir.ShellBash, "env", "git", "commit", "--no-verify")
	r.ExpandWrappers(context.Background(), s)

	outer := &s.Pipelines[0].Commands[0]
	if len(outer.Nested) == 0 {
		t.Fatal("no nested command was produced; the fixture never reached the aliasing path")
	}
	nested := outer.Nested[0].Pipelines[0].Commands[0]
	before := nested.Program()
	if before != "git" {
		t.Fatalf("nested program = %q, want git", before)
	}
	outer.Argv[1] = "REWRITTEN"
	if got := outer.Nested[0].Pipelines[0].Commands[0].Program(); got != "git" {
		t.Errorf("nested program changed to %q when the parent's argv was written; the two slices alias", got)
	}
}
