package frontend

import (
	"context"
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

func TestExpandWrappers_DepthCap(t *testing.T) {
	// A frontend that always reports a nested wrapper would recurse forever
	// without the cap; ensure it terminates.
	f := &recursiveWrapper{}
	r := NewRegistry()
	r.Register(f)
	s := cmdScript(ir.ShellBash, "bash", "-c", "bash -c x")
	r.ExpandWrappers(context.Background(), s) // must return (depth-capped)
}

// recursiveWrapper always returns `bash -c x`, to exercise the depth cap.
type recursiveWrapper struct{}

func (recursiveWrapper) Shells() []ir.Shell {
	return []ir.Shell{ir.ShellBash, ir.ShellSh, ir.ShellZsh, ir.ShellMksh}
}
func (recursiveWrapper) Parse(_ context.Context, shell ir.Shell, _ string) (*ir.Script, error) {
	return cmdScript(shell, "bash", "-c", "bash -c x"), nil
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
