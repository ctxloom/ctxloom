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
// Reaching the cap now returns truncated=true (see expandWrappers) so the
// caller fails CLOSED (denies) instead of silently stopping expansion —
// hitting this cap means there is more nested structure than ltk verified,
// which is itself a strong evasion signal. Set higher than the old 4 (which
// was a soft "stop re-parsing" limit) because it is now a hard deny: ordinary
// deeply-nested-but-benign command substitutions should not trip it.
const maxWrapDepth = 8

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
// finds in s (`bash -c "…"`, `eval "…"`, `cmd /c "…"`, `pwsh -Command "…"`),
// and unwraps any argv-PREPENDING wrapper (`env …`, `timeout 5 …`, `command
// …`, …), appending the inner command to that command's Nested, so the
// evaluator (which already walks Nested) matches the real command. It
// recurses into nested scripts up to maxWrapDepth. It mutates s in place.
//
// The bool result reports whether expansion was TRUNCATED — the recursion hit
// maxWrapDepth while there was still nested structure left to descend into.
// A caller MUST treat truncated=true as fail-closed (deny the command): the
// depth cap existing at all means the walk stopped somewhere the evaluator
// never got to look, which for a guard is the same as not having looked.
func (r *Registry) ExpandWrappers(ctx context.Context, s *ir.Script) (truncated bool) {
	return r.expandWrappers(ctx, s, 0)
}

func (r *Registry) expandWrappers(ctx context.Context, s *ir.Script, depth int) (truncated bool) {
	if s == nil {
		return false
	}
	if depth >= maxWrapDepth {
		return true // fail closed: more nested structure exists, unverified
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
			// An argv-prepending wrapper's inner command is already argv, not a
			// string to re-parse — just append it as a same-shell nested command
			// so Walk (and further wrapper expansion) sees it too.
			if inner, ok := prefixWrapped(sc.Argv, s.Shell); ok {
				sc.Nested = append(sc.Nested, &ir.Script{
					Shell:     s.Shell,
					Pipelines: []ir.Pipeline{{Commands: []ir.SimpleCommand{{Argv: inner}}}},
				})
			}
			// Recurse into already-present nested scripts (substitutions) and any
			// just-added wrapper bodies (interpreter- and prefix-style alike), so
			// e.g. `env timeout 5 git …` unwraps fully.
			for _, ns := range sc.Nested {
				if r.expandWrappers(ctx, ns, depth+1) {
					truncated = true
				}
			}
		}
	}
	return truncated
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

// ---- argv-prepending wrappers (lusty-probe) --------------------------------
//
// wrapperRule (above) covers INTERPRETER wrappers, whose inner command is a
// single STRING argument that must be re-parsed (`sh -c "…"`). A different
// class of wrapper instead PREPENDS itself to an otherwise-untouched argv:
// `env git commit --no-verify`, `timeout 5 git commit --no-verify`, `command
// git commit --no-verify` all run `git commit --no-verify` as their real
// command, with `env`/`timeout`/`command` as argv[0]. Nothing needs
// re-parsing — the remaining argv already IS the inner command — so this is a
// STRIP, not a parse. Without it, a rule targeting `git commit --no-verify`
// only ever saw the outer program (`env`, `timeout`, `command`) and never
// matched, a deny-rule bypass.
//
// The wrapper set mirrors Claude Code's own Bash-tool wrapper list (env,
// command, setsid, timeout, time, nice, nohup, stdbuf, xargs): ltk runs
// inside the Claude Code harness and users move between the two guards, so a
// shared set avoids ltk and the harness disagreeing about what's
// "transparent". Per-wrapper skip semantics are best-effort — the goal is to
// correctly locate where the inner command starts for common invocations, not
// to fully replicate each tool's getopt grammar — and bias toward stripping
// MORE rather than less: an extra unwrap only WIDENS what a deny rule can
// catch (the same fail-safe direction as the deny-side isSubsequence operand
// match in package rules), whereas under-stripping leaves a real inner
// command unseen.

