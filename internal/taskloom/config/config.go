// Package config resolves taskloom's own layered configuration — a SEPARATE
// config surface from ctxloom's (~/.ctxloom/config.yaml): taskloom reads
// ~/.taskloom/config.yaml and the per-project .taskloom/config.yaml, merged
// via internal/shared/confload exactly like ctxloom's own internal/config
// does (home < project < TASKLOOM_CONFIG_* env < --config-set), with a
// EnvPrefix of "TASKLOOM_CONFIG_" so it can never collide with ctxloom's own
// CTXLOOM_CONFIG_* overrides even though both binaries can run in the same
// process tree (see TestConfload_SecondProductReusesPattern in
// internal/shared/confload, which this package makes real).
//
// Today's only setting is the task-store HOMING MODE (paths.ModeHome /
// paths.ModeRepo — see the `homing` key in
// resources/schema/input/taskloom-config-schema.json): where a project's
// task log lives. ResolveMode defaults to paths.ModeHome when NOTHING at any
// layer (home config, project config, TASKLOOM_CONFIG_HOMING, --config-set,
// --homing) sets it — silently, with no error and no diagnostic. This is
// deliberate: ModeHome is the STATUS QUO, exactly what taskloom did before
// homing-mode selection existed, so defaulting to it surprises nobody and is
// a no-op for every existing project and every fresh clone (including this
// repo, which ships no .taskloom/config.yaml of its own). The only
// surprising default would be ModeRepo, which would silently relocate
// someone's tasks into their tree — that direction is never defaulted, only
// ever chosen explicitly. An INVALID value (anything other than "home"/
// "repo" at any layer) is still a returned error naming the bad value and
// the valid set: absent is fine, wrong is not.
//
// This default is completely uniform: every caller of
// internal/shared/tasks/operations.TaskContext that never sets HomingMode at
// all (ctxloom's own internal/cli, internal/operations,
// internal/lm/isolation) already gets ModeHome via TaskContext's own zero
// value, and cmd/taskloom's own frontend (via ResolveMode) now resolves to
// the identical mode when it finds nothing configured either — there is no
// longer a policy difference between "asked taskloom's own config" and
// "never asked at all".
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
	"github.com/ctxloom/ctxloom/resources"
)

// DirName/FileName are taskloom's own on-disk config conventions —
// ~/.taskloom/config.yaml and <project>/.taskloom/config.yaml — deliberately
// its OWN dot-dir, never nested under ctxloom's .ctxloom (see paths.RepoDirName's
// doc: .taskloom/config.yaml is meant to be COMMITTED, unlike .ctxloom/*).
const (
	DirName  = paths.RepoDirName // ".taskloom"
	FileName = "config.yaml"

	// envPrefix mirrors ctxloom's own CTXLOOM_CONFIG_ convention (see
	// internal/config's ctxloomProduct), scoped to taskloom so the two never
	// collide even when both binaries run in the same process tree.
	envPrefix = "TASKLOOM_CONFIG_"

	// SchemaResourceName is the embedded schema path (relative to
	// resources/schema/) read via resources.GetSchema, and the CLI-facing
	// path (relative to the repo root) both cmd/taskloom/docs_gen.go's
	// docsgen.Product.ConfigSchema and the `just gen-docs` recipe pass to
	// --config-schema.
	SchemaResourceName = "input/taskloom-config-schema.json"
	SchemaPath         = "resources/schema/" + SchemaResourceName

	// HomingConfigKey is the dotted config key selecting the task store's
	// homing mode — the exact key a home/project .taskloom/config.yaml, or a
	// --config-set/TASKLOOM_CONFIG_ override, must set.
	HomingConfigKey = "homing"

	// homingFlagName is taskloom's dedicated root flag naming the same
	// setting for a single invocation (highest precedence — see ResolveMode).
	homingFlagName = "homing"
)

