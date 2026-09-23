package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// Definition is the DECLARATIVE part of an engine kind: pure data, the
// fields an engine package fills once in its constructor. ONE TYPED FIELD
// PER SURFACE KIND: a kind that does not exist cannot be declared and a kind
// cannot be declared twice, at compile time; nil = not carried. There is no
// runtime validation of kinds. Requiredness — a nil Context on an engine
// that must carry a system prompt — is checked LOUDLY at Instance(), never
// at compile time. The views over a Definition and the common decisioning
// live on Base, the engine root every engine struct embeds.
type Definition struct {
	Name         Name
	Distribution Distribution
	Modes        []Mode
	Permissions  PermissionFacts
	// The six surface kinds, EXACTLY ONE typed approach each. nil = not
	// carried. For a kind the engine requires, one is present and nil is
	// Instance()'s refusal; for an optional kind (MCP among them) nil is
	// legal and means the engine has no native form for it.
	Context  ContextApproach
	MCP      MCPApproach
	Settings SettingsApproach
	Hooks    HooksApproach
	Commands CommandsApproach
	Skills   SkillsApproach
	// Dynamic is the engine's PROVIDED dynamic approach: how it consumes
	// items served on the session's MCP endpoint. OPTIONAL: nil means the
	// engine has no dynamic half and receives everything statically. The
	// compounding of the two halves is Base.Delegate's, written once in
	// core; the engine supplies the approach, never the decision.
	Dynamic DynamicApproach
	// CLI declares each mode's argv grammar; ParseArgv is the shared parser
	// the anti-drift test runs Exec's output through.
	CLI []CLIGrammar
	// ModelAliases translates configured model strings: a declared table.
	ModelAliases map[string]string
	// HookLosses names, per unified hook event, why the engine's hook
	// mechanism has no native form for it — a declared gap, reported to the
	// user only when a loadout actually configures that event. nil = every
	// event is carried. Exports' HookEvent table cannot stand in for it:
	// an engine carries an event through a matcher-narrowed native one
	// without listing it there.
	HookLosses map[string]string
	// ExportSchema is the JSON schema of the per-engine `exports` block a
	// bundle item may carry under Name.
	ExportSchema []byte
	// Version is how to ask the engine's binary for its version. The zero
	// value means the engine has no binary to ask (a double).
	Version VersionCommand
}

// VersionCommand is the argv an engine's binary answers its version to and
// the parse of that answer: vendors print different shapes, so the engine
// declares its own reading rather than one shared regex guessing.
type VersionCommand struct {
	Args  []string
	Parse func(output string) (string, error)
}

// Declared reports whether the engine can be asked for its version at all.
func (v VersionCommand) Declared() bool { return len(v.Args) > 0 && v.Parse != nil }

// Base is the ENGINE ROOT: every engine struct embeds it, so the views over
// a Definition and the COMMON DECISIONING are written once, in core, and no
// engine re-implements them. Per-engine structs supply approaches and
// engine-specific logic only.
type Base struct{ Definition }

// Root makes the embedding engine struct satisfy Engine.Root.
func (b Base) Root() Base { return b }

// kinds is the closed set in Kind order: the one place the walk over the
// typed fields is spelled, so Surfaces, Static and Delegate agree.
var kinds = []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills}

// approach returns the typed field for the Kind, nil when not carried. It
// is the single walk every derived view reads.
func (d Base) approach(k present.Kind) present.Approach {
	switch k {
	case present.Context:
		if d.Context != nil {
			return d.Context
		}
	case present.MCP:
		if d.MCP != nil {
			return d.MCP
		}
	case present.Settings:
		if d.Settings != nil {
			return d.Settings
		}
	case present.Hooks:
		if d.Hooks != nil {
			return d.Hooks
		}
	case present.Commands:
		if d.Commands != nil {
			return d.Commands
		}
	case present.Skills:
		if d.Skills != nil {
			return d.Skills
		}
	}
	return nil
}

