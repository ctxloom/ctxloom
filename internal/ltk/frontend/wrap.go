package frontend

import (
	"context"
	"path"
	"strings"
	"unicode"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/ltk/shellenv"
)

// maxWrapDepth bounds how deep ExpandWrappers re-parses nested wrappers
// (e.g. `bash -c "bash -c '…'"`), guarding against pathological input.
const maxWrapDepth = 4

// wrapperRule identifies a command that runs another command given inline — a
// trivial way to sneak a denied command past a rule. ExpandWrappers re-parses
// that inner command so rules match the real thing.
type wrapperRule struct {
	programs []string // argv[0] basename (lowercased)
	flag     string   // flag whose argument(s) are the inner command; "" = eval-style (all args)
	joinRest bool     // true: inner = all args after the flag joined; false: the single arg after it
	caseFold bool     // true: flag matches case-insensitively (cmd switches, pwsh parameters); POSIX flags are case-sensitive (-c ≠ -C)
	cluster  bool     // true: the flag also counts inside a POSIX short-option cluster (`bash -ec …` ≡ `bash -e -c …`)
}

// wrapperRules covers the common shell wrappers. Inner-command shell is derived
// from the program name via innerShell.
var wrapperRules = []wrapperRule{
	{programs: []string{"sh", "bash", "zsh", "dash", "ksh", "mksh"}, flag: "-c", joinRest: false, cluster: true},
	{programs: []string{"eval"}, flag: "", joinRest: true},
	{programs: []string{"cmd", "cmd.exe"}, flag: "/c", joinRest: true, caseFold: true},
	{programs: []string{"cmd", "cmd.exe"}, flag: "/k", joinRest: true, caseFold: true},
	// PowerShell joins everything after -Command into one command line; -c is
	// its documented shorthand.
	{programs: []string{"pwsh", "powershell", "pwsh.exe", "powershell.exe"}, flag: "-command", joinRest: true, caseFold: true},
	{programs: []string{"pwsh", "powershell", "pwsh.exe", "powershell.exe"}, flag: "-c", joinRest: true, caseFold: true},
}

// ExpandWrappers re-parses the inner command of any trivial shell wrapper it
// finds in s (`bash -c "…"`, `eval "…"`, `cmd /c "…"`, `pwsh -Command "…"`) and
// appends it to that command's Nested, so the evaluator (which already walks
// Nested) matches the real command. It recurses into nested scripts up to a
// small depth cap. It mutates s in place.
func (r *Registry) ExpandWrappers(ctx context.Context, s *ir.Script) {
	r.expandWrappers(ctx, s, 0)
}

func (r *Registry) expandWrappers(ctx context.Context, s *ir.Script, depth int) {
	if s == nil || depth >= maxWrapDepth {
		return
	}
	for pi := range s.Pipelines {
		cmds := s.Pipelines[pi].Commands
		for ci := range cmds {
			sc := &cmds[ci]
			if inner, shell, ok := wrappedCommand(sc.Argv, s.Shell); ok && inner != "" {
				// Append whatever the inner frontend salvaged even on a parse error:
				// the Frontend contract returns a non-nil (possibly partial) Script
				// alongside an error so callers can still match the commands it did
				// recover. Dropping those would fail OPEN for a guard. An unsupported
				// inner shell yields a nil Script (nothing to add).
				if nested, _ := r.Parse(ctx, shell, inner); nested != nil {
					sc.Nested = append(sc.Nested, nested)
				}
			}
			// Recurse into already-present nested scripts (substitutions) and any
			// just-added wrapper bodies.
			for _, ns := range sc.Nested {
				r.expandWrappers(ctx, ns, depth+1)
			}
		}
	}
}

// wrappedCommand returns the inner command string and the shell it is written
// in, if argv is a recognized wrapper invocation.
func wrappedCommand(argv []string, outer ir.Shell) (string, ir.Shell, bool) {
	if len(argv) == 0 {
		return "", "", false
	}
	prog := strings.ToLower(path.Base(argv[0]))
	args := argv[1:]
	for _, rule := range wrapperRules {
		if !containsFold(rule.programs, prog) {
			continue
		}
		inner, ok := rule.extract(args)
		if !ok {
			continue
		}
		return inner, innerShell(prog, outer), true
	}
	return "", "", false
}