// DefaultTagSchema is the tag_schema shipped when a project's config leaves
// the key unset at every layer: the triage-classification standard's
// baseline. Shipping this by default means a fresh project gets the full
// standard — scalar-collapse, derived priority, and lint coverage — with no
// opt-in required, the same ergonomics `homing`'s own default (see the
// package doc) established the precedent for.
//
// Four declared targets, each arity=scalar (at most one tag with that key
// survives on a task — see internal/shared/tasks/operations's write-seam
// collapse):
//
//   - `triage:level` — the CONSEQUENCE ladder and the one hand-assigned
//     input to priority_fn: an integer 1..5, where 1 is the worst outcome if
//     the task is never done and 5 the mildest (docs/task-tagging-standard.md
//     is the legend that makes each integer readable). It is numeric, not a
//     word, for two reasons that both matter: a relational `--tag-query`
//     (e.g. `triage:level<=2`) can select a band of it, and priority_fn can
//     do arithmetic on it rather than enumerating every value. The declared
//     range 1,5 is what `taskloom lint` checks a stored value against, and
//     arity=scalar is what stops a task carrying two of them.
//   - `triage:kind` — a work-KIND enum (defect, capability, chore). It shapes
//     the ROT CURVE only (decay_fn, below): how a task's urgency MOVES with
//     age is a property of what shape of work it is. It is deliberately not
//     an input to priority_fn, where a kind weight would only restate
//     triage:level in a second, conflicting vocabulary.
//   - `triage:effort` — a 0-5 numeric estimate of cost; priority_fn divides
//     it back out (a costlier fix ranks lower, all else equal).
//   - `triage:blocks-release` — a scalar, valued (e.g. a semver like
//     "0.7.0"); priority_fn only ever presence-tests it (see the `=*`
//     composite key below). It also declares `tagma.type:...=semver`
//     (tagma SPEC.md §9, client-loadable type comparison), so a relational
//     `--tag-query` over it (e.g. `triage:blocks-release<=0.7.0`) orders by
//     real SemVer 2.0.0 precedence instead of tagma's own numeric grammar —
//     which rejects a two-dot value like "0.7.0" outright — see
//     internal/shared/tasks's registerTypes/typeConfigTags.
//
// `triage:exposed` (enum: wire, cli, config, on-disk, api) is declared for
// lint's enum check but is NOT arity=scalar — it, like every other flag
// below, is read purely by presence.
//
// A number of additional tags are FLAGS this schema declares no
// arity/enum/range for at all — bare presence (or, for the valued ones,
// presence of ANY value) is the entire signal:
//
//   - `triage:exposed=<surface>` and `triage:blind-gate=<gate>` are read by
//     decay_fn, which only ever presence-tests them via the `=*` composite
//     key and never reads the value itself.
//   - `triage:crashes`, `triage:data-loss`, `triage:no-workaround`,
//     `triage:security=<cwe>`, `triage:regression=<version-or-sha>` are
//     SEARCHABLE LABELS, read by neither formula. They record a fact about
//     an issue that a `--tag-query` can select on; the ranking consequence
//     of that fact is carried by triage:level, which is the axis a human
//     assigns after weighing it.
//   - `triage:exploited-in-wild` — bare; forces a task's derived Priority to
//     the ceiling regardless of what the formula computes (see
//     priority.ExploitedInWildTarget).
//
// priority_fn/decay_fn (mustache form — a `{{ns:key}}` placeholder reads a
// tag's value by its full "namespace:key"; a `{{ns:key=value}}` placeholder
// reads whether that EXACT value is present; a `{{ns:key=*}}` placeholder
// reads whether the target is present AT ALL, valued or not (see
// internal/shared/tasks/priority's resolveTagValues doc for why the bare
// "or valueless" half of that last form is load-bearing); any other
// `{{name}}` is a taskloom-provided built-in; see
// internal/shared/tasks/tagschema's CompileFormula and
// internal/shared/tasks/priority's builtin set) are evaluated read-time by
// internal/shared/tasks/priority.Compute, never stored. Exactly one of each
// may be declared project-wide; the target each is filed under is the slot
// it occupies, not a restriction on which tags it may read.
//
//   - priority_fn is a base weight of 2**(3-level) — so each step DOWN the
//     ladder halves the score: critical 4, serious 2, normal 1, minor 0.5,
//     wishlist 0.25 — multiplied by decay_fn's own age_factor, doubled if
//     the task blocks a release, and divided by a cost penalty for declared
//     effort. An UNRATED task floors at 0.1, below even wishlist, and the
//     `{{triage:level}} > 0` guard is what puts it there: an absent tag
//     resolves to 0, and 2**(3-0) is 8, so without the guard every untriaged
//     task would outrank every critical one. Sinking the unrated is
//     deliberate — the fix is to rate the task, and the per-target coverage
//     diagnostic (priority.Diagnostics.TargetCoverage) is what says how many
//     are sitting on the floor.
//   - decay_fn is a TARGETED escalation/rot curve: a currently-EXPOSED or
//     otherwise actively-exploitable (`triage:exposed`, `triage:blind-gate`)
//     issue escalates upward with age (asymptotically capped at 2x — an
//     ancient low-level exposed issue never outranks a merely fresh
//     higher-level one, see internal/shared/tasks/priority's crossover
//     invariant test), while a capability/chore-kinded task DECAYS gently
//     downward with age instead (aging low-stakes work loses urgency over
//     time, absent an active exposure signal). A defect with neither signal
//     holds steady at 1 — no escalation, no decay.
var DefaultTagSchema = []string{
	`tagma.arity:"triage:level"=scalar`,
	`tagma.arity:"triage:verdict"=scalar`,
	`tagma.arity:"triage:audited"=scalar`,
	`tagma.arity:"repo"=scalar`,
	`tagma.arity:"triage:kind"=scalar`,
	`tagma.arity:"triage:effort"=scalar`,
	`tagma.arity:"triage:blocks-release"=scalar`,
	`tagma.type:"triage:blocks-release"=` + tagschema.SemverTypeName,
	`tagma.enum:"triage:kind"="defect,capability,chore"`,
	`tagma.enum:"triage:exposed"="wire,cli,config,on-disk,api"`,
	`tagma.enum:"triage:verdict"="holds,phantom,obsolete,partial,unclear"`,
	`tagma.range:"triage:level"="1,5"`,
	`tagma.range:"triage:audited"="20250101,20991231"`,
	`tagma.range:"triage:effort"="0,5"`,
	`tagma.decay_fn:"triage:kind"="{{triage:exposed=*}} > 0 ? 1 + {{age_days}}/({{age_days}}+90) : {{triage:blind-gate=*}} > 0 ? 1 + {{age_days}}/({{age_days}}+30) : {{triage:kind=capability}} > 0 ? 0.4 + 0.6 * 0.5 ** ({{age_days}}/120) : {{triage:kind=chore}} > 0 ? 0.5 + 0.5 * 0.5 ** ({{age_days}}/180) : 1"`,
	`tagma.priority_fn:"triage:kind"="({{triage:level}} > 0 ? 2 ** (3 - {{triage:level}}) : 0.1) * {{age_factor}} * (1 + {{triage:blocks-release=*}}) / (1 + {{triage:effort}}/2)"`,
}