// Surfaces is the derived approach table: the typed fields, walked, nils
// omitted. Nothing declares it separately.
func (d Base) Surfaces() Surfaces {
	s := Surfaces{}
	for _, k := range kinds {
		if a := d.approach(k); a != nil {
			s[k] = a
		}
	}
	return s
}

// Carries reports whether the engine declared an approach for the Kind.
func (d Base) Carries(k present.Kind) bool { return d.approach(k) != nil }

// Static lists the Kinds the engine carries, in Kind order.
func (d Base) Static() []present.Kind {
	var out []present.Kind
	for _, k := range kinds {
		if d.Carries(k) {
			out = append(out, k)
		}
	}
	return out
}

// ErrDefinition is the constructor's refusal of an incoherent declaration;
// every Validate error wraps it.
var ErrDefinition = errors.New("engine: invalid definition")

// Delegation is the root's decision for one package: which kinds go to the
// engine's static approach types, and which items to its dynamic approach.
type Delegation struct {
	Static  []present.Kind // kinds with items, delivered through the typed static approaches
	Dynamic []string       // refs of PREFACE items served on the endpoint; empty when the engine provides no dynamic approach
}

// Delegate is the COMMON DECISIONING, written once here. Fragments with a
// PREFACE (a premise: the conditional fragments) are WITHHELD from static
// delivery when the engine provides a dynamic approach, because dynamic
// delivery of fragments exists; every non-preface item goes to the engine's
// static approach type for its kind; with no dynamic approach, everything
// goes static. The delivery router applies this decision and never re-makes
// it.
func (d Base) Delegate(items Items) Delegation {
	var out Delegation
	static := map[present.Kind]bool{}
	for _, f := range items.Fragments {
		if f.Premise != "" && d.Dynamic != nil {
			out.Dynamic = append(out.Dynamic, f.Ref)
			continue
		}
		static[present.Context] = true
	}
	if len(items.Commands) > 0 {
		static[present.Commands] = true
	}
	if len(items.Skills) > 0 {
		static[present.Skills] = true
	}
	if len(items.Hooks) > 0 {
		static[present.Hooks] = true
	}
	if len(items.MCP) > 0 || d.Dynamic != nil { // the session endpoint itself is an MCP entry
		static[present.MCP] = true
	}
	if items.Settings {
		static[present.Settings] = true
	}
	for _, k := range kinds {
		if static[k] {
			out.Static = append(out.Static, k)
		}
	}
	return out
}

// Validate is the non-kind coherence check the constructor runs once: Name
// set and lowercase, a decided Distribution, Modes non-empty, a CLI grammar
// per Mode, every declared approach named with at least one root, and a
// dynamic approach only on an engine that declares MCP (the endpoint is
// named through the MCP file). Kinds need no check: the type did it.
func (d Base) Validate() error {
	if d.Name == "" || len(d.Modes) == 0 {
		return fmt.Errorf("%w: name and modes are required", ErrDefinition)
	}
	if string(d.Name) != strings.ToLower(string(d.Name)) {
		return fmt.Errorf("%w: name %q must be lowercase", ErrDefinition, d.Name)
	}
	if !d.Distribution.Decided() {
		return fmt.Errorf("%w: %s declares Distribution %s; an undeclared policy must not default-ship", ErrDefinition, d.Name, d.Distribution)
	}
	if d.Dynamic != nil && d.MCP == nil {
		return fmt.Errorf("%w: %s: a dynamic approach needs an MCP approach to name the endpoint", ErrDefinition, d.Name)
	}
	for _, m := range d.Modes {
		if _, ok := CLIFor(d.CLI, m); !ok {
			return fmt.Errorf("%w: %s: mode %v has no CLI grammar", ErrDefinition, d.Name, m)
		}
	}
	for _, k := range kinds {
		a := d.approach(k)
		if a == nil {
			continue
		}
		if a.Name() == "" || len(a.Traits().Roots) == 0 {
			return fmt.Errorf("%w: %s: the approach for kind %v lacks a name or a root", ErrDefinition, d.Name, k)
		}
	}
	if d.Dynamic != nil && (d.Dynamic.Name() == "" || len(d.Dynamic.Traits().Roots) == 0) {
		return fmt.Errorf("%w: %s: the dynamic approach lacks a name or a root", ErrDefinition, d.Name)
	}
	return nil
}

