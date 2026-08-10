// Package rules holds the YAML rule schema, its loader, and the evaluator that
// matches a parsed Script against the rules. The evaluator depends only on the
// IR, so it is shell-agnostic.
package rules

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Action is the outcome a rule (or default policy) selects.
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
)

// Config is the top-level YAML document.
type Config struct {
	Version  int      `yaml:"version"`
	Defaults Defaults `yaml:"defaults"`
	Rules    []Rule   `yaml:"rules"`
}

// Defaults control behavior when no rule fires.
type Defaults struct {
	// Shell is the fallback dialect used when neither a --shell override nor the
	// engine adapter could determine one. Empty means "no default" (the resolver
	// then sniffs, finally falling back to bash).
	Shell ir.Shell `yaml:"shell"`
	// OnParseError decides what to do when the frontend could not parse the
	// command at all (e.g. PowerShell not installed). Default allow (fail-open).
	OnParseError Action `yaml:"on_parse_error"`
	// RepeatWindowSeconds enables the "confirm by repeating" override: when a
	// command is denied, re-running the exact same command within this many
	// seconds is allowed instead. 0 (the default) disables it. It is an escape
	// hatch, not a control. The evaluator does not implement it (it is stateless
	// and pure); the CLI layer reads this and tracks state on disk.
	RepeatWindowSeconds int `yaml:"repeat_window_seconds"`
	// RepeatDelaySeconds is the default minimum wait before any `confirm` rule can
	// be confirmed by repeating: an immediate repeat is ignored until this many
	// seconds after the first denial. 0 (the default) means no delay. A per-rule
	// delay_seconds overrides it. Must be less than the effective window. It
	// removes the "repeating is quicker than complying" incentive; it is not a
	// control (a determined agent can wait).
	RepeatDelaySeconds int `yaml:"repeat_delay_seconds"`
}

// Mode controls whether and how strongly a rule fires. It collapses the older
// enabled/confirm pair into one axis.
type Mode string

const (
	// ModeEnable is the default: the rule fires and the denial is firm — it
	// cannot be lifted by repeating the command (an "inviolate" rule).
	ModeEnable Mode = "enable"
	// ModeConfirm fires the rule but lets the agent confirm by repeating the
	// exact command within the window (a time-boxed escape hatch).
	ModeConfirm Mode = "confirm"
	// ModeDisable keeps the rule in the file but inert — it never matches.
	ModeDisable Mode = "disable"
)

// Rule is a single match → action mapping.
type Rule struct {
	ID      string `yaml:"id"`
	Match   Match  `yaml:"match"`
	Action  Action `yaml:"action"` // defaults to deny
	Message string `yaml:"message"`
	Suggest string `yaml:"suggest"`
	// Mode is disable | confirm | enable (default enable). It replaces the older
	// `enabled`/`confirm` booleans:
	//   - enable  (default): rule fires; the denial is firm (inviolate).
	//   - confirm: rule fires; re-running the exact command within the window
	//              confirms and lets it through.
	//   - disable: rule never matches.
	Mode Mode `yaml:"mode"`
	// WindowSeconds is the confirm-by-repeating window for a `confirm` rule. 0
	// means "use defaults.repeat_window_seconds". Ignored for other modes.
	WindowSeconds int `yaml:"window_seconds"`
	// DelaySeconds is a minimum wait before a `confirm` rule can be confirmed: the
	// repeat is ignored until this many seconds after the first denial, then
	// honored up to the window. 0 (default) means no delay — an immediate repeat
	// works. It removes the convenience incentive to bypass (waiting is slower than
	// just running the suggested command) and breaks the reflexive instant retry;
	// it is NOT a security control (a determined agent can still wait). Must be
	// less than the effective window. Ignored for non-confirm modes.
	DelaySeconds int `yaml:"delay_seconds"`
}

func (r Rule) action() Action {
	if r.Action == "" {
		return ActionDeny
	}
	return r.Action
}

// mode returns the rule's mode, defaulting to enable.
func (r Rule) mode() Mode {
	if r.Mode == "" {
		return ModeEnable
	}
	return r.Mode
}

// isEnabled reports whether the rule participates in evaluation. Only
// `mode: disable` turns it off.
func (r Rule) isEnabled() bool {
	return r.mode() != ModeDisable
}