// Config is taskloom's own parsed, layered configuration.
type Config struct {
	// Homing selects the task-store homing mode ("home" or "repo" — see
	// paths.Mode). Empty means unset at every config layer; ResolveMode
	// silently resolves "" (with no flag either) to paths.ModeHome, the
	// pre-homing status quo — see the package doc for why that default, and
	// only that direction, is safe to pick without asking.
	Homing string `yaml:"homing,omitempty"`

	// TagSchema declares taskloom's tag-schema: a list of tagma-syntax
	// DECLARATION strings (see internal/shared/tasks/tagschema.Parse). The
	// dotted config key is "tag_schema", spelled directly in the yaml tag
	// below and in the JSON Schema, since nothing else binds it. Empty means
	// unset at every config layer;
	// ResolvedTagSchema falls back to DefaultTagSchema in that case, exactly
	// mirroring how Homing/ResolveMode default when unset (see this
	// package's doc).
	TagSchema []string `yaml:"tag_schema,omitempty"`
}

// ResolvedTagSchema returns c's TagSchema, falling back to DefaultTagSchema
// when c declares none — the same "absent is fine, defaults silently"
// policy Homing/ResolveMode already establishes for this config surface.
func (c Config) ResolvedTagSchema() []string {
	if len(c.TagSchema) > 0 {
		return c.TagSchema
	}
	return DefaultTagSchema
}

