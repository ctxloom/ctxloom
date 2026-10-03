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
	// Rules are command rules, matched against every parsed shell command.
	Rules []CommandRule `yaml:"rules"`
	// PathRules are file-edit rules, matched against the target of a
	// file-editing tool call. The two kinds share ids (unique across both) but
	// nothing else: neither evaluator reads the other's list.
	PathRules []PathRule `yaml:"path_rules"`
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

// RuleBase is what every rule carries whatever it matches: its identity, the
// action it selects, what it tells the agent, and how firmly it fires.
type RuleBase struct {
	ID      string `yaml:"id"`
	Action  Action `yaml:"action"` // defaults to deny
	Message string `yaml:"message"`
	Suggest string `yaml:"suggest"`
	// Mode is disable | confirm | enable (default enable):
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

// CommandRule maps a shell-command match to an action.
type CommandRule struct {
	RuleBase `yaml:",inline"`
	Match    CommandMatch `yaml:"match"`
}

// PathRule maps a file-edit match to an action.
type PathRule struct {
	RuleBase `yaml:",inline"`
	Match    PathMatch `yaml:"match"`
}

func (r RuleBase) action() Action {
	if r.Action == "" {
		return ActionDeny
	}
	return r.Action
}

// mode returns the rule's mode, defaulting to enable.
func (r RuleBase) mode() Mode {
	if r.Mode == "" {
		return ModeEnable
	}
	return r.Mode
}

// isEnabled reports whether the rule participates in evaluation. Only
// `mode: disable` turns it off.
func (r RuleBase) isEnabled() bool {
	return r.mode() != ModeDisable
}

// confirmPolicy resolves whether a denial by this rule may be lifted by
// repeating the command, the window for doing so, and any minimum delay before
// the repeat counts, given the global defaults. Only a `confirm` rule is
// repeatable, and only when a positive window applies (per-rule window overriding
// the global default). `enable` rules are inviolate.
func (r RuleBase) confirmPolicy(d Defaults) (repeatable bool, windowSeconds, delaySeconds int) {
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

// Alignment is how a command rule's operand patterns (match.command after the
// program) line up against the command's operands. The default follows the
// rule's action; see CommandMatch.Command for why the two actions differ.
type Alignment string

const (
	// AlignSubsequence: the patterns match operands in order, not necessarily
	// contiguous or leading. The only alignment a deny rule takes, and its
	// default.
	AlignSubsequence Alignment = "subsequence"
	// AlignPrefix: pattern i matches operand i, starting at operand 0; anything
	// may follow. The default for an allow rule.
	AlignPrefix Alignment = "prefix"
	// AlignExact: the command is exactly the pattern — pattern i matches operand
	// i, and nothing follows: no further operand and no option anywhere. An
	// allow-only alignment, for clearing one precise invocation (a read) whose
	// longer forms (a write) must still reach the deny. Options are excluded
	// too because they are what turn many reads into writes (`git config
	// --unset user.name` has the read's operands) and never occupy an operand
	// slot, so an operand count alone cannot see them.
	AlignExact Alignment = "exact"
)

// CommandMatch is the set of conditions a shell command must satisfy. All
// present conditions must hold (AND). An entirely empty CommandMatch matches
// nothing, to avoid accidental catch-all denials.
//
// Every element of Command, ArgsAny, ArgsAll and Unless is a Pattern: an RE2
// expression matched against ONE whole argv element (implicitly
// ^(?:pattern)$). Case-sensitive unless the pattern opts in with (?i). Write
// each one single-quoted in YAML: a double-quoted "\." is an invalid YAML
// escape, and | [ * ? { : need quoting in a flow list anyway.
type CommandMatch struct {
	// Command is [program, operand...].
	//
	// The program pattern matches argv[0] as written or its basename, so
	// absolute paths still match. Under cmd and pwsh both are first lowercased
	// and stripped of a trailing .exe (how Windows resolves a program), so a
	// pattern for them is written lowercase without .exe; POSIX shells match as
	// written.
	//
	// The remaining patterns match the command's OPERANDS (its non-option
	// arguments, classified per the command's shell) under Align. They are
	// operand patterns only: a pattern whose literal prefix is `-` is refused
	// (ErrOptionInCommand). Options go in ArgsAll/ArgsAny/Unless. The check is
	// best-effort — `(-a|-b)` has no literal prefix and is not caught.
	//
	// A trailing '.*' operand requires an operand to exist, whatever its text,
	// the empty string included: `[git, config, 'user\.name', '.*']` matches the
	// write `git config user.name Bob` and not the read `git config user.name`.
	//
	// # Allow vs. deny: matching discipline (the firewall-rule model)
	//
	// ltk's rule list is evaluated the way a packet-filter ruleset is (iptables/
	// nftables `-j ACCEPT/DROP` chains, OpenBSD pf, cloud security groups):
	// rules are walked IN ORDER and, for one command, the first that matches
	// wins (see Evaluate, which also documents how that nests inside the
	// command walk) — there is no "most specific rule wins" reranking. The
	// other firewall principle is that a rule matches STRUCTURED fields with an
	// explicit operator per field, never "does this byte sequence appear
	// anywhere in the packet". Here the fields are argv elements, classified
	// before any pattern runs, and the operator is the anchored regex — which is
	// why a regex cannot span elements or turn an operand into an option.
	//
	// Which operand INDEX a pattern is compared to is the structural decision,
	// and it depends on the rule's action, because broadening a DENY only widens
	// what gets blocked (safe) while broadening an ALLOW widens what gets let
	// through (unsafe):
	//
	//   - deny rules take ORDERED SUBSEQUENCE: operand patterns match in order,
	//     not contiguously or leading. A value-taking option whose value lands
	//     among the operands (the matcher can't know `-C`/`--context`/`--prefix`
	//     consumes a word) cannot push the subcommand out of match position:
	//     `git -C /repo push`, `docker --context prod build`, and `npm --prefix
	//     /x run …` still match `[git, push]` / `[docker, build]` / `[npm, run]`.
	//     A pattern may thus match a non-leading operand — that only ever widens
	//     what a deny catches, which is fail-safe for a guard.
	//   - allow rules take a POSITION-ANCHORED PREFIX (or exact) match: pattern
	//     i must match operand i, starting at operand[0]. An ordered-subsequence
	//     allow is fail-open: `allow: [git, status]` would clear `git commit -m
	//     status --no-verify`, because `status` — the VALUE of `-m` — is an
	//     operand, and a one-element subsequence is satisfied by any operand
	//     list containing it.
	//
	// A too-strict allow fails safe: the command is not cleared by it and falls
	// through to the next rule. A too-loose allow fails open. Consequence: an
	// allow whose real subcommand is pushed out of operand[0] by a value-taking
	// option (`docker --context prod build`) does not match `[docker, build]`,
	// and there is no position-blind way around that — args_any/args_all are
	// refused on allow rules (ErrArgsOnAllow) because they are exactly the
	// smuggling class strict prefix closes. Such a command falls through to the
	// deny, which is the safe direction.
	//
	// (ltk's overall DEFAULT policy is still allow-by-default when nothing
	// matches — unlike a network firewall's usual default-deny posture. That is
	// a separate, deliberate design choice ["a guardrail against reflexive
	// mistakes, not a security boundary" — see docs/ltk]; only the PER-RULE
	// matching operator adopts firewall discipline.)
	Command []Pattern `yaml:"command"`
	// Align selects how the operand patterns line up; empty means the action's
	// default (deny: subsequence, allow: prefix). Allow takes prefix or exact;
	// deny takes subsequence only.
	Align Alignment `yaml:"align"`
	// ArgsAny / ArgsAll are position-blind tests over the arguments after the
	// program, with bundled POSIX short options also offered expanded (`-rf`
	// as `-r` and `-f`; see expandShortClusters). Deny rules only: on an allow
	// a position-blind positive predicate widens what is cleared.
	ArgsAny []Pattern `yaml:"args_any"` // some argument matches some pattern
	ArgsAll []Pattern `yaml:"args_all"` // every pattern matches some argument
	// Unless lists exceptions: if any argument matches any pattern, the rule
	// does NOT match — e.g. block `git tag` `unless: ['--list|-l']` so the
	// read-only listing form is exempt. Position-blind, so on a deny it is the
	// fail-open direction: write it as tight as the exception really is.
	Unless []Pattern `yaml:"unless"`
	// Backgrounded matches a command DETACHED from the invoking session,
	// rather than one identified by its own spelling. A rule written as
	// `command: [nohup]` catches that spelling and nothing else — a bare
	// trailing `&`, which is what actually backgrounds a job (`just
	// test-acceptance &`), sails through, because the command's own program is
	// `just`. No other field is a positive match on "this job is detached", so
	// it is its own predicate.
	//
	// True when SimpleCommand.Background (the statement ended in `&`) OR the
	// command's own program is nohup, setsid, or disown. All three leave a
	// harness that is notified only when a foreground child exits never learning
	// this one finished.
	//
	// false (the zero value, and so also an explicit `backgrounded: false`)
	// means "no constraint from this field", the same "absent = no constraint"
	// convention every other field uses; it only ever narrows when `true`.
	Backgrounded bool `yaml:"backgrounded"`
	// Shells restricts the rule to commands owned by these shells.
	Shells []ir.Shell `yaml:"shells"`
}

// PathMatch is a file-edit rule's condition: it matches when a file-editing
// tool (Edit/Write/MultiEdit/NotebookEdit) targets a file whose path matches
// one of Path. Path patterns are doublestar GLOBS, not regexes — a different
// input domain from argv elements, where `**` is the idiom and an unescaped
// `.` in every extension would be the common bug.
type PathMatch struct {
	// Path holds patterns of three forms:
	//
	//   - a glob, matched against the file's basename or its full slash-path.
	//     e.g. `VERSION` blocks hand-editing VERSION anywhere; `*.lock` blocks
	//     any lockfile; `dist/*` blocks one level under dist/.
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

// RuleCount is the number of rules of both kinds.
func (c *Config) RuleCount() int { return len(c.Rules) + len(c.PathRules) }

// submodulesToken is the reserved match.path value that ExpandSubmodules rewrites
// into a directory-subtree pattern per .gitmodules entry. See PathMatch.Path.
const submodulesToken = "@submodules"

func (m CommandMatch) hasConstraint() bool {
	return len(m.Command) > 0 || len(m.ArgsAny) > 0 ||
		len(m.ArgsAll) > 0 || len(m.Unless) > 0 || len(m.Shells) > 0 || m.Backgrounded
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
func (m PathMatch) matchesPath(file string) bool {
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
	for i := range c.PathRules {
		pats := c.PathRules[i].Match.Path
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
		c.PathRules[i].Match.Path = expanded
	}
	return nil
}

// alignment resolves the rule's operand alignment: the written one, or the
// action's default (see CommandMatch.Command).
func (r CommandRule) alignment() Alignment {
	if r.Match.Align != "" {
		return r.Match.Align
	}
	if r.action() == ActionAllow {
		return AlignPrefix
	}
	return AlignSubsequence
}

// matches reports whether c, owned by a script in shell, satisfies m with its
// operand patterns aligned per al. The caller (Evaluate) derives al from the
// rule; matches itself has no notion of "rule".
func (m CommandMatch) matches(shell ir.Shell, c ir.SimpleCommand, al Alignment) bool {
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
	if m.Backgrounded && !isBackgrounded(c, argShell) {
		return false
	}
	if len(m.Command) > 0 && !matchCommand(m.Command, c.Argv, argShell, al) {
		return false
	}
	return m.matchesArgs(expandShortClusters(c.Args(), argShell))
}

// detachedPrograms names commands whose own effect is to hand a job off
// without leaving anything for the caller to wait on — the same detachment a
// trailing `&` produces structurally, just spelled as a program instead of a
// job-control operator. Kept here (a short, rules-owned list) rather than
// read from frontend.prefixWrapperRules: that table answers a different
// question — "strip this program off so the command it WRAPS can still be
// matched" — and importing it would also invert the package layering (rules
// is a peer of frontend, evaluated against the IR frontend produces; frontend
// does not depend on rules, and rules must not start depending on frontend).
// disown has no entry in that table at all — it does not prepend to another
// command, so there was never anything for a wrapper-stripping rule to do
// with it — but it belongs here for the same reason nohup/setsid do: running
// it is itself the detaching act.
var detachedPrograms = []string{"nohup", "setsid", "disown"}

// isBackgrounded reports whether c is detached from the invoking session:
// SimpleCommand.Background (the frontend's record of a trailing `&` on this
// command's statement) or an explicit nohup/setsid/disown invocation.
func isBackgrounded(c ir.SimpleCommand, argShell ir.Shell) bool {
	if c.Background {
		return true
	}
	return slices.Contains(detachedPrograms, programBasename(c.Program(), argShell))
}

// matchesArgs applies the position-blind argument conditions — args_all,
// args_any and the unless exceptions — to a command's already cluster-expanded
// arguments.
func (m CommandMatch) matchesArgs(args []string) bool {
	for _, p := range m.ArgsAll {
		if !p.anyMatches(args) {
			return false
		}
	}
	if len(m.ArgsAny) > 0 &&
		!slices.ContainsFunc(m.ArgsAny, func(p Pattern) bool { return p.anyMatches(args) }) {
		return false
	}
	// unless: any matching argument means this invocation is an exception
	// (e.g. a read-only `--list`/`--dry-run` form), so the rule does not match.
	return !slices.ContainsFunc(m.Unless, func(p Pattern) bool { return p.anyMatches(args) })
}

// shellForProgram maps program (a command's argv[0]) to the shell it itself
// is, when it is a directly recognized shell/interpreter binary. It is
// deliberately independent of shellenv.ShellFromPath: that one answers "which
// dialect is this HOST", this one answers "which dialect does this ARGV[0]
// interpret", and the two tables are free to diverge (ksh is spelled here
// without its version suffixes, for instance). path.Base is POSIX-only, so a
// Windows-style absolute path is normalized to forward slashes first — the
// same normalization as programNames, for the same reason. Returns "" when
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

// programNames returns the spellings of argv[0] a program pattern is tried
// against: as written and its basename. Under cmd and pwsh both are lowercased
// and lose a trailing .exe, because that is how Windows resolves a program
// name; a backslash is a path separator there too. POSIX names are taken as
// written — a literal backslash in a POSIX filename is not a separator.
func programNames(argv0 string, shell ir.Shell) [2]string {
	if shell != ir.ShellCmd && shell != ir.ShellPwsh {
		return [2]string{argv0, path.Base(argv0)}
	}
	norm := func(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".exe") }
	slashed := strings.ReplaceAll(argv0, `\`, "/")
	return [2]string{norm(argv0), norm(path.Base(slashed))}
}

// programBasename returns the trailing path component of argv[0] as the
// shell that runs it resolves a bare name (see programNames).
func programBasename(argv0 string, shell ir.Shell) string {
	return programNames(argv0, shell)[1]
}

// matchCommand reports whether a command pattern matches a command's argv,
// using the command's shell to classify operands. See CommandMatch.Command.
//
//   - pattern[0] (the program) matches argv[0] as written or by basename.
//   - pattern[1:] are operand patterns, aligned against the command's operands
//     per al.
func matchCommand(pattern []Pattern, argv []string, shell ir.Shell, al Alignment) bool {
	if len(pattern) == 0 || len(argv) == 0 {
		return false
	}
	names := programNames(argv[0], shell)
	if !pattern[0].MatchString(names[0]) && !pattern[0].MatchString(names[1]) {
		return false
	}
	args := argv[1:]
	operands := classifyOperands(args, shell)
	switch al {
	case AlignExact:
		return len(operands) == len(args) && len(operands) == len(pattern)-1 &&
			alignPrefix(pattern[1:], operands)
	case AlignPrefix:
		return alignPrefix(pattern[1:], operands)
	default:
		return alignSubsequence(pattern[1:], operands)
	}
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

// alignSubsequence reports whether the patterns match elements of s in
// order, with any other elements allowed in between. Greedy leftmost is
// complete for per-element predicates: taking the earliest operand a pattern
// matches never blocks a later pattern from matching. The DENY alignment.
func alignSubsequence(pats []Pattern, s []string) bool {
	i := 0
	for _, w := range s {
		if i == len(pats) {
			break
		}
		if pats[i].MatchString(w) {
			i++
		}
	}
	return i == len(pats)
}

// alignPrefix reports whether pattern i matches s[i] for every pattern,
// starting at s[0] — a position-anchored prefix, not "somewhere in s". No
// patterns is vacuously a prefix of anything. The ALLOW alignment.
func alignPrefix(pats []Pattern, s []string) bool {
	if len(pats) > len(s) {
		return false
	}
	for i, p := range pats {
		if !p.MatchString(s[i]) {
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
	if err := checkRemovedForms(data); err != nil {
		return nil, err
	}
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
		return fmt.Errorf("defaults.shell: %w %q", ErrInvalidShell, c.Defaults.Shell)
	}
	seen := make(map[string]bool, len(c.Rules)+len(c.PathRules))
	for i := range c.Rules {
		r := &c.Rules[i]
		if err := validateRuleBase(&r.RuleBase, listCommand, i, seen, c.Defaults); err != nil {
			return err
		}
		if fe := validateCommandMatch(r); fe != nil {
			return &RuleError{List: listCommand, Index: i, RuleID: r.ID, Field: fe.field, Err: fe.err}
		}
	}
	for i := range c.PathRules {
		r := &c.PathRules[i]
		if err := validateRuleBase(&r.RuleBase, listPath, i, seen, c.Defaults); err != nil {
			return err
		}
		if len(r.Match.Path) == 0 {
			return &RuleError{List: listPath, Index: i, RuleID: r.ID, Field: "match.path", Err: ErrNoConditions}
		}
		if err := validatePathPatterns(r.Match.Path); err != nil {
			return &RuleError{List: listPath, Index: i, RuleID: r.ID, Field: "match.path", Err: err}
		}
	}
	return nil
}

// fieldErr is a validation failure inside one rule, before it is placed.
type fieldErr struct {
	field string
	err   error
}

// validateRuleBase checks the fields every rule kind shares and records the
// id in seen (ids are unique across both lists). Ordered so nothing reports
// against a rule whose id is not yet known to be usable in the message.
func validateRuleBase(r *RuleBase, list string, index int, seen map[string]bool, d Defaults) error {
	fail := func(field string, err error) error {
		return &RuleError{List: list, Index: index, RuleID: r.ID, Field: field, Err: err}
	}
	if r.ID == "" {
		return fail("id", ErrMissingID)
	}
	if seen[r.ID] {
		return fail("id", ErrDuplicateID)
	}
	seen[r.ID] = true
	if err := validAction(r.action()); err != nil {
		return fail("action", err)
	}
	if err := validMode(r.mode()); err != nil {
		return fail("mode", err)
	}
	if err := validateConfirm(r, d); err != nil {
		return fail("mode", err)
	}
	if err := validateDenyIsExplained(r); err != nil {
		return fail("message", err)
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
func validateConfirm(r *RuleBase, d Defaults) error {
	if r.mode() != ModeConfirm {
		return nil
	}
	repeatable, window, delay := r.confirmPolicy(d)
	if !repeatable {
		return ErrConfirmNeedsWindow
	}
	if delay > 0 && delay >= window {
		return fmt.Errorf("%w: %d >= %d", ErrDelayNotBelowWindow, delay, window)
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
func validateDenyIsExplained(r *RuleBase) error {
	if r.isEnabled() && r.action() == ActionDeny &&
		strings.TrimSpace(r.Message) == "" && strings.TrimSpace(r.Suggest) == "" {
		return ErrDenyUnexplained
	}
	return nil
}

// validateCommandMatch checks a command rule's match: it has a condition, its
// shells are known, its alignment suits its action, every pattern compiles,
// operand patterns are not options, and an allow carries no position-blind
// positive predicate.
func validateCommandMatch(r *CommandRule) *fieldErr {
	m := &r.Match
	if !m.hasConstraint() {
		return &fieldErr{"match", ErrNoConditions}
	}
	for _, sh := range m.Shells {
		if !sh.Valid() {
			return &fieldErr{"match.shells", fmt.Errorf("%w %q", ErrInvalidShell, sh)}
		}
	}
	if fe := validateAlignment(r); fe != nil {
		return fe
	}
	if fe := compilePatterns(m); fe != nil {
		return fe
	}
	for i, p := range m.Command {
		if i > 0 && p.leadsWithOption() {
			return &fieldErr{fmt.Sprintf("match.command[%d]", i), fmt.Errorf("%w: %q", ErrOptionInCommand, p.src)}
		}
	}
	if r.action() == ActionAllow {
		if len(m.ArgsAny) > 0 {
			return &fieldErr{"match.args_any", ErrArgsOnAllow}
		}
		if len(m.ArgsAll) > 0 {
			return &fieldErr{"match.args_all", ErrArgsOnAllow}
		}
	}
	return nil
}

// validateAlignment checks match.align against the rule's action: an allow
// takes prefix or exact, a deny subsequence only (see CommandMatch.Command).
func validateAlignment(r *CommandRule) *fieldErr {
	al := r.Match.Align
	if al == "" {
		return nil
	}
	if al != AlignPrefix && al != AlignExact && al != AlignSubsequence {
		return &fieldErr{"match.align", fmt.Errorf("%w: %q", ErrInvalidAlignment, al)}
	}
	if len(r.Match.Command) == 0 {
		return &fieldErr{"match.align", ErrAlignmentNeedsCommand}
	}
	if (r.action() == ActionAllow) == (al == AlignSubsequence) {
		return &fieldErr{"match.align", fmt.Errorf("%w: %s on %s", ErrAlignmentForAction, al, r.action())}
	}
	return nil
}

// compilePatterns compiles every pattern of m in place, naming the first that
// fails by field and index.
func compilePatterns(m *CommandMatch) *fieldErr {
	for _, f := range []struct {
		name string
		pats []Pattern
	}{
		{"match.command", m.Command},
		{"match.args_any", m.ArgsAny},
		{"match.args_all", m.ArgsAll},
		{"match.unless", m.Unless},
	} {
		for i := range f.pats {
			if err := f.pats[i].compile(); err != nil {
				return &fieldErr{fmt.Sprintf("%s[%d]", f.name, i), err}
			}
		}
	}
	return nil
}

// validatePathPatterns checks that each match.path entry is a well-formed glob.
// The submodules sentinel is exempt (it is expanded before evaluation); a
// trailing slash is directory sugar for `/**` and is stripped before checking.
func validatePathPatterns(patterns []string) error {
	for _, pat := range patterns {
		if pat == "" {
			return ErrEmptyPattern
		}
		if pat != submodulesToken && !doublestar.ValidatePattern(strings.TrimRight(pat, "/")) {
			return fmt.Errorf("%w: %q", ErrInvalidGlob, pat)
		}
	}
	return nil
}

func validAction(a Action) error {
	switch a {
	case ActionAllow, ActionDeny:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidAction, a)
	}
}

func validMode(m Mode) error {
	switch m {
	case ModeEnable, ModeConfirm, ModeDisable:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidMode, m)
	}
}
