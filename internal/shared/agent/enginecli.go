package agent

import (
	"fmt"
	"slices"
	"strings"
)

// This file defines the ENGINE CLI CONTRACT: a backend's single declaration of
// the vendor process surfaces ctxloom actually spawns — the binary and
// subcommand, the argv grammar, where the prompt travels, the environment set
// and stripped, and the context surfaces the vendor CLI reads at startup.
//
// ONE declaration, TWO consumers:
//
//  1. the DRIVER side (this repo spawning the real vendor CLI): its buildArgs
//     emits argv that must PARSE against the declared grammar — enforced by
//     each backend's anti-drift test (see internal/claude's
//     TestEngineCLI_BuildArgsFlagsAreDeclared);
//  2. the FAKE side (a future deterministic stand-in vendor binary): it PARSES
//     argv with the same grammar and probes the same surface list, so a test
//     can ask it "what did you receive, and where did you find it?".
//
// The point is anti-drift. A fake carrying its OWN copy of "claude takes
// --print, reads CLAUDE.md, puts the oneshot prompt on stdin" would keep
// passing after the driver changed — this project's silent-no-op failure mode
// wearing a new hat. Both sides reading one declaration makes drift a red test
// at the driver instead of a green test over fiction.
//
// DRY here means ONE SOURCE OF TRUTH, not one data structure. Behaviour that
// resists declaration stays as CODE in the backend's own package, which both
// sides call:
//
//   - WHICH flag a permission posture selects (claude's bypass →
//     --dangerously-skip-permissions vs plan → --permission-mode plan
//     --disallowedTools ...), and codex's sandbox tier table;
//   - the ORDER flags are emitted in;
//   - the CONDITIONS gating a flag (SkipSetup, CellKind, a non-empty surface
//     path, a harp in the env).
//
// A descriptor with an escape hatch for each of those would be worse than
// either option. The declaration answers "does this flag exist on this
// surface, and does it take a value?"; buildArgs answers "is it emitted right
// now, and where in the line?".
//
// SCOPE: oneshot and interactive only. The ACP surface (claude-code-acp,
// codex-acp — a different binary with a different grammar) is deferred to
// v0.8 and is deliberately NOT modelled here.

// CLISurface names one process surface a backend's engine presents. The two
// surfaces do not share a grammar: claude's oneshot takes --print and reads the
// prompt from stdin, its interactive form takes --name and a positional
// prompt; codex's oneshot is a distinct `exec` SUBCOMMAND that rejects
// --ask-for-approval outright.
type CLISurface string

const (
	// CLISurfaceOneshot is the engine's non-interactive run: one prompt in,
	// one result out, no terminal.
	CLISurfaceOneshot CLISurface = "oneshot"
	// CLISurfaceInteractive is the engine's TUI run, attached to the user's
	// terminal.
	CLISurfaceInteractive CLISurface = "interactive"
)

// ValueShape declares what an argv flag's value token IS. It is not decoration:
// claude's --settings takes EITHER a file path (the normal delivery path) or a
// LITERAL JSON object (the SkipSetup minimal-mode override), and a grammar that
// said "path" would be wrong half the time — a fake would stat a JSON document
// and report the settings surface as absent while the real CLI applied it.
type ValueShape string

const (
	// ValueNone is a boolean flag: it consumes no following token.
	ValueNone ValueShape = ""
	// ValueString is an opaque string token. It may legitimately be EMPTY —
	// claude's `--tools ""` and `--system-prompt ""` pass an empty string as a
	// separate argv token, which is not the same as omitting the flag.
	ValueString ValueShape = "string"
	// ValuePath is a filesystem path a consumer may stat/read.
	ValuePath ValueShape = "path"
	// ValueJSON is a literal JSON document carried inline in argv.
	ValueJSON ValueShape = "json"
	// ValuePathOrJSON is claude's --settings: a path on the normal path, an
	// inline JSON object under SkipSetup. A consumer must discriminate on the
	// token (a leading '{' means literal), never assume.
	ValuePathOrJSON ValueShape = "path-or-json"
)

