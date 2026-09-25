package configload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/config/layerscope"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// ErrUnparsableLayer is the refusal for a config layer that EXISTS and cannot
// be parsed. An absent layer is the shipped default; a present one the
// process cannot read as YAML is not a config it may proceed on, so the
// refusal names the file rather than dropping the layer with a warning.
var ErrUnparsableLayer = errors.New("config layer cannot be parsed")

// resolveConfigLayerPaths computes the value-layering inputs from the
// bootstrap decision: the project layer's config.yaml (always present as a
// path, whether or not the file exists) and, when appPath is a real project,
// the home layer's config.yaml — or "" when home cannot be resolved or IS
// the project (a home-only run), so the caller reads one file exactly as
// before layering existed.
func resolveConfigLayerPaths(appPath string, source config.ConfigSource) (projectConfigPath, homeConfigPath string) {
	projectConfigPath = paths.ConfigPath(appPath)
	if source != config.SourceProject {
		return projectConfigPath, ""
	}
	homeAppDir, err := paths.HomeConfigDir()
	if err != nil {
		return projectConfigPath, ""
	}
	candidate := paths.ConfigPath(homeAppDir)
	if candidate == projectConfigPath {
		return projectConfigPath, ""
	}
	return projectConfigPath, candidate
}

// loadLayeredConfig reads every participating layer (home, then project —
// ascending precedence), deep-merges their decoded values (home < project;
// lists replace, an explicit zero beats inheritance), resolves overrides
// (env then flags) against the result, and decodes into the builder once.
//
// Each layer is upgraded, schema-validated and warned about INDEPENDENTLY
// before merging — never the merged result — so an unknown key or a stale
// schema generation is diagnosed with its own file's path, and a key valid
// in one layer but not the other still fails loudly.
//
// Overrides resolve against the merged files however many exist, INCLUDING
// none: a fresh project with only env/CLI values set still gets them.
func (s *Sources) loadLayeredConfig(_ context.Context, b *config.Builder, homeConfigPath, projectConfigPath string, fs afero.Fs, source config.ConfigSource) error {
	appPath := filepath.Dir(projectConfigPath)
	homeAppPath := ""
	if homeConfigPath != "" {
		homeAppPath = filepath.Dir(homeConfigPath)
	}

	homeValues, homePending, err := s.loadHomeLayer(b, appPath, homeAppPath, homeConfigPath, fs)
	if err != nil {
		return err
	}
	layers := appendLayer(nil, homeValues)

	projectValues, projectPending, err := s.loadConfigLayer(b, projectLayerScope(homeConfigPath, source), appPath, homeAppPath, projectConfigPath, fs)
	if err != nil {
		return err
	}
	layers = appendLayer(layers, projectValues)
	b.SetPendingUpgrades(projectPending, homePending)

	if len(layers) == 0 && s.noOverrides() {
		// Neither layer exists and nothing overrides: the builder's zero
		// value, which the shipped default registry then fills.
		return nil
	}
	return s.decodeMergedLayers(b, layers)
}

// loadHomeLayer is the home layer, when there is a home config to read.
func (s *Sources) loadHomeLayer(b *config.Builder, appPath, homeAppPath, homeConfigPath string, fs afero.Fs) (map[string]any, *config.PendingUpgrade, error) {
	if homeConfigPath == "" {
		return nil, nil, nil
	}
	return s.loadConfigLayer(b, layerscope.LayerHome, appPath, homeAppPath, homeConfigPath, fs)
}

// projectLayerScope is the scope the project-path file is loaded under.
// The single-file case is the PROJECT layer by default, but not when this
// one file genuinely IS home acting alone (the bootstrap fell back to home,
// or a pinned appDir named ~/.ctxloom): tagging it LayerProject would strip
// every ScopeMachine value from a file that was never a committed project
// file. The home==project DEDUP case keeps the ordinary project scope.
func projectLayerScope(homeConfigPath string, source config.ConfigSource) layerscope.Layer {
	if homeConfigPath == "" && source == config.SourceHome {
		return layerscope.LayerHome
	}
	return layerscope.LayerProject
}

// appendLayer appends a layer's values when the layer exists.
func appendLayer(layers []map[string]any, values map[string]any) []map[string]any {
	if values == nil {
		return layers
	}
	return append(layers, values)
}

// noOverrides reports whether neither env nor flags override anything.
func (s *Sources) noOverrides() bool {
	return len(s.overrides.Env) == 0 && len(s.overrides.Flags) == 0
}

// splitJoinedErrors unwraps an errors.Join result (or a plain single error)
// into its constituents, so each can be classified by TYPE rather than the
// whole blob treated as one kind.
func splitJoinedErrors(err error) []error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		return joined.Unwrap()
	}
	return []error{err}
}

// decodeMergedLayers merges the file layers, resolves overrides against the
// result, and decodes into the builder. A layer that cannot be READ is the
// caller's refusal; from the merge onward every fault is a warning, because
// the files have been read and validated and the remaining steps are ours.
func (s *Sources) decodeMergedLayers(b *config.Builder, layers []map[string]any) error {
	product := s.product()
	merged, mergeErr := product.MergeLayers(layers...)
	if mergeErr != nil {
		return fmt.Errorf("merging config layers: %w", mergeErr)
	}
	merged, overrideErr := product.ApplyOverrides(merged, s.overrides)
	if overrideErr != nil {
		// A scope-driven drop and a schema refusal are classified as the SAME
		// kinds the per-file-layer checks record, so the strict startup gate
		// reports both routes to one problem identically; every other override
		// fault keeps the coarser parse classification.
		for _, sub := range splitJoinedErrors(overrideErr) {
			var scopeErr *confload.ScopeViolationError
			var schemaErr *confload.SchemaViolationError
			switch {
			case errors.As(sub, &scopeErr):
				b.Warn(config.WarnKindLayerScope, "config override resolution: %v", sub)
			case errors.As(sub, &schemaErr):
				b.Warn(config.WarnKindValidate, "config override resolution: %v", sub)
			default:
				b.Warn(config.WarnKindParse, "config override resolution: %v", sub)
			}
		}
		zap.L().Warn("config_override_warning", zap.Error(overrideErr))
	}
	b.Decode(merged)
	return nil
}