// PermissionFacts is the engine's permission vocabulary as facts.
type PermissionFacts struct {
	// Native lists the postures the engine maps to a mechanism of its own.
	Native []PermissionMode
	// ReadOnlyPlan is true when the engine maps PermissionPlan to a
	// genuinely read-only, non-prompting mode; false = no such tier, and the
	// resolver collapses plan to default.
	ReadOnlyPlan bool
	// HostDefault is the posture a run takes when nothing declared one; a
	// headless run refuses it unless it is SafeHeadless.
	HostDefault PermissionMode
	// HostDefaultReason is shown to the user (under -v) when HostDefault is
	// the posture a run resolved to.
	HostDefaultReason string
}

// CLIGrammar declares one mode's argv grammar: the binary, the flags it
// accepts (with whether each consumes a value) and how many positionals it
// takes. Everything after a bare "--" is positional.
type CLIGrammar struct {
	Mode       Mode
	Binary     string
	Flags      []Flag
	Positional int
}

// Flag is one declared flag.
type Flag struct {
	Name     string
	HasValue bool
}

// Parsed is an argv line read against a grammar: the flags that occurred
// (last occurrence's value) and the positionals in order.
type Parsed struct {
	Flags       map[string]string
	Positionals []string
}

// ErrArgv is the grammar's refusal: an argv the declared grammar does not
// accept.
var ErrArgv = errors.New("engine: argv rejected by the grammar")

// ParseArgv reads args against the grammar. A flag the grammar does not
// declare, a value-taking flag with no value, a value on a flag that takes
// none, or more positionals than declared is refused with ErrArgv naming
// the offender — the anti-drift test's whole signal.
func (g CLIGrammar) ParseArgv(args []string) (Parsed, error) {
	declared := make(map[string]Flag, len(g.Flags))
	for _, f := range g.Flags {
		declared[f.Name] = f
	}
	out := Parsed{Flags: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out.Positionals = append(out.Positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			out.Positionals = append(out.Positionals, a)
			continue
		}
		name, val, inline := strings.Cut(a, "=")
		f, ok := declared[name]
		if !ok {
			return Parsed{}, fmt.Errorf("%w: %s/%v does not declare flag %q (argv: %s)", ErrArgv, g.Binary, g.Mode, name, strings.Join(args, " "))
		}
		switch {
		case f.HasValue && !inline:
			i++
			if i >= len(args) {
				return Parsed{}, fmt.Errorf("%w: %s/%v: flag %s declares a value but argv ends after it", ErrArgv, g.Binary, g.Mode, name)
			}
			val = args[i]
		case !f.HasValue && inline:
			return Parsed{}, fmt.Errorf("%w: %s/%v: flag %s takes no value", ErrArgv, g.Binary, g.Mode, name)
		}
		out.Flags[name] = val
	}
	if len(out.Positionals) > g.Positional {
		return Parsed{}, fmt.Errorf("%w: %s/%v declares %d positional(s), argv carries %d", ErrArgv, g.Binary, g.Mode, g.Positional, len(out.Positionals))
	}
	return out, nil
}

// CLIFor selects the grammar for a mode.
func CLIFor(grammars []CLIGrammar, mode Mode) (CLIGrammar, bool) {
	for _, g := range grammars {
		if g.Mode == mode {
			return g, true
		}
	}
	return CLIGrammar{}, false
}