// CLIFlag declares one flag in a backend's argv grammar for one surface.
type CLIFlag struct {
	// Name is the flag exactly as it appears in argv, dashes included
	// ("--model").
	Name string
	// Value declares the flag's value token (ValueNone for a boolean flag).
	Value ValueShape
	// Ignored records that the real vendor binary PARSES this flag and then
	// does nothing with it — a silent no-op at the VENDOR's end, not ours. A
	// fake must accept it (rejecting would fail a spawn the real binary
	// accepts) without acting on it. Ignored flags are exempt from a driver's
	// emission-coverage assertion: they may be declared for grammar fidelity
	// without ctxloom ever emitting them.
	Ignored bool
	// Required declares that this surface's argv is INVALID without the flag.
	// It is the surface's DISCRIMINATOR where one exists — claude's oneshot is
	// `claude --print` and nothing else, and a driver that stopped emitting it
	// while still piping the prompt on stdin hangs the real binary on a
	// terminal handshake while a name-only grammar reports a perfect run
	// (U079-F02).
	Required bool
	// Enum is the closed set of legal values, when the vendor declares one
	// ("read-only | workspace-write | danger-full-access"). Empty means any
	// token of the declared shape. A grammar that accepted `--sandbox
	// nonsense` would green a line the real binary exits 2 on (U079-F01).
	Enum []string
	// ConflictsWith names flags that may not appear in the same argv as this
	// one. The relation is enforced SYMMETRICALLY, so declaring it on either
	// side is enough — a constraint that depended on argv order would be a
	// coin toss.
	ConflictsWith []string
	// Note carries verification provenance for a non-obvious entry ("empty
	// string is a separate argv token", "value is inline JSON under SkipSetup").
	Note string
}

// AllowsValue reports whether v is legal for this flag: any token when no enum
// is declared, otherwise a member of it.
func (f CLIFlag) AllowsValue(v string) bool {
	return len(f.Enum) == 0 || slices.Contains(f.Enum, v)
}

// TakesValue reports whether this flag consumes the NEXT argv token.
func (f CLIFlag) TakesValue() bool { return f.Value != ValueNone }

// PromptDelivery declares HOW the user/task prompt reaches the engine on a
// surface. It is a first-class part of the contract because the two backends
// genuinely differ and the difference is load-bearing: claude's oneshot pipes
// the task on STDIN (argv delivery hit E2BIG on `ctxloom weave` synthesis)
// while its interactive form and both codex surfaces pass it as an argv
// POSITIONAL. A fake that looked for the prompt in the wrong place would report
// "no prompt received" against a driver that delivered one perfectly.
type PromptDelivery string

const (
	// PromptNone means this surface carries no prompt.
	PromptNone PromptDelivery = "none"
	// PromptPositional means the prompt is the trailing argv positional.
	PromptPositional PromptDelivery = "positional"
	// PromptStdin means the prompt is written to the child's stdin.
	PromptStdin PromptDelivery = "stdin"
)

// ProbeKind categorises one context surface a vendor CLI reads. It deliberately
// mirrors SurfaceKind's labels for the five kinds ctxloom DELIVERS, and adds
// the ones a vendor READS that ctxloom has no delivery surface for. The two
// vocabularies are kept in step by ProbeKindOf plus the agreement test in
// enginecli_test.go, so a renamed SurfaceKind label cannot silently diverge
// from a probe declaration.
type ProbeKind string