// confirmPolicy resolves whether a denial by this rule may be lifted by
// repeating the command, the window for doing so, and any minimum delay before
// the repeat counts, given the global defaults. Only a `confirm` rule is
// repeatable, and only when a positive window applies (per-rule window overriding
// the global default). `enable` rules are inviolate.
func (r Rule) confirmPolicy(d Defaults) (repeatable bool, windowSeconds, delaySeconds int) {
	if r.mode() != ModeConfirm {
		return false, 0, 0
	}
	windowSeconds = d.RepeatWindowSeconds
	if r.WindowSeconds > 0 {
		windowSeconds = r.WindowSeconds
	}
	delaySeconds = d.RepeatDelaySeconds
	if r.DelaySeconds > 0 {
		delaySeconds = r.DelaySeconds
	}
	return windowSeconds > 0, windowSeconds, delaySeconds
}

// CommandPattern is an argv prefix to match. In YAML it may be written as a
// scalar string (shell-split on whitespace: "sh -c" → ["sh", "-c"]) or as a
// list, taken verbatim (["go", "test"]).
type CommandPattern []string

// UnmarshalYAML accepts either a scalar string or a sequence of strings.
func (c *CommandPattern) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*c = strings.Fields(node.Value)
	case yaml.SequenceNode:
		var toks []string
		if err := node.Decode(&toks); err != nil {
			return err
		}
		*c = toks
	default:
		return fmt.Errorf("match.command: expected a string or list of strings")
	}
	return nil
}

