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

	// EnvPrefix mirrors ctxloom's own CTXLOOM_CONFIG_ convention (see
	// internal/config's ctxloomProduct), scoped to taskloom so the two never
	// collide even when both binaries run in the same process tree.
	EnvPrefix = "TASKLOOM_CONFIG_"

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

	// HomingFlagName is taskloom's dedicated root flag naming the same
	// setting for a single invocation (highest precedence — see ResolveMode).
	HomingFlagName = "homing"

	// TagSchemaConfigKey is the dotted config key holding the tag-schema
	// declaration list (see the TagSchema field and internal/shared/tasks/
	// tagschema).
	TagSchemaConfigKey = "tag_schema"
)

// DefaultTagSchema is the tag_schema shipped when a project's config leaves
// the key unset at every layer: the triage-classification standard's
// baseline. Shipping this by default means a fresh project gets the full
// standard — scalar-collapse, derived priority, and lint coverage — with no
// opt-in required, the same ergonomics `homing`'s own default (see the
// package doc) established the precedent for.
//
// This is REV 2 of the standard. Rev 1 scored a quality-attribute enum
// (`triage:impact`, 9 values) and a 0-5 `triage:severity` scalar directly.
// Measured distributions across several large FOSS trackers (Debian, KDE,
// Mozilla, FreeBSD, GNOME, Godot, Kubernetes) showed no project scores a
// quality-attribute dimension as a priority input, and that an effective
// triage vocabulary collapses to roughly three kind-of-work buckets. Rev 2
// replaces both the enum and the scalar with a small discrete `triage:kind`
// plus a set of independently-checkable FACTUAL flags — priority is DERIVED
// from which flags are present, not hand-assigned. No `triage:impact`/
// `triage:severity` tag exists in any real task store as of this rev, so
// this is a clean replacement, not a migration.
//
// Three declared targets, each arity=scalar (at most one tag with that key
// survives on a task — see internal/shared/tasks/operations's write-seam
// collapse):
//
//   - `triage:kind` — a work-KIND enum (defect, capability, chore) and the
//     target priority_fn/decay_fn are declared on (see below).
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
// A number of additional tags are FLAGS this schema's formulas read but
// declares no arity/enum/range for at all — bare presence (or, for the
// valued ones, presence of ANY value) is the entire signal:
//
//   - `triage:crashes`, `triage:data-loss`, `triage:no-workaround` — bare
//     modifier tags.
//   - `triage:security=<cwe>`, `triage:regression=<version-or-sha>`,
//     `triage:blind-gate=<gate>` — conventionally valued, but bare is also
//     accepted (priority_fn/decay_fn only ever presence-test them via the
//     `=*` composite key, never read the value itself).
//   - `triage:exploited-in-wild` — bare; forces a task's derived Priority to
//     the ceiling regardless of what the formula computes (existing
//     behaviour, unchanged — see priority.ExploitedInWildTarget).
//
// priority_fn/decay_fn (mustache form — a `{{ns:key}}` placeholder reads a
// tag's value by its full "namespace:key"; a `{{ns:key=value}}` placeholder
// reads whether that EXACT value is present; a `{{ns:key=*}}` placeholder
// reads whether the target is present AT ALL, valued or not (see
// internal/shared/tasks/priority's resolveTagValues doc for why the bare
// "or valueless" half of that last form is load-bearing); any other
// `{{name}}` is a taskloom-provided built-in; see
// internal/shared/tasks/tagschema's CompileFormula and
// internal/shared/tasks/priority's builtin set) are declared on
// `triage:kind` and evaluated read-time by
// internal/shared/tasks/priority.Compute, never stored:
//
//   - priority_fn assigns a base weight by triage:kind (defect=3,
//     capability=2, chore=1), multiplies it by a risk bump for each present
//     factual flag (data-loss, security, no-workaround, crashes,
//     regression), by decay_fn's own age_factor, again if the task blocks a
//     release, and divides by a cost penalty for declared effort.
//   - decay_fn is a TARGETED escalation/rot curve: a currently-EXPOSED or
//     otherwise actively-exploitable (`triage:exposed`, `triage:blind-gate`)
//     issue escalates upward with age (asymptotically capped at 2x — an
//     ancient low-weight exposed issue never outranks a merely fresh
//     higher-weight one, see internal/shared/tasks/priority's crossover
//     invariant test), while a capability/chore-kinded task DECAYS gently
//     downward with age instead (aging low-stakes work loses urgency over
//     time, absent an active exposure signal). A defect with neither signal
//     holds steady at 1 — no escalation, no decay.
var DefaultTagSchema = []string{
	`tagma.arity:"triage:kind"=scalar`,
	`tagma.arity:"triage:effort"=scalar`,
	`tagma.arity:"triage:blocks-release"=scalar`,
	`tagma.type:"triage:blocks-release"=` + tagschema.SemverTypeName,
	`tagma.enum:"triage:kind"="defect,capability,chore"`,
	`tagma.enum:"triage:exposed"="wire,cli,config,on-disk,api"`,
	`tagma.range:"triage:effort"="0,5"`,
	`tagma.decay_fn:"triage:kind"="{{triage:exposed=*}} > 0 ? 1 + {{age_days}}/({{age_days}}+90) : {{triage:blind-gate=*}} > 0 ? 1 + {{age_days}}/({{age_days}}+30) : {{triage:kind=capability}} > 0 ? 0.4 + 0.6 * 0.5 ** ({{age_days}}/120) : {{triage:kind=chore}} > 0 ? 0.5 + 0.5 * 0.5 ** ({{age_days}}/180) : 1"`,
	`tagma.priority_fn:"triage:kind"="({{triage:kind=defect}} > 0 ? 3 : {{triage:kind=capability}} > 0 ? 2 : 1) * (1 + {{triage:data-loss}} + {{triage:security=*}} + 0.75*{{triage:no-workaround}} + 0.5*{{triage:crashes}} + 0.5*{{triage:regression=*}}) * {{age_factor}} * (1 + {{triage:blocks-release=*}}) / (1 + {{triage:effort}}/2)"`,
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
	// DECLARATION strings (see internal/shared/tasks/tagschema.Parse and
	// TagSchemaConfigKey's doc). Empty means unset at every config layer;
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
// conventions, exactly mirroring internal/config's ctxloomProduct. validator
// may be nil (schema failed to load) — schema.ConfigValidator.KnownPath is
// nil-receiver-safe, degrading to "nothing recognized" rather than panicking,
// matching ctxloom's own fault tolerance.
func product(validator *schema.ConfigValidator) confload.Product {
	return confload.Product{
		Name:      "taskloom",
		DirName:   DirName,
		FileName:  FileName,
		EnvPrefix: EnvPrefix,
		KnownPath: validator.KnownPath,
	}
}

// NewValidator compiles taskloom's own embedded config JSON Schema
// (resources/schema/input/taskloom-config-schema.json, via resources.GetSchema
// — see docsgen.go:12-18's doc for why the generator, and this validator,
// walk the hand-authored schema rather than reflecting the Config struct).
func NewValidator() (*schema.ConfigValidator, error) {
	data, err := resources.GetSchema(SchemaResourceName)
	if err != nil {
		return nil, fmt.Errorf("load taskloom config schema: %w", err)
	}
	return schema.NewValidatorFromSchema(data)
}

// HomeConfigPath returns ~/.taskloom/config.yaml.
func HomeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName, FileName), nil
}

// ProjectConfigPath returns <workDir>/.taskloom/config.yaml ("" when workDir
// is "").
func ProjectConfigPath(workDir string) string {
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
	validator, _ := NewValidator() // nil on failure: degrade, never block a load over it
	p := product(validator)

	o, oerr := p.ReadOverrides(fs)
	if oerr != nil {
		clidiag.Warn("taskloom", "config override resolution: %v", oerr)
	}

	var src confload.Sources
	if hp, herr := HomeConfigPath(); herr == nil {
		src.HomePath = hp
	}
	src.ProjectPath = ProjectConfigPath(workDir)

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

	if v, verr := NewValidator(); verr == nil {
		if err := v.ValidateBytes(data); err != nil {
			return cfg, fmt.Errorf("taskloom: config error: %w", err)
		}
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
	value := cfg.Homing
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