// extract pulls the inner command string out of a wrapper's arguments.
func (rule wrapperRule) extract(args []string) (string, bool) {
	if rule.flag == "" { // eval-style: the command is all the arguments
		if len(args) == 0 {
			return "", false
		}
		return strings.Join(args, " "), true
	}
	for i, a := range args {
		if !rule.flagMatches(a) {
			continue
		}
		rest := args[i+1:]
		if rule.joinRest { // cmd /c, pwsh -Command: everything after the flag
			if len(rest) == 0 {
				return "", false
			}
			return strings.Join(rest, " "), true
		}
		// POSIX `-c`: the command string is the first OPERAND after the flag, not
		// blindly the next token. A shell skips any options that follow `-c`, honors
		// `--` (the next token is then the command even if it begins with '-'), and
		// reads an argument-consuming option's value (`-o name`, including one
		// bundled into the matched cluster like `-oc name`) from the following word.
		// Mis-locating this operand is a guard bypass — `bash -c -- 'rm -rf /'` and
		// `bash -oc errexit 'rm -rf /'` both run the inner command — so the wrapper
		// must re-parse the real one, not `--`/`-x`/the option name.
		if clusterTakesOptArg(a) { // the matched cluster's `-o` eats rest[0]
			if len(rest) == 0 {
				return "", false
			}
			rest = rest[1:]
		}
		return posixCommandOperand(rest)
	}
	return "", false
}

// posixCommandOperand returns the command_string of a POSIX `sh -c` invocation:
// scanning args, it steps over option tokens (and the argument of an
// argument-consuming `-o`/`+o` option), and on `--` takes the very next token
// verbatim. The first non-option token is the command string.
func posixCommandOperand(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "--":
			if i+1 < len(args) {
				return args[i+1], true // option terminator: next token is the command
			}
			return "", false
		case isPosixOption(tok):
			if clusterTakesOptArg(tok) {
				i++ // step over the option's argument (the `-o` option name)
			}
		default:
			return tok, true // first operand is the command string
		}
	}
	return "", false
}

// isPosixOption reports whether tok is an option token (`-x`, `--long`, `+o`)
// rather than an operand. A lone "-" (stdin) and the empty string are operands.
func isPosixOption(tok string) bool {
	return len(tok) > 1 && (tok[0] == '-' || tok[0] == '+')
}

// clusterTakesOptArg reports whether a short-option token consumes the NEXT
// token as its argument, i.e. it bundles an `-o`/`+o` (set/unset a named shell
// option, e.g. `set -o errexit`). POSIX shells read that option name from the
// following word, so the command string sits one token further along. Long
// options (`--…`) never qualify.
func clusterTakesOptArg(tok string) bool {
	if !isPosixOption(tok) || strings.HasPrefix(tok, "--") {
		return false
	}
	for _, r := range tok[1:] {
		if r == 'o' || r == 'O' {
			return true
		}
	}
	return false
}

// flagMatches reports whether arg is this rule's wrapper flag, folding case
// only where the host shell does (cmd switches, pwsh parameters). For the
// POSIX shells the flag also matches inside a bundled short-option cluster:
// `bash -ec '…'` is `bash -e -c '…'`, and skipping it would let a denied
// command ride through unexpanded.
func (rule wrapperRule) flagMatches(arg string) bool {
	if rule.caseFold {
		return strings.EqualFold(arg, rule.flag)
	}
	if arg == rule.flag {
		return true
	}
	return rule.cluster && clusterHasFlag(arg, rule.flag)
}

// clusterHasFlag reports whether arg is a POSIX bundled short-option cluster
// (single leading dash, more than one letter, all letters) carrying flag's
// option letter, e.g. "-ec" or "-xc" for "-c". The cluster's position relative
// to the inner command holds regardless of letter order: for sh/bash/zsh -c
// does not consume the next token as a getopt argument — the command string is
// the first operand after the options — so `-ce` and `-ec` behave alike.
//
// A cluster containing 'o'/'O' is an argument-consuming `set -o`-style option:
// it reads its option name from the NEXT word, so the command string sits one
// token further along. That extra token is handled by extract/posixCommandOperand
// (clusterTakesOptArg), so such clusters are accepted here rather than skipped —
// dropping them was a residual bypass (`bash -oc errexit 'rm -rf /'`).
func clusterHasFlag(arg, flag string) bool {
	if len(flag) != 2 || flag[0] != '-' {
		return false
	}
	if len(arg) <= 2 || arg[0] != '-' || arg[1] == '-' {
		return false
	}
	found := false
	for _, r := range arg[1:] {
		switch {
		case !unicode.IsLetter(r):
			return false // not a pure flag cluster (e.g. "-c5", "-o:")
		case r == rune(flag[1]):
			found = true
		}
	}
	return found
}

// innerShell maps a wrapper program to the shell its inner command is written
// in. It reuses shellenv's basename→Shell mapping (the same logic that resolves
// $SHELL), so the dialect table lives in one place; eval and anything shellenv
// doesn't recognize run in the surrounding shell.
func innerShell(prog string, outer ir.Shell) ir.Shell {
	if s := shellenv.ShellFromPath(prog); s != "" {
		return s
	}
	return outer
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