// Match is the set of conditions a command must satisfy. All present conditions
// must hold (AND). An entirely empty Match matches nothing, to avoid accidental
// catch-all denials.
type Match struct {
	// Command matches the program plus arguments. The first token is the
	// program: it matches argv[0] exactly or by basename, so absolute paths
	// still match. Remaining tokens are classified per the command's shell into:
	//
	//   - positional args (e.g. subcommands like `test`, `commit`): order
	//     matters, but HOW they must line up against the command's non-option
	//     arguments (its operands) depends on the rule's action — see "Allow
	//     vs. deny: matching discipline" below. This is the load-bearing
	//     asymmetry in the whole model; read that section before changing
	//     anything here.
	//   - options (any flag: `-c`, `-x`, `--no-cache`, and `/c` under cmd): no
	//     implied order; each must appear somewhere in the command's arguments,
	//     and they never consume a positional slot.
	//
	// Trailing arguments are always allowed. Examples: `go` matches any `go …`;
	// `[go, test]` matches `go test …` (and, for a deny rule, `go --mod=mod
	// test …`); `sh -c` matches `sh -c …` and `sh -e -c …`; `[git, push,
	// --force, --no-verify]` matches those flags in any order after `push`.
	//
	// Bundled short options expand for matching: under a POSIX shell `-rf` is
	// treated as also carrying `-r` and `-f`, so `[rm, -r, -f]` matches `rm -rf`,
	// `rm -fr`, and `rm -r -f` alike. See expandShortClusters.
	//
	// # Allow vs. deny: matching discipline (the firewall-rule model)
	//
	// ltk's rule list is evaluated the way a packet-filter ruleset is (iptables/
	// nftables `-j ACCEPT/DROP` chains, OpenBSD pf, cloud security groups):
	// rules are walked IN ORDER and, for one command, the first that matches
	// wins (see Evaluate, which also documents how that nests inside the
	// command walk) — there is no "most specific rule wins" reranking. That
	// ordering discipline was already correct here; what was missing is the
	// other firewall-rule principle: a firewall matches on STRUCTURED fields
	// (source IP, port, protocol) with an EXPLICIT operator per field — exact
	// equality, CIDR/set membership, an anchored range — never "does this byte
	// sequence appear anywhere in the packet." `iptables --dport 22` cannot
	// match a packet merely because the byte `22` occurs somewhere in its
	// payload. Applying that discipline here means a rule's positional tokens
	// must be matched against a specific, named field (an argv POSITION) with
	// an operator appropriate to the rule's consequence, not a fuzzy
	// pattern-appears-somewhere scan.
	//
	// The two rule actions carry opposite consequences, so they get different
	// operators — this is also standard firewall practice: a narrow allow
	// carve-out in an otherwise default-deny chain is written tight (exact
	// match), while the surrounding deny/catch-all can safely be broad, because
	// broadening a DENY only widens what gets blocked (safe), while broadening
	// an ALLOW widens what gets let through (unsafe):
	//
	//   - deny rules keep ORDERED SUBSEQUENCE matching: positionals must appear
	//     in the given order among the operands, but not contiguously or
	//     leading. A value-taking option whose value lands among the operands
	//     (the matcher can't know `-C`/`--context`/`--prefix` consumes a word)
	//     cannot push the subcommand out of match position: `git -C /repo push`,
	//     `docker --context prod build`, and `npm --prefix /x run …` still
	//     match `[git, push]` / `[docker, build]` / `[npm, run]`. A positional
	//     may thus match a non-leading operand of the same spelling — that only
	//     ever widens what a deny rule catches, which is fail-safe for a guard.
	//   - allow rules use POSITION-ANCHORED STRICT PREFIX matching: every
	//     positional must equal the operand at the SAME index, starting at
	//     operand[0]. This is the fix for the fail-open bug an ordered-
	//     subsequence allow rule had: `allow: [git, status]` used to match
	//     `git commit -m status --no-verify`, because `status` — the VALUE of
	//     `-m`, not a subcommand — is an operand and or an ordered subsequence
	//     of one element ["status"] is trivially satisfied by ANY operand list
	//     containing "status" anywhere after the start. On `action: allow` that
	//     widened match doesn't just widen what's caught, it widens what's
	//     PERMITTED — the exact inverse of the deny case, so it needed the
	//     opposite (stricter) operator, not a shared one.
	//
	// A too-strict allow rule fails safe: the command just isn't allowed by
	// that rule, and falls through to the next rule / the default policy. A
	// too-loose allow rule fails open: it can clear a command a deny rule would
	// otherwise have caught. That asymmetry is why the operators differ by
	// action rather than there being one "positional matching" rule for both.
	//
	// Consequence: an allow rule whose real subcommand is pushed out of
	// operand[0] by a value-taking option (`docker --context prod build`) no
	// longer matches `command: [docker, build]` — strict prefix rejects it,
	// same as it rejects the smuggling case, because no fixed-index anchor can
	// tell the two apart without per-program argument-arity knowledge ltk
	// deliberately doesn't have. The documented escape hatch is `args_all` /
	// `args_any` (see below), which are program-agnostic set-membership checks
	// over the whole (short-cluster-expanded) argument list, not
	// position-sensitive: write `command: [docker], args_all: [build]` instead
	// of `command: [docker, build]` for an allow rule that must tolerate a
	// leading value-option.
	//
	// (ltk's overall DEFAULT policy is still allow-by-default when nothing
	// matches — unlike a network firewall's usual default-deny posture. That is
	// a separate, deliberate design choice ["a guardrail against reflexive
	// mistakes, not a security boundary" — see docs/ltk] and is unchanged here;
	// only the PER-RULE matching operator adopts firewall discipline.)
	//
	// # Tokens are LITERAL — no globs, no regular expressions
	//
	// Every token here is compared for exact string equality (the program token
	// additionally by basename). This is the opposite of match.Path below, whose
	// patterns ARE full doublestar globs, and the asymmetry is the trap: a rule
	// written `command: [git, "push*"]` does not mean "any push subcommand", it
	// means an argument spelled literally `push*`, so it silently never fires.
	// Argv reaches the matcher un-globbed (the frontends expand variables but do
	// not do filename expansion), which is why the equality is right for what
	// this field matches and also why a metacharacter cannot be reinterpreted
	// here without changing what an existing literal rule catches. Use several
	// rules, or args_any/args_all, where a pattern is wanted.
	Command CommandPattern `yaml:"command"`
	// ArgsAny / ArgsAll are program-agnostic refinements on the arguments
	// (beyond the Command prefix); bundled short options are expanded first.
	ArgsAny []string `yaml:"args_any"` // at least one present in args
	ArgsAll []string `yaml:"args_all"` // all present in args
	// Unless lists exception tokens: if the command contains any of them, the
	// rule does NOT match. This is the read-only/safe escape hatch — e.g. block
	// `git tag` `unless: [--list]` so the read-only listing form is exempt.
	Unless []string `yaml:"unless"`
	// Shells restricts the rule to these shells.
	Shells []ir.Shell `yaml:"shells"`
	// Path makes this a FILE-EDIT rule instead of a command rule: it matches when
	// a file-editing tool (Edit/Write/MultiEdit/NotebookEdit) targets a file whose
	// path matches one of these patterns. A rule is either a command rule or a path
	// rule, never both. Each pattern is one of three forms:
	//
	//   - a glob (Go path.Match), matched against the file's basename or its full
	//     slash-path. e.g. `VERSION` blocks hand-editing VERSION anywhere; `*.lock`
	//     blocks any lockfile; `dist/*` blocks one level under dist/.
	//   - a directory subtree, written with a trailing slash: `vendor/` blocks
	//     every file under any directory named vendor, at any depth. The segments
	//     are matched literally (not globbed) and bounded on slashes, so `a/b/`
	//     matches `…/a/b/x` and `…/a/b/c/x` but not `…/ab/x`. This is how you
	//     "prohibit writes to a directory".
	//   - the sentinel "@submodules", which Config.ExpandSubmodules rewrites into a
	//     directory-subtree pattern for every path in .gitmodules — so one rule
	//     blocks edits inside all git submodules without naming them. Left
	//     unexpanded (no .gitmodules) it matches nothing.
	Path []string `yaml:"path"`
}