// loadConfigLayer reads and processes ONE config.yaml layer: in-memory
// upgrade, schema version check, schema validation, layer-scope and
// engineless-agent drops — each recorded against this file's own path.
// An absent file is nil values and no error. A present file that cannot be
// parsed is ErrUnparsableLayer, naming the file. pending is this layer's own
// in-memory upgrade, nil when the file is current.
func (s *Sources) loadConfigLayer(b *config.Builder, layer layerscope.Layer, appPath, homeAppPath, configPath string, fs afero.Fs) (values map[string]any, pending *config.PendingUpgrade, err error) {
	data, readErr := afero.ReadFile(fs, configPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, nil, nil
		}
		b.Warn(config.WarnKindRead, "failed to read config at %s: %v", configPath, readErr)
		zap.L().Warn("config_read_warning", zap.String("path", configPath), zap.Error(readErr))
		return nil, nil, nil
	}

	data, pending = upgradeLayer(s.upgradePipeline(b), data, configPath)
	// The refusal comes before any judgement of the document: a file that is
	// not YAML has no version to be below the floor and no keys to validate.
	var raw map[string]any
	if perr := yaml.Unmarshal(data, &raw); perr != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrUnparsableLayer, configPath, perr)
	}
	refuseStaleConfigVersion(data, configPath)
	s.warnInvalidLayer(b, data, configPath)
	warnLayerDrops(b, layer, raw, appPath, homeAppPath, configPath)

	zap.L().Debug("config_loaded", zap.String("path", configPath))
	return raw, pending, nil
}

// upgradePipeline is the in-memory upgrade every layer runs through: the
// profile-ref canonicalization, when a canonicalizer is composed.
func (s *Sources) upgradePipeline(b *config.Builder) upgrade.Pipeline {
	pipeline := upgrade.Pipeline{}
	if s.canonicalize != nil {
		shell := b.Shell()
		pipeline = append(pipeline, profileRefCanonicalizeUpgrade{canonical: func(ref string) string { return s.canonicalize(shell, ref) }})
	}
	return pipeline
}

// upgradeLayer runs the pipeline over a layer's bytes, returning the bytes
// to judge and, when any upgrade applied, the pending upgrade to record.
func upgradeLayer(pipeline upgrade.Pipeline, data []byte, configPath string) ([]byte, *config.PendingUpgrade) {
	upgraded, applied := pipeline.Run(data)
	if len(applied) == 0 {
		return data, nil
	}
	zap.L().Info("config_upgrade_pending", zap.String("path", configPath), zap.Strings("applied", applied))
	return upgraded, &config.PendingUpgrade{Path: configPath, Data: upgraded, Applied: applied}
}

// refuseStaleConfigVersion records the migration refusal for a layer whose
// schema version is below this build's.
func refuseStaleConfigVersion(data []byte, configPath string) {
	v, declared := declaredConfigVersion(data)
	if v >= config.CurrentConfigVersion {
		return
	}
	spelled := "no `version` key, i.e. the pre-versioning generation"
	if declared {
		spelled = fmt.Sprintf("`version: %d`", v)
	}
	strictness.FailOnce(report.KindMigration,
		fmt.Sprintf("back up %s, then re-run `ctxloom init` to scaffold a current one and re-apply your settings", configPath),
		"%s carries %s but this ctxloom requires config schema version %d, and in-place upgrades have been removed — an old config is no longer rewritten on load",
		configPath, spelled, config.CurrentConfigVersion)
}

// warnInvalidLayer warns, per classified cause, about a layer the schema
// validator rejects.
func (s *Sources) warnInvalidLayer(b *config.Builder, data []byte, configPath string) {
	if s.validator == nil {
		return
	}
	verr := s.validator.ValidateBytes(data)
	if verr == nil {
		return
	}
	for _, w := range classifyValidationError(configPath, s.validator, verr) {
		b.Warn(w.Kind, "%s", w.Text)
	}
	zap.L().Warn("config_validation_warning", zap.String("path", configPath), zap.Error(verr))
}

// warnLayerDrops drops (with a warning each) the keys this layer may not set
// and the agents that name no engine.
func warnLayerDrops(b *config.Builder, layer layerscope.Layer, raw map[string]any, appPath, homeAppPath, configPath string) {
	for _, v := range config.DropLayerScopeViolations(layer, raw) {
		b.Warn(config.WarnKindLayerScope, "%s", v.Message(appPath, homeAppPath))
		zap.L().Warn("config_layer_scope_warning", zap.String("path", configPath), zap.Strings("key", v.Path))
	}
	for _, w := range dropEnginelessAgents(configPath, raw) {
		b.Warn(w.Kind, "%s", w.Text)
		zap.L().Warn("config_engineless_agent_warning", zap.String("path", configPath), zap.String("warning", w.Text))
	}
}