const (
	// ProbeKindContext is the engine's instruction/context file (CLAUDE.md,
	// AGENTS.md) or the flag-delivered equivalent.
	ProbeKindContext ProbeKind = "context"
	// ProbeKindMCP is the engine's MCP server config.
	ProbeKindMCP ProbeKind = "mcp"
	// ProbeKindSettings is the engine's settings/hooks surface.
	ProbeKindSettings ProbeKind = "settings"
	// ProbeKindCommands is the engine's slash-command directory.
	ProbeKindCommands ProbeKind = "commands"
	// ProbeKindSkills is the engine's Agent Skill package directory.
	ProbeKindSkills ProbeKind = "skills"
	// ProbeKindAgents is an engine's sub-agent definition directory
	// (.claude/agents/). It has NO SurfaceKind counterpart: ctxloom's writer
	// for it (claude's WriteAgentFiles) is a separate, not-yet-wired write path
	// rather than a delivery surface, so nothing delivers it today — which is
	// precisely why declaring it is worth doing. A probe reporting "looked,
	// found nothing" is how the day it gets wired and silently writes zero
	// files becomes visible.
	ProbeKindAgents ProbeKind = "agents"
)

// ProbeKindOf maps a delivery SurfaceKind onto its probe category, so the
// declaration never restates a label the SurfaceKind enum already owns.
func ProbeKindOf(k SurfaceKind) ProbeKind { return ProbeKind(k.String()) }

// ProbeScope says where a probe's search roots come from.
type ProbeScope string

const (
	// ScopeCwd probes exactly one path: <cwd>/<Rel>.
	ScopeCwd ProbeScope = "cwd"
	// ScopeHome probes $HOME/<Rel> — the user-global surface, lowest
	// precedence.
	ScopeHome ProbeScope = "home"
	// ScopeEnvDir probes <$EnvVar>/<Rel>, falling back to
	// $HOME/<EnvHomeDefault>/<Rel> when the variable is unset — codex's
	// CODEX_HOME, whose prompts are NOT cwd-relative.
	ScopeEnvDir ProbeScope = "env-dir"
	// ScopeFlagValue probes the path named by a declared flag's VALUE
	// (--mcp-config <file>). The flag being absent from argv means the probe
	// contributes nothing, which is itself a reportable observation.
	ScopeFlagValue ProbeScope = "flag-value"
)

// CLIProbe declares one context surface the vendor CLI reads at startup, at the
// position in its search order the declaration itself gives.
//
// Probes are ORDERED WITHIN A KIND: the first entry for a kind is consulted
// first and, where the vendor's rule is "first wins", it is the winner. Across
// kinds the order is only cosmetic.
type CLIProbe struct {
	// Kind is the surface category (see ProbeKind).
	Kind ProbeKind
	// Scope selects the search root (see ProbeScope).
	Scope ProbeScope
	// Rel is the path relative to the resolved root ("CLAUDE.md",
	// ".claude/settings.json"). Empty for a ScopeFlagValue probe, whose path
	// comes from argv.
	Rel string
	// EnvVar names the environment variable holding the root, for ScopeEnvDir.
	EnvVar string
	// EnvHomeDefault is the $HOME-relative fallback root when EnvVar is unset
	// (".codex").
	EnvHomeDefault string
	// Flag names the declared flag whose value is the path, for ScopeFlagValue.
	Flag string
	// Dir marks a directory surface (.claude/commands/): a consumer reports the
	// directory's entries rather than one file's bytes.
	Dir bool
	// Note documents the vendor precedence or merge rule this probe's position
	// encodes ("layers over the project file unless --strict-mcp-config").
	Note string
}

// EngineCLI is a backend's declaration of ONE process surface.
type EngineCLI struct {
	// Engine is the backend registry name ("claude-code", "codex").
	Engine string
	// Surface is which of the engine's process surfaces this declares.
	Surface CLISurface
	// Binary is the command ctxloom spawns, resolved on PATH ("claude"). It is
	// also the name a stand-in binary must answer to when PATH-shadowed.
	Binary string
	// Subcommand is the fixed leading argv token this surface requires, before
	// any flag ("exec" for codex's oneshot). Empty when the surface is the
	// bare binary — claude has no subcommand on either surface.
	Subcommand string
	// Prompt declares how the prompt reaches the engine on this surface.
	Prompt PromptDelivery
	// Flags is the argv grammar for this surface. It carries every flag
	// ctxloom's driver can emit here; a flag the vendor accepts but ctxloom
	// never emits belongs here too, marked Ignored, or not at all.
	Flags []CLIFlag
	// SetEnv names the environment variables ctxloom SETS on the child for this
	// surface.
	SetEnv []string
	// StripEnv names inherited variables ctxloom REMOVES. A strip that silently
	// stopped happening is otherwise invisible, so it is declared rather than
	// left implicit in the spawn code.
	StripEnv []string
	// Probes is the ordered context-surface list the vendor reads (see
	// CLIProbe).
	Probes []CLIProbe
}