// prefixWrapperRule identifies an argv-prepending wrapper and how to locate
// where its inner command begins.
type prefixWrapperRule struct {
	programs []string // argv[0] basename (lowercased)
	// skip scans args (argv[1:], the wrapper's own arguments) and returns the
	// index in args where the inner command's argv begins, having stepped over
	// the wrapper's recognized options/operands. ok is false when no inner
	// command can be located (e.g. only options given, nothing after them).
	skip func(args []string) (start int, ok bool)
}

// prefixWrapperRules is intentionally disjoint from wrapperRules' program
// names (interpreter wrappers vs. argv-prepending wrappers are different
// programs), so a command is never double-expanded by both tables.
var prefixWrapperRules = []prefixWrapperRule{
	{programs: []string{"env"}, skip: skipEnv},
	{programs: []string{"command"}, skip: skipCommand},
	{programs: []string{"setsid"}, skip: skipSetsid},
	{programs: []string{"nohup"}, skip: skipNohup},
	{programs: []string{"timeout"}, skip: skipTimeout},
	{programs: []string{"nice"}, skip: skipNice},
	{programs: []string{"stdbuf"}, skip: skipStdbuf},
	{programs: []string{"time"}, skip: skipTime},
	{programs: []string{"xargs"}, skip: skipXargs},
}

// prefixWrapped reports whether argv is a recognized argv-prepending wrapper
// invocation and, if so, the inner command's argv — the wrapper's own
// program and recognized options/operands stripped off. shell is unused today
// (argv-prepending wrappers don't change dialect the way `cmd /c` or `pwsh
// -Command` do) but kept for symmetry with wrappedCommand and in case a future
// wrapper needs it.
func prefixWrapped(argv []string, _ ir.Shell) ([]string, bool) {
	if len(argv) == 0 {
		return nil, false
	}
	prog := strings.ToLower(path.Base(argv[0]))
	args := argv[1:]
	for _, rule := range prefixWrapperRules {
		if !containsFold(rule.programs, prog) {
			continue
		}
		start, ok := rule.skip(args)
		if !ok {
			return nil, false
		}
		inner := args[start:]
		if len(inner) == 0 {
			return nil, false
		}
		return inner, true
	}
	return nil, false
}