// submodulesToken is the reserved match.path value that ExpandSubmodules rewrites
// into a directory-subtree pattern per .gitmodules entry. See Match.Path.
const submodulesToken = "@submodules"

// isPathRule reports whether this match targets file edits rather than commands.
func (m Match) isPathRule() bool { return len(m.Path) > 0 }

// mixesCommandAndPath reports whether a path rule also carries command-style
// conditions, which is a config error: a rule is one kind or the other.
func (m Match) mixesCommandAndPath() bool {
	return m.isPathRule() && (len(m.Command) > 0 || len(m.ArgsAny) > 0 ||
		len(m.ArgsAll) > 0 || len(m.Unless) > 0 || len(m.Shells) > 0)
}

func (m Match) hasConstraint() bool {
	return len(m.Command) > 0 || len(m.ArgsAny) > 0 ||
		len(m.ArgsAll) > 0 || len(m.Unless) > 0 || len(m.Shells) > 0 ||
		len(m.Path) > 0
}

// matchesPath reports whether a file-edit of file is caught by this path rule.
// Patterns are full globs (doublestar: `*`, `?`, `[…]`, `{a,b}`, and `**` which
// spans directories). Because editing tools pass absolute paths, each pattern is
// tried three ways so repo-relative patterns still fire:
//
//   - against the basename, so `*.lock` catches `/proj/a/b/x.lock`;
//   - against the full slash-path, so an absolute or exact pattern works;
//   - against the full path with an implicit `**/` prefix, so a repo-relative
//     pattern like `src/**/*.go` matches `/proj/src/a/b.go` and `dist/*` matches
//     `/proj/dist/x` (but not the deeper `dist/x/y`, since `*` stops at `/`).
//
// A trailing slash is directory sugar: `vendor/` means the whole subtree, i.e.
// `vendor/**`. The "@submodules" sentinel is inert here — ExpandSubmodules
// rewrites it into directory patterns first, and an unexpanded one matches nothing.
func (m Match) matchesPath(file string) bool {
	file = strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
	if file == "" {
		return false
	}
	base := path.Base(file)
	for _, pat := range m.Path {
		if pat == submodulesToken {
			continue // unexpanded sentinel; expand via ExpandSubmodules
		}
		if strings.HasSuffix(pat, "/") {
			pat = strings.TrimRight(pat, "/") + "/**" // directory subtree
		}
		if globMatch(pat, base) || globMatch(pat, file) || globMatch("**/"+pat, file) {
			return true
		}
	}
	return false
}

// globMatch reports whether name matches the doublestar pattern, treating a
// malformed pattern as no-match (validation surfaces such patterns elsewhere).
func globMatch(pattern, name string) bool {
	ok, err := doublestar.Match(pattern, name)
	return ok && err == nil
}