// LookupFlag returns the declared flag with the given name.
func (c EngineCLI) LookupFlag(name string) (CLIFlag, bool) {
	i := slices.IndexFunc(c.Flags, func(f CLIFlag) bool { return f.Name == name })
	if i < 0 {
		return CLIFlag{}, false
	}
	return c.Flags[i], true
}

// FlagNames returns every declared flag name, in declaration order.
func (c EngineCLI) FlagNames() []string {
	out := make([]string, 0, len(c.Flags))
	for _, f := range c.Flags {
		out = append(out, f.Name)
	}
	return out
}

// ProbesFor returns the declared probes for one kind, in precedence order.
func (c EngineCLI) ProbesFor(kind ProbeKind) []CLIProbe {
	var out []CLIProbe
	for _, p := range c.Probes {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// Validate reports a self-inconsistent declaration: duplicate flags, a
// ScopeFlagValue probe naming a flag this surface does not declare (or one that
// takes no value), or a probe whose scope and fields disagree. It exists so a
// malformed declaration fails at its own test rather than producing a
// mysteriously wrong parse downstream.
func (c EngineCLI) Validate() error {
	seen := make(map[string]bool, len(c.Flags))
	for _, f := range c.Flags {
		if f.Name == "" {
			return fmt.Errorf("%s/%s: a flag has an empty name", c.Engine, c.Surface)
		}
		if seen[f.Name] {
			return fmt.Errorf("%s/%s: flag %s declared twice", c.Engine, c.Surface, f.Name)
		}
		seen[f.Name] = true
		if len(f.Enum) > 0 && !f.TakesValue() {
			return fmt.Errorf("%s/%s: flag %s declares an enum but takes no value", c.Engine, c.Surface, f.Name)
		}
	}
	// A constraint that names something undeclared is a rule that can never
	// fire, which is worse than no rule: the declaration reads as if the
	// exclusion were enforced.
	for _, f := range c.Flags {
		for _, other := range f.ConflictsWith {
			if !seen[other] {
				return fmt.Errorf("%s/%s: flag %s conflicts with undeclared flag %q", c.Engine, c.Surface, f.Name, other)
			}
		}
	}
	for _, p := range c.Probes {
		switch p.Scope {
		case ScopeFlagValue:
			f, ok := c.LookupFlag(p.Flag)
			if !ok {
				return fmt.Errorf("%s/%s: probe %s names undeclared flag %q", c.Engine, c.Surface, p.Kind, p.Flag)
			}
			if !f.TakesValue() {
				return fmt.Errorf("%s/%s: probe %s reads the value of valueless flag %s", c.Engine, c.Surface, p.Kind, p.Flag)
			}
		case ScopeEnvDir:
			if p.EnvVar == "" {
				return fmt.Errorf("%s/%s: probe %s uses env-dir scope with no EnvVar", c.Engine, c.Surface, p.Kind)
			}
		case ScopeCwd, ScopeHome:
			if p.Rel == "" {
				return fmt.Errorf("%s/%s: probe %s uses %s scope with no Rel", c.Engine, c.Surface, p.Kind, p.Scope)
			}
		default:
			return fmt.Errorf("%s/%s: probe %s has unknown scope %q", c.Engine, c.Surface, p.Kind, p.Scope)
		}
	}
	return nil
}

// ParsedFlag is one flag occurrence recovered from an argv line.
type ParsedFlag struct {
	Flag  CLIFlag
	Value string
}

// ParsedArgv is an argv line read against a declared grammar.
type ParsedArgv struct {
	// Subcommand is the leading fixed token, when the surface declares one.
	Subcommand string
	// Flags are the flag occurrences in argv order.
	Flags []ParsedFlag
	// Positionals are the non-flag, non-value tokens in argv order — for a
	// PromptPositional surface, the prompt is the last of them.
	Positionals []string
}

// Has reports whether the named flag occurred.
func (p ParsedArgv) Has(name string) bool {
	return slices.ContainsFunc(p.Flags, func(f ParsedFlag) bool { return f.Flag.Name == name })
}

// Value returns the first occurrence's value for the named flag. The bool
// distinguishes "absent" from "present with an empty value" — the distinction
// claude's `--tools ""` turns on.
func (p ParsedArgv) Value(name string) (string, bool) {
	for _, f := range p.Flags {
		if f.Flag.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

// Values returns every occurrence's value for the named flag, in argv order.
func (p ParsedArgv) Values(name string) []string {
	var out []string
	for _, f := range p.Flags {
		if f.Flag.Name == name {
			out = append(out, f.Value)
		}
	}
	return out
}

// UndeclaredFlagError is what makes drift LOUD: the driver emitted a flag the
// contract does not declare. It names the flag and the whole argv so the
// failure identifies the drift instead of merely reporting a mismatch.
type UndeclaredFlagError struct {
	Engine  string
	Surface CLISurface
	Flag    string
	Argv    []string
}

// Error renders the drift report.
func (e *UndeclaredFlagError) Error() string {
	return fmt.Sprintf("%s/%s emitted undeclared flag %q — declare it in the engine CLI contract (argv: %s)",
		e.Engine, e.Surface, e.Flag, strings.Join(e.Argv, " "))
}

// MissingValueError is a declared value-taking flag left with no value token.
type MissingValueError struct {
	Engine  string
	Surface CLISurface
	Flag    string
}

// Error renders the missing-value report.
func (e *MissingValueError) Error() string {
	return fmt.Sprintf("%s/%s: flag %s declares a value but argv ends after it", e.Engine, e.Surface, e.Flag)
}

// SubcommandError is a surface whose declared subcommand is missing or wrong.
type SubcommandError struct {
	Engine  string
	Surface CLISurface
	Want    string
	Got     string
}

// Error renders the subcommand report.
func (e *SubcommandError) Error() string {
	return fmt.Sprintf("%s/%s: expected leading subcommand %q, got %q", e.Engine, e.Surface, e.Want, e.Got)
}

// MissingFlagError is a declared REQUIRED flag absent from the argv line —
// the surface's discriminator left out, which the real binary rejects.
type MissingFlagError struct {
	Engine  string
	Surface CLISurface
	Flag    string
	Argv    []string
}

// Error renders the missing-flag report.
func (e *MissingFlagError) Error() string {
	return fmt.Sprintf("%s/%s: required flag %s is absent — the real binary rejects this line (argv: %s)",
		e.Engine, e.Surface, e.Flag, strings.Join(e.Argv, " "))
}

// ValueNotAllowedError is a value token outside a flag's declared enum.
type ValueNotAllowedError struct {
	Engine  string
	Surface CLISurface
	Flag    string
	Value   string
	Allowed []string
}

// Error renders the illegal-value report.
func (e *ValueNotAllowedError) Error() string {
	return fmt.Sprintf("%s/%s: flag %s value %q is not one of [%s]",
		e.Engine, e.Surface, e.Flag, e.Value, strings.Join(e.Allowed, " | "))
}

// ConflictingFlagsError is two flags the declaration says are mutually
// exclusive appearing in one argv line.
type ConflictingFlagsError struct {
	Engine  string
	Surface CLISurface
	Flag    string
	Other   string
}

// Error renders the exclusion report.
func (e *ConflictingFlagsError) Error() string {
	return fmt.Sprintf("%s/%s: flags %s and %s are mutually exclusive within one argv",
		e.Engine, e.Surface, e.Flag, e.Other)
}

// ParseArgv reads an argv line against this surface's declared grammar. It is
// the SHARED reader both consumers use: the driver's anti-drift test runs its
// own buildArgs output through it (an undeclared flag is a red test), and a
// future fake runs the argv it was spawned with through it (so the fake never
// restates the grammar).
//
// An unrecognised token that starts with "-" is an UndeclaredFlagError — that
// is the whole point. There is deliberately NO lenient mode: skipping unknown
// tokens is exactly how drift would hide. A backend's opaque user-configured
// passthrough args (claude's ClaudeConfig.Args) are undeclarable by
// construction, so a caller validating a line must supply them empty.
//
// "--" is honoured as the end-of-flags separator; everything after it is a
// positional.
func (c EngineCLI) ParseArgv(argv []string) (ParsedArgv, error) {
	var out ParsedArgv
	i := 0
	if c.Subcommand != "" {
		if len(argv) == 0 || argv[0] != c.Subcommand {
			got := ""
			if len(argv) > 0 {
				got = argv[0]
			}
			return out, &SubcommandError{Engine: c.Engine, Surface: c.Surface, Want: c.Subcommand, Got: got}
		}
		out.Subcommand = c.Subcommand
		i = 1
	}
	endOfFlags := false
	for ; i < len(argv); i++ {
		tok := argv[i]
		if endOfFlags {
			out.Positionals = append(out.Positionals, tok)
			continue
		}
		if tok == "--" {
			endOfFlags = true
			continue
		}
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			out.Positionals = append(out.Positionals, tok)
			continue
		}
		f, ok := c.LookupFlag(tok)
		if !ok {
			return out, &UndeclaredFlagError{Engine: c.Engine, Surface: c.Surface, Flag: tok, Argv: argv}
		}
		pf := ParsedFlag{Flag: f}
		if f.TakesValue() {
			if i+1 >= len(argv) {
				return out, &MissingValueError{Engine: c.Engine, Surface: c.Surface, Flag: tok}
			}
			i++
			pf.Value = argv[i]
			if !f.AllowsValue(pf.Value) {
				return out, &ValueNotAllowedError{Engine: c.Engine, Surface: c.Surface,
					Flag: f.Name, Value: pf.Value, Allowed: f.Enum}
			}
		}
		out.Flags = append(out.Flags, pf)
	}
	return out, c.checkConstraints(out, argv)
}

// checkConstraints applies the whole-LINE rules — the ones no single token can
// break on its own: every Required flag present, and no declared mutual
// exclusion violated. They run after the scan because both are statements
// about the set of flags, not about a token.
func (c EngineCLI) checkConstraints(out ParsedArgv, argv []string) error {
	for _, f := range c.Flags {
		if f.Required && !out.Has(f.Name) {
			return &MissingFlagError{Engine: c.Engine, Surface: c.Surface, Flag: f.Name, Argv: argv}
		}
	}
	// The exclusion is symmetric: a rule that only fired when the declaring
	// flag came first would pass or fail on argv order alone.
	for _, f := range c.Flags {
		if !out.Has(f.Name) {
			continue
		}
		for _, other := range f.ConflictsWith {
			if out.Has(other) {
				return &ConflictingFlagsError{Engine: c.Engine, Surface: c.Surface, Flag: f.Name, Other: other}
			}
		}
	}
	return nil
}

// EngineCLIProvider is implemented by a backend that declares its native CLI
// surfaces. Implementing it is what lets a stand-in binary impersonate that
// backend's engine WITHOUT a second copy of the backend's knowledge. Backends
// that have not adopted it are simply absent from the declaration side rather
// than fabricating one.
type EngineCLIProvider interface {
	EngineCLIs() []EngineCLI
}

// EngineCLIFor selects the named surface from a declaration list.
func EngineCLIFor(clis []EngineCLI, surface CLISurface) (EngineCLI, bool) {
	i := slices.IndexFunc(clis, func(c EngineCLI) bool { return c.Surface == surface })
	if i < 0 {
		return EngineCLI{}, false
	}
	return clis[i], true
}