// ParsedTagSchema resolves (ResolvedTagSchema) and parses (tagschema.Parse)
// c's tag-schema declarations into a *tagschema.Schema, ready for
// internal/shared/tasks/operations's write-seam scalar-collapse. A
// malformed declaration — in either an explicit config or (a code defect in)
// DefaultTagSchema itself — is a returned error naming the offending
// declaration: fail loud, never silently run with an empty/partial schema.
func (c Config) ParsedTagSchema() (*tagschema.Schema, error) {
	return tagschema.Parse(c.ResolvedTagSchema())
}

// product builds the confload.Product describing taskloom's own on-disk/env
// conventions, exactly mirroring internal/config's ctxloomProduct — including
// leaving KnownPath NIL when validator is nil (schema failed to load), which
// is confload's own documented "no schema knowledge available" degradation.
// See ctxloomProduct's doc for why a method value on a nil pointer would
// defeat that path rather than take it.
func product(validator *schema.ConfigValidator) confload.Product {
	p := confload.Product{
		Name:      "taskloom",
		DirName:   DirName,
		FileName:  FileName,
		EnvPrefix: envPrefix,
	}
	if validator != nil {
		p.KnownPath = validator.KnownPath
		// Same override schema gate ctxloomProduct installs, for the same
		// reason: a file layer is validated before it merges, so without this
		// an env/--config-set value is the one way into this config that no
		// schema ever sees.
		p.ValidateValue = validator.ValidateAt
	}
	return p
}

// newValidator returns taskloom's own embedded config JSON Schema
// (resources/schema/input/taskloom-config-schema.json, via resources.GetSchema
// — see docsgen.go:12-18's doc for why the generator, and this validator,
// walk the hand-authored schema rather than reflecting the Config struct),
// compiled ONCE per process.
//
// The schema is a resource baked into the binary: it cannot change while the
// process runs, so the compile has exactly one possible outcome and repeating
// it buys nothing. It was repeated four times per command — loadRaw and Load
// each compile it, and every command resolves the config twice — which is
// pure waste on the startup path of a CLI that mostly does one small thing
// and exits. The compiled validator is stateless and safe to share; both the
// value and the error are cached, because a failing compile fails
// identically every time.
var newValidator = sync.OnceValues(func() (*schema.ConfigValidator, error) {
	data, err := resources.GetSchema(SchemaResourceName)
	if err != nil {
		return nil, fmt.Errorf("load taskloom config schema: %w", err)
	}
	return schema.NewValidatorFromSchema(data)
})

// homeConfigPath returns ~/.taskloom/config.yaml.
func homeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName, FileName), nil
}

// projectConfigPath returns <workDir>/.taskloom/config.yaml ("" when workDir
// is "").
func projectConfigPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, DirName, FileName)
}

// loadRaw reads and layers taskloom's home + project config.yaml (home <
// project), then resolves fs's env (TASKLOOM_CONFIG_*) and --config-set
// overrides against the result — the full confload chain, unvalidated. It is
// the seam TestConfig_ExplicitFalseBeatsInheritedTrue exercises directly (with
// a synthetic key outside taskloom's real schema) to prove the underlying
// confload.Product wiring — not just confload itself, already proven by
// TestConfload_SecondProductReusesPattern — is correct for taskloom's actual
// DirName/FileName/EnvPrefix. A malformed --config-set entry is downgraded to
// a warning (clidiag.Warn), matching ctxloom's own
// InstallOverridesFromFlags convention: one bad override never blocks
// startup.
func loadRaw(workDir string, fs *pflag.FlagSet) (map[string]any, error) {
	validator, _ := newValidator() // nil on failure: degrade, never block a load over it
	p := product(validator)

	o, oerr := p.ReadOverrides(fs)
	if oerr != nil {
		clidiag.Warn("taskloom", "config override resolution: %v", oerr)
	}

	var src confload.Sources
	if hp, herr := homeConfigPath(); herr == nil {
		src.HomePath = hp
	} else {
		// Losing a whole config LAYER is not the same class of event as a
		// layer being absent, and it must not be silent: an existing
		// ~/.taskloom/config.yaml stops applying, so taskloom can resolve a
		// different homing mode — a different task store — than it did on the
		// previous run. Degrade to project-only anyway, matching the
		// one-bad-input-never-blocks-startup policy the override warning above
		// applies.
		clidiag.Warn("taskloom", "home config location unresolved (%v): %s/%s not read", herr, DirName, FileName)
	}
	src.ProjectPath = projectConfigPath(workDir)

	merged, err := p.Load(src, o)
	if err != nil {
		return nil, err
	}
	return merged, nil
}

