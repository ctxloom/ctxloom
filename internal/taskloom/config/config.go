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
// task log lives. taskloom refuses to guess it — ResolveMode fails loud,
// naming both the config key and the --homing flag, when neither config
// layer nor the flag decides it. That fail-loud policy is deliberately
// scoped to taskloom's OWN CLI/MCP entry points (cmd/taskloom's
// taskContextSingle, called immediately before any command touches a SINGLE
// project's store) — every other caller of
// internal/shared/tasks/operations.TaskContext (ctxloom's own internal/cli,
// internal/operations, internal/lm/isolation) never sets HomingMode at all,
// so it defaults to paths.ModeHome exactly like every call site behaved
// before this package existed. Baking the fail-loud requirement into the
// shared operations package itself would have broken every one of those
// callers — none of them can supply a --homing flag, and forcing every
// existing project to author a NEW .taskloom/config.yaml before ctxloom's
// own trigger evaluation could run again is not this task's mandate.
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
)

// Config is taskloom's own parsed, layered configuration.
type Config struct {
	// Homing selects the task-store homing mode ("home" or "repo" — see
	// paths.Mode). Empty means unset at every config layer; ResolveMode is
	// the fail-loud gate that turns "" (with no flag either) into an
	// actionable error instead of a silent default.
	Homing string `yaml:"homing,omitempty"`
}

// FailLoudMessage is the exact diagnostic ResolveMode returns when NEITHER a
// home/project config.yaml, TASKLOOM_CONFIG_HOMING, --config-set, nor the
// dedicated --homing flag sets the task-store homing mode. It names both the
// config key and the flag, per the fail-loud requirement: taskloom must never
// silently pick a default.
const FailLoudMessage = "taskloom: task-store location is not configured — " +
	"set `" + HomingConfigKey + ": home` or `" + HomingConfigKey + ": repo` in .taskloom/config.yaml " +
	"(home or project), or pass --" + HomingFlagName + " home or --" + HomingFlagName + " repo"

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
// CLI flag). Returns an error whose text is exactly FailLoudMessage when
// nothing at any layer sets it, and a distinct error for a set-but-invalid
// value (anything other than "home"/"repo").
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
		return "", fmt.Errorf("%s", FailLoudMessage)
	}
	switch paths.Mode(value) {
	case paths.ModeHome, paths.ModeRepo:
		return paths.Mode(value), nil
	default:
		return "", fmt.Errorf("taskloom: invalid %s value %q (must be %q or %q)",
			HomingConfigKey, value, paths.ModeHome, paths.ModeRepo)
	}
}