// ExpandSubmodules rewrites the "@submodules" sentinel in every path rule into a
// directory-subtree pattern (one per submodule path), so a rule written as
// `path: ["@submodules"]` blocks edits inside all of a repo's git submodules
// without naming them. submodulePaths come from .gitmodules (see internal/scm).
// With none, the sentinel is dropped — a rule left with no patterns matches
// nothing (fail-open); the caller is responsible for distinguishing "no
// submodules" from "could not read .gitmodules" before calling (see
// scm.SubmodulePaths, which now returns that error). Other patterns in the same
// list are preserved in place. It returns an error when a submodule path is not
// a valid glob pattern.
func (c *Config) ExpandSubmodules(submodulePaths []string) error {
	var dirs []string
	for _, p := range submodulePaths {
		if p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/"); p != "" {
			dirs = append(dirs, p+"/")
		}
	}
	// These patterns are injected into Match.Path AFTER Parse validated that
	// list, so they are the one class of pattern that never met
	// validatePathPatterns — and globMatch treats a malformed pattern as
	// no-match. A submodule whose path carries a glob metacharacter would
	// therefore expand into a pattern that can never fire, leaving that
	// submodule unprotected by the very rule written to protect it, with
	// nothing said anywhere. Re-run the same validation the parser runs and
	// refuse rather than expand into a dead rule.
	if err := validatePathPatterns(dirs); err != nil {
		return fmt.Errorf("expand @submodules: %w", err)
	}
	for i := range c.Rules {
		pats := c.Rules[i].Match.Path
		if !slices.Contains(pats, submodulesToken) {
			continue
		}
		expanded := make([]string, 0, len(pats)+len(dirs))
		for _, pat := range pats {
			if pat == submodulesToken {
				expanded = append(expanded, dirs...)
			} else {
				expanded = append(expanded, pat)
			}
		}
		c.Rules[i].Match.Path = expanded
	}
	return nil
}

// strictPrefix selects the operand-matching operator per the allow/deny
// discipline documented on Match.Command: true for an allow rule (position-
// anchored strict prefix), false for a deny rule (ordered subsequence, the
// permissive default). The caller (Evaluate) derives it from the rule's
// action; matches itself has no notion of "rule".
func (m Match) matches(shell ir.Shell, c ir.SimpleCommand, strictPrefix bool) bool {
	if !m.hasConstraint() {
		return false
	}
	if len(m.Shells) > 0 && !slices.Contains(m.Shells, shell) {
		return false
	}
	// The shell whose flag conventions classify THIS command's own arguments:
	// when the command's program is itself a recognized shell/interpreter
	// binary, ITS identity governs its args regardless of what dialect the
	// enclosing script declares — running `pwsh -Recurse` as a
	// plain command from a bash script must not have `-Recurse` shredded into
	// single-letter POSIX clusters just because the ENCLOSING script is bash.
	// The `shells:` constraint above stays keyed on the enclosing script,
	// unchanged: "does this command live in a cmd-dialect script" is a
	// separate question from "what convention governs its own flags".
	argShell := shell
	if s := shellForProgram(c.Program()); s != "" {
		argShell = s
	}
	if len(m.Command) > 0 && !matchCommand(m.Command, c.Argv, argShell, strictPrefix) {
		return false
	}
	return m.matchesArgs(expandShortClusters(c.Args(), argShell))
}

// matchesArgs applies the program-agnostic argument conditions — args_all,
// args_any and the unless exception list — to a command's already
// cluster-expanded arguments. They are position-insensitive set-membership
// tests, unlike the Command prefix, which is why they factor out cleanly.
func (m Match) matchesArgs(args []string) bool {
	for _, a := range m.ArgsAll {
		if !slices.Contains(args, a) {
			return false
		}
	}
	if len(m.ArgsAny) > 0 &&
		!slices.ContainsFunc(m.ArgsAny, func(a string) bool { return slices.Contains(args, a) }) {
		return false
	}
	// unless: any listed token present means this invocation is an exception
	// (e.g. a read-only `--list`/`--dry-run` form), so the rule does not match.
	return !slices.ContainsFunc(m.Unless, func(a string) bool { return slices.Contains(args, a) })
}