// skipEnv locates the inner command after `env`'s leading KEY=VAL assignment
// tokens and options: -i/--ignore-environment and -0/--null take no argument;
// -u/--unset, -C/--chdir, -S/--split-string each consume the following token
// as their argument (or, in `--opt=val` form, none). The first token that is
// neither an assignment nor a recognized option begins the inner command. An
// unrecognized option token is conservatively skipped by itself (bias toward
// stripping more, per the package doc above) rather than treated as the
// inner command's program name.
func skipEnv(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case isEnvAssignment(a):
			i++
			continue
		case a == "-i" || a == "--ignore-environment" || a == "-0" || a == "--null":
			i++
			continue
		case a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" || a == "-S" || a == "--split-string":
			i += 2
			continue
		case strings.HasPrefix(a, "--unset=") || strings.HasPrefix(a, "--chdir=") || strings.HasPrefix(a, "--split-string="):
			i++
			continue
		case isPosixOption(a):
			i++ // unrecognized option: skip it alone, don't guess an argument
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// isEnvAssignment reports whether tok is a `KEY=VAL` environment-assignment
// token as `env` accepts them on its command line (before the program name):
// a POSIX-ish identifier (letters/underscore, digits after the first char)
// followed by '='.
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		switch {
		case unicode.IsLetter(r) || r == '_':
		case i > 0 && unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}

// skipCommand locates the inner command after `command`'s no-argument options
// -p/-v/-V. (`command -v foo` only PRINTS foo's resolution rather than
// running it, but treating it as if foo runs is the fail-safe direction for a
// guard — see the package doc above.)
func skipCommand(args []string) (int, bool) {
	i := 0
	for i < len(args) && (args[i] == "-p" || args[i] == "-v" || args[i] == "-V") {
		i++
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// skipSetsid locates the inner command after setsid's no-argument options
// -w/--wait, -c/--ctty, -f/--fork.
func skipSetsid(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-w", "--wait", "-c", "--ctty", "-f", "--fork":
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// skipNohup locates the inner command: nohup has no options that take an
// operand-shaped argument, so only its informational --help/--version (which
// carry no inner command at all) need excluding.
func skipNohup(args []string) (int, bool) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "--version" {
		return 0, false
	}
	return 0, true
}

// skipTimeout locates the inner command after timeout's options (-s/--signal,
// -k/--kill-after take an argument; --preserve-status/--foreground/-v/
// --verbose take none) and then ONE more operand — the DURATION, which is
// mandatory and always present before the command.
func skipTimeout(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-s" || a == "--signal" || a == "-k" || a == "--kill-after":
			i += 2
			continue
		case strings.HasPrefix(a, "--signal=") || strings.HasPrefix(a, "--kill-after="):
			i++
			continue
		case a == "--preserve-status" || a == "--foreground" || a == "-v" || a == "--verbose":
			i++
			continue
		case isPosixOption(a):
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false // no duration/command left
	}
	i++ // the DURATION operand
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// skipNice locates the inner command after nice's -n/--adjustment ADJ (which
// takes an argument) and the bare `-N` adjustment shorthand (e.g. `nice -5
// cmd`).
func skipNice(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-n" || a == "--adjustment":
			i += 2
			continue
		case strings.HasPrefix(a, "--adjustment="):
			i++
			continue
		case isNiceAdjustment(a):
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// isNiceAdjustment reports whether tok is nice's bare adjustment shorthand
// (`-5`, `-20`): a leading dash followed only by digits.
func isNiceAdjustment(tok string) bool {
	if len(tok) < 2 || tok[0] != '-' {
		return false
	}
	for _, r := range tok[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// skipStdbuf locates the inner command after stdbuf's -i/-o/-e (each takes an
// argument: the buffering mode).
func skipStdbuf(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-i" || a == "-o" || a == "-e" || a == "--input" || a == "--output" || a == "--error":
			i += 2
			continue
		case strings.HasPrefix(a, "--input=") || strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--error="):
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// skipTime locates the inner command after `time`'s leading options.
// Conservative/best-effort: covers GNU /usr/bin/time's -o/--output FILE,
// -f/--format FMT (argument-taking) and -a/--append, -v/--verbose, -q/--quiet,
// --portability, -p/--portability (no argument); any other unrecognized
// option token is skipped alone (fail-safe: bias toward stripping more).
func skipTime(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-o" || a == "--output" || a == "-f" || a == "--format":
			i += 2
			continue
		case strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "--format="):
			i++
			continue
		case a == "-p" || a == "--portability" || a == "-a" || a == "--append" ||
			a == "-v" || a == "--verbose" || a == "-q" || a == "--quiet":
			i++
			continue
		case isPosixOption(a):
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}

// skipXargs locates the inner command after xargs' options: -n/-P/-I/-d/-s/
// -E/-a (and their long forms) take an argument; -0/-r/-t/-p and their long
// forms take none. xargs without an explicit command reads a default one
// (usually /bin/echo), which is out of scope here — a bare `xargs` with only
// options/no command is reported as "no inner command" (ok=false).
func skipXargs(args []string) (int, bool) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-n" || a == "-P" || a == "-I" || a == "-d" || a == "-s" || a == "-E" || a == "-a" ||
			a == "--max-args" || a == "--max-procs" || a == "--replace" || a == "--delimiter" ||
			a == "--max-chars" || a == "--eof" || a == "--arg-file":
			i += 2
			continue
		case strings.HasPrefix(a, "--max-args=") || strings.HasPrefix(a, "--max-procs=") ||
			strings.HasPrefix(a, "--replace=") || strings.HasPrefix(a, "--delimiter=") ||
			strings.HasPrefix(a, "--max-chars=") || strings.HasPrefix(a, "--eof=") || strings.HasPrefix(a, "--arg-file="):
			i++
			continue
		case a == "-0" || a == "-r" || a == "-t" || a == "-p" || a == "--null" ||
			a == "--no-run-if-empty" || a == "--verbose" || a == "--interactive":
			i++
			continue
		case isPosixOption(a):
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return 0, false
	}
	return i, true
}