// Load resolves taskloom's fully-layered config for workDir (home <
// project < TASKLOOM_CONFIG_* env < --config-set, via loadRaw) and decodes it
// into Config, validating against the embedded schema first: an unknown key
// (or any other schema violation) is a returned error naming the offending
// content, mirroring — for taskloom's own, much smaller schema — the
// fail-loud unknown-key detection internal/config/unknown_keys.go gives
// ctxloom (additionalProperties:false at the schema's top level already
// rejects it; this just surfaces that rejection as an error instead of
// letting it validate silently).
func Load(workDir string, fs *pflag.FlagSet) (Config, error) {
	merged, err := loadRaw(workDir, fs)
	if err != nil {
		return Config{}, err
	}

	data, err := yaml.Marshal(merged)
	if err != nil {
		return Config{}, fmt.Errorf("taskloom: remarshal merged config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("taskloom: parse merged config: %w", err)
	}

	// A schema-compile failure must not degrade to "valid": the
	// embedded schema is a build-time resource, so this either fails
	// uniformly for every invocation or never — silently skipping
	// ValidateBytes here would mean an unknown key, a malformed tag_schema
	// shape, or any other schema violation passes straight through with no
	// signal, contradicting this function's own documented fail-loud
	// guarantee.
	v, verr := newValidator()
	if verr != nil {
		return Config{}, fmt.Errorf("taskloom: load config schema: %w", verr)
	}
	if err := v.ValidateBytes(data); err != nil {
		// Config{}, not cfg: on error this function yields no config at all,
		// the same as every other error return above. Handing back the value
		// the schema just rejected invites a caller that mishandles the error
		// to act on content that failed validation.
		return Config{}, fmt.Errorf("taskloom: config error: %w", err)
	}
	return cfg, nil
}

// ResolveMode determines the effective task-store homing mode for a taskloom
// CLI/MCP invocation touching workDir's project: taskloom's own layered
// config (Load: home < project .taskloom/config.yaml < TASKLOOM_CONFIG_HOMING
// env < --config-set homing=...), then flagValue — the dedicated --homing
// flag's current value ("" when not passed) — which wins over everything
// else, completing the documented precedence chain (home < project < env <
// CLI flag).
//
// The default sits BELOW that entire chain, not inside it: when value is ""
// after every layer has had its say, ResolveMode returns paths.ModeHome
// directly — no error, no diagnostic (see the package doc for why this
// particular default is safe to pick silently). Any layer that DOES set a
// value, however far down the chain, still wins over the default exactly as
// it would win over a lower layer's explicit value.
//
// A value that IS set but invalid (anything other than "home"/"repo", at any
// layer) is a returned error naming the bad value and the valid set — the
// distinction that matters: absent is fine, wrong is not.
func ResolveMode(workDir string, fs *pflag.FlagSet, flagValue string) (paths.Mode, error) {
	cfg, err := Load(workDir, fs)
	if err != nil {
		return "", err
	}
	return cfg.ResolveMode(flagValue)
}

// ResolveMode is the flag-vs-config half of the package-level ResolveMode,
// split out so a caller that needs BOTH the homing mode and the tag-schema
// can resolve them from ONE Load instead of layering the whole config twice
// (cmd/taskloom's taskContextSingle is that caller, and it is on the startup
// path of every command that touches a single project's store). It performs
// no I/O of its own; see ResolveMode for the precedence chain and the
// default.
func (c Config) ResolveMode(flagValue string) (paths.Mode, error) {
	value := c.Homing
	if flagValue != "" {
		value = flagValue
	}
	if value == "" {
		return paths.ModeHome, nil
	}
	switch paths.Mode(value) {
	case paths.ModeHome, paths.ModeRepo:
		return paths.Mode(value), nil
	default:
		return "", fmt.Errorf("taskloom: invalid %s value %q (must be %q or %q)",
			HomingConfigKey, value, paths.ModeHome, paths.ModeRepo)
	}
}