// shellForProgram maps program (a command's argv[0]) to the shell it itself
// is, when it is a directly recognized shell/interpreter binary. It is
// deliberately independent of shellenv.ShellFromPath: that one answers "which
// dialect is this HOST", this one answers "which dialect does this ARGV[0]
// interpret", and the two tables are free to diverge (ksh is spelled here
// without its version suffixes, for instance). path.Base is POSIX-only, so a
// Windows-style absolute path is normalized to forward slashes first — the
// same fix as programBasename above, for the same reason. Returns "" when
// program is not one of these.
func shellForProgram(program string) ir.Shell {
	name := strings.ToLower(path.Base(strings.ReplaceAll(program, `\`, "/")))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "bash":
		return ir.ShellBash
	case "sh":
		return ir.ShellSh
	case "zsh":
		return ir.ShellZsh
	case "mksh":
		return ir.ShellMksh
	// `ksh` is AT&T ksh93 on most hosts, whose process substitution and
	// C-style `for ((;;))` the MirBSDKorn variant rejects outright — and a
	// rejected inner command is one no rule ever sees. Same reasoning, and the
	// same measurement, as shellenv.ShellFromPath.
	case "ksh", "ksh93":
		return ir.ShellBash
	case "pwsh", "powershell":
		return ir.ShellPwsh
	case "cmd":
		return ir.ShellCmd
	default:
		return ""
	}
}

// programBasename returns the trailing path component of argv[0] the way the
// shell that will actually run it resolves a bare-name match. path.Base is
// POSIX-only (it splits on '/' alone), so a Windows-style absolute program
// path — `C:\Windows\System32\cmd.exe`, the normal form under the cmd/pwsh
// dialects this unit itself supports — never reduced to a bare `cmd.exe` the
// way `/usr/bin/git` already reduces to `git`: a `command: [cmd]`
// rule silently never fired against an absolute-backslash-path invocation.
// Scoped to ShellCmd/ShellPwsh so a literal backslash inside an unusual POSIX
// filename is never misread as a path separator.
func programBasename(argv0 string, shell ir.Shell) string {
	if shell == ir.ShellCmd || shell == ir.ShellPwsh {
		argv0 = strings.ReplaceAll(argv0, "\\", "/")
	}
	return path.Base(argv0)
}

// matchCommand reports whether a command pattern matches a command's argv,
// using the command's shell to classify flags. See Match.Command for the full
// model, including why the positional operator depends on strictPrefix.
//
//   - pattern[0] (the program) matches argv[0] exactly or by basename.
//   - positional pattern tokens (non-options) must appear among the command's
//     non-option arguments (operands), in order: as a position-anchored
//     strict prefix when strictPrefix is true (allow rules), or as an ordered
//     subsequence when false (deny rules, the permissive default).
//   - option pattern tokens (flags) must each appear somewhere in args, in any
//     order (with bundled short options expanded, e.g. -rf ⇒ -r, -f).
func matchCommand(pattern, argv []string, shell ir.Shell, strictPrefix bool) bool {
	if len(pattern) == 0 || len(argv) == 0 {
		return false
	}
	if argv[0] != pattern[0] && programBasename(argv[0], shell) != pattern[0] {
		return false
	}
	positionals, options := classifyArgs(pattern[1:], shell)
	args := argv[1:]
	expanded := expandShortClusters(args, shell)
	for _, opt := range options {
		if !slices.Contains(expanded, opt) {
			return false
		}
	}
	operands := classifyOperands(args, shell)
	if strictPrefix {
		return isPrefix(positionals, operands)
	}
	return isSubsequence(positionals, operands)
}

// classifyArgs splits pattern tokens into positionals (operands) and options.
func classifyArgs(toks []string, shell ir.Shell) (positionals, options []string) {
	for _, tok := range toks {
		if isOption(tok, shell) {
			options = append(options, tok)
		} else {
			positionals = append(positionals, tok)
		}
	}
	return positionals, options
}

// classifyOperands returns the non-option arguments, in order.
func classifyOperands(args []string, shell ir.Shell) []string {
	operands := make([]string, 0, len(args))
	for _, a := range args {
		if !isOption(a, shell) {
			operands = append(operands, a)
		}
	}
	return operands
}

// isSubsequence reports whether every element of sub appears in s in the same
// order, with any other elements allowed in between. This is the DENY-rule
// operand operator: permissive by design, so a value-taking option's value
// among the operands cannot shift the real subcommand out of match position.
// See Match.Command ("Allow vs. deny: matching discipline") for the full
// model and why allow rules use the strict isPrefix operator instead.
func isSubsequence(sub, s []string) bool {
	i := 0
	for _, w := range s {
		if i == len(sub) {
			break
		}
		if w == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

// isPrefix reports whether sub matches, element for element, a leading run of
// s starting at s[0] (a position-anchored strict prefix — not "sub appears as
// a contiguous substring somewhere in s"). This is the ALLOW-rule operand
// operator: an empty sub is vacuously a prefix of anything (a pattern with no
// positionals, e.g. bare options/program only, imposes no positional
// constraint). See Match.Command for why allow rules need this stricter
// operator than deny's isSubsequence.
func isPrefix(sub, s []string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i, w := range sub {
		if s[i] != w {
			return false
		}
	}
	return true
}

// isOption reports whether a token is an option (a flag) for the given shell, as
// opposed to a positional argument (a POSIX "operand"). The classification is
// shell-aware so rules stay portable:
//
//   - POSIX family (sh/bash/zsh/mksh) and PowerShell: options start with "-"
//     (e.g. -c, -x, --no-cache, -Recurse). A lone "-" is positional (stdin).
//   - cmd.exe: options conventionally start with "/" (e.g. /c, /s); "-" is also
//     accepted. (Note "/" is the switch char in cmd, whereas in POSIX it begins
//     a path, which is why this must be shell-aware.)
func isOption(tok string, shell ir.Shell) bool {
	if tok == "" || tok == "-" {
		return false
	}
	if shell == ir.ShellCmd && strings.HasPrefix(tok, "/") {
		return true
	}
	return strings.HasPrefix(tok, "-")
}

// expandShortClusters returns args plus the individual flags of any bundled
// short-option cluster (POSIX getopt convention: "-rf" also carries "-r" and
// "-f"), so a rule written with separate short flags matches a bundled
// invocation in any order. Originals are kept; nothing is rewritten.
//
// This is deliberately a *matcher-level* heuristic, not an IR transform, because
// bundling is per-program semantics the shell can't know: getopt-based tools
// (GNU/BSD coreutils) cluster, but Go's flag package treats "-rf" as one flag,
// `find` uses single-dash long options, and PowerShell uses "-LongName". So we
// only expand under POSIX shells, and only single-dash all-letter clusters.
func expandShortClusters(args []string, shell ir.Shell) []string {
	out := append([]string(nil), args...)
	for _, a := range args {
		if !isShortCluster(a, shell) {
			continue
		}
		for _, r := range a[1:] {
			out = append(out, "-"+string(r))
		}
	}
	return out
}

// isShortCluster reports whether tok is a POSIX bundled short-option cluster
// (e.g. "-rf"): a single leading dash, more than one letter, all letters. cmd
// (/switches) and PowerShell (-LongName) do not bundle, so they never qualify.
func isShortCluster(tok string, shell ir.Shell) bool {
	if shell == ir.ShellCmd || shell == ir.ShellPwsh {
		return false
	}
	if len(tok) <= 2 || tok[0] != '-' || tok[1] == '-' {
		return false
	}
	for _, r := range tok[1:] {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// Parse decodes and validates a config from YAML bytes. Unknown fields are
// rejected so that typos in a rule file surface as errors instead of being
// silently ignored.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yamlx.DecodeStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.normalizeAndValidate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Empty returns the built-in allow-all config — no rules, defaults
// normalized. It cannot fail, which is the point: this used to be produced by
// parsing a hardcoded YAML string at runtime, so its (impossible) error made
// the caller report a fail-closed deny-everything as "could not load its rules
// config", naming a path the user never wrote.
func Empty() *Config {
	var cfg Config
	// Cannot fail: a Config with no rules only normalizes defaults.
	_ = cfg.normalizeAndValidate()
	return &cfg
}

// Load reads and parses a config file.
func Load(pathname string) (*Config, error) {
	data, err := os.ReadFile(pathname)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

func (c *Config) normalizeAndValidate() error {
	if c.Defaults.OnParseError == "" {
		c.Defaults.OnParseError = ActionAllow
	}
	if err := validAction(c.Defaults.OnParseError); err != nil {
		return fmt.Errorf("defaults.on_parse_error: %w", err)
	}
	if c.Defaults.Shell != "" && !c.Defaults.Shell.Valid() {
		return fmt.Errorf("defaults.shell: unknown shell %q", c.Defaults.Shell)
	}
	seen := make(map[string]bool, len(c.Rules))
	for i := range c.Rules {
		if err := validateRule(&c.Rules[i], i, seen); err != nil {
			return err
		}
		if err := validateConfirm(&c.Rules[i], c.Defaults); err != nil {
			return err
		}
	}
	return nil
}

// validateConfirm checks a confirm-mode rule's window/delay coherence. A
// `confirm` rule needs a positive window to live in (a per-rule window_seconds
// or defaults.repeat_window_seconds): without one it can never be confirmed and
// silently behaves like an inviolate `enable` rule — the exact inverse of the
// time-boxed escape hatch `mode: confirm` is for — so it is rejected rather than
// left to mislead. A delay_seconds in turn must fit inside that window: positive
// and strictly less than it (the repeat is honored only in the band
// [delay, window] after the first denial). Non-confirm rules are unaffected.
func validateConfirm(r *Rule, d Defaults) error {
	if r.mode() != ModeConfirm {
		return nil
	}
	repeatable, window, delay := r.confirmPolicy(d)
	if !repeatable {
		return fmt.Errorf("rule %q: mode confirm needs a window; set window_seconds or defaults.repeat_window_seconds", r.ID)
	}
	if delay <= 0 {
		return nil
	}
	if delay >= window {
		return fmt.Errorf("rule %q: delay_seconds (%d) must be less than the confirm window (%d)", r.ID, delay, window)
	}
	return nil
}

// validateRule checks one rule and records its id in seen. The three stages
// are ordered: nothing downstream may report an error against a rule whose id
// is not yet known to be usable in that message.
func validateRule(r *Rule, index int, seen map[string]bool) error {
	if err := validateRuleIdentity(r, index, seen); err != nil {
		return err
	}
	if err := validateMatchShape(r); err != nil {
		return err
	}
	return validateDenyIsExplained(r)
}

// validateRuleIdentity checks the fields that name a rule and select its
// behaviour — id (present and unique), action, mode — and records the id.
func validateRuleIdentity(r *Rule, index int, seen map[string]bool) error {
	if r.ID == "" {
		return fmt.Errorf("rule #%d: missing id", index)
	}
	if seen[r.ID] {
		return fmt.Errorf("duplicate rule id %q", r.ID)
	}
	seen[r.ID] = true
	if err := validAction(r.action()); err != nil {
		return fmt.Errorf("rule %q: %w", r.ID, err)
	}
	if err := validMode(r.mode()); err != nil {
		return fmt.Errorf("rule %q: %w", r.ID, err)
	}
	return nil
}

// validateMatchShape checks that the rule's match is a well-formed instance of
// exactly one of the two kinds a Match can be — a command rule or a file-edit
// (path) rule — and that each condition it does carry is usable.
func validateMatchShape(r *Rule) error {
	if !r.Match.hasConstraint() {
		return fmt.Errorf("rule %q: match has no conditions", r.ID)
	}
	if r.Match.mixesCommandAndPath() {
		return fmt.Errorf("rule %q: match.path cannot be combined with command/args/shells", r.ID)
	}
	if slices.Contains(r.Match.Command, "") {
		return fmt.Errorf("rule %q: empty token in match.command", r.ID)
	}
	if err := validatePathPatterns(r.Match.Path); err != nil {
		return fmt.Errorf("rule %q: %w", r.ID, err)
	}
	for _, sh := range r.Match.Shells {
		if !sh.Valid() {
			return fmt.Errorf("rule %q: unknown shell %q in match.shells", r.ID, sh)
		}
	}
	return nil
}

// validateDenyIsExplained rejects a denial with nothing to say. Such a rule is
// a guard that fires and communicates nothing: engine.Response.Message()
// returns "" when both are empty, so the hook response carries no
// permissionDecisionReason at all and the agent cannot tell the user why it was
// blocked or what to do instead — it just sees "deny" and retries. `suggest`
// alone is enough (it renders as "Use instead: …"). Only rules that can
// actually deny are held to this: an allow rule explains nothing by design, and
// a `mode: disable` rule never fires (enabling it is the loud moment, and this
// check fires then).
func validateDenyIsExplained(r *Rule) error {
	if r.isEnabled() && r.action() == ActionDeny &&
		strings.TrimSpace(r.Message) == "" && strings.TrimSpace(r.Suggest) == "" {
		return fmt.Errorf("rule %q: a deny rule needs a message (or a suggest) — without one the agent is told %q with no reason and no alternative", r.ID, "deny")
	}
	return nil
}

// validatePathPatterns checks that each match.path entry is a well-formed glob.
// The submodules sentinel is exempt (it is expanded before evaluation); a
// trailing slash is directory sugar for `/**` and is stripped before checking.
func validatePathPatterns(patterns []string) error {
	for _, pat := range patterns {
		if pat == "" {
			return fmt.Errorf("empty pattern in match.path")
		}
		if pat != submodulesToken && !doublestar.ValidatePattern(strings.TrimRight(pat, "/")) {
			return fmt.Errorf("invalid glob in match.path: %q", pat)
		}
	}
	return nil
}

func validAction(a Action) error {
	switch a {
	case ActionAllow, ActionDeny:
		return nil
	default:
		return fmt.Errorf("invalid action %q (want allow or deny)", a)
	}
}

func validMode(m Mode) error {
	switch m {
	case ModeEnable, ModeConfirm, ModeDisable:
		return nil
	default:
		return fmt.Errorf("invalid mode %q (want enable, confirm, or disable)", m)
	}
}
