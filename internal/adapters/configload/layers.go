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
	var layers []map[string]any
	appPath := filepath.Dir(projectConfigPath)
	homeAppPath := ""
	if homeConfigPath != "" {
		homeAppPath = filepath.Dir(homeConfigPath)
	}

	var homePending, projectPending *config.PendingUpgrade
	if homeConfigPath != "" {
		homeValues, pending, err := s.loadConfigLayer(b, layerscope.LayerHome, appPath, homeAppPath, homeConfigPath, fs)
		if err != nil {
			return err
		}
		homePending = pending
		if homeValues != nil {
			layers = append(layers, homeValues)
		}
	}

	// The single-file case is the PROJECT layer by default, but not when this
	// one file genuinely IS home acting alone (the bootstrap fell back to home,
	// or a pinned appDir named ~/.ctxloom): tagging it LayerProject would strip
	// every ScopeMachine value from a file that was never a committed project
	// file. The home==project DEDUP case keeps the ordinary project scope.
	layer := layerscope.LayerProject
	if homeConfigPath == "" && source == config.SourceHome {
		layer = layerscope.LayerHome
	}
	projectValues, pending, err := s.loadConfigLayer(b, layer, appPath, homeAppPath, projectConfigPath, fs)
	if err != nil {
		return err
	}
	projectPending = pending
	if projectValues != nil {
		layers = append(layers, projectValues)
	}
	b.SetPendingUpgrades(projectPending, homePending)

	if len(layers) == 0 && len(s.overrides.Env) == 0 && len(s.overrides.Flags) == 0 {
		// Neither layer exists and nothing overrides: the builder's zero
		// value, which the shipped default registry then fills.
		return nil
	}
	return s.decodeMergedLayers(b, layers)
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

	pipeline := upgrade.Pipeline{}
	if s.canonicalize != nil {
		shell := b.Shell()
		pipeline = append(pipeline, profileRefCanonicalizeUpgrade{canonical: func(ref string) string { return s.canonicalize(shell, ref) }})
	}
	if upgraded, applied := pipeline.Run(data); len(applied) > 0 {
		data = upgraded
		pending = &config.PendingUpgrade{Path: configPath, Data: upgraded, Applied: applied}
		zap.L().Info("config_upgrade_pending", zap.String("path", configPath), zap.Strings("applied", applied))
	}
	// The refusal comes before any judgement of the document: a file that is
	// not YAML has no version to be below the floor and no keys to validate.
	var raw map[string]any
	if perr := yaml.Unmarshal(data, &raw); perr != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrUnparsableLayer, configPath, perr)
	}
	if v, declared := declaredConfigVersion(data); v < config.CurrentConfigVersion {
		spelled := "no `version` key, i.e. the pre-versioning generation"
		if declared {
			spelled = fmt.Sprintf("`version: %d`", v)
		}
		strictness.FailOnce(strictness.ClassMigration,
			fmt.Sprintf("back up %s, then re-run `ctxloom init` to scaffold a current one and re-apply your settings", configPath),
			"%s carries %s but this ctxloom requires config schema version %d, and in-place upgrades have been removed — an old config is no longer rewritten on load",
			configPath, spelled, config.CurrentConfigVersion)
	}

	if s.validator != nil {
		if verr := s.validator.ValidateBytes(data); verr != nil {
			for _, w := range classifyValidationError(configPath, s.validator, verr) {
				b.Warn(w.Kind, "%s", w.Text)
			}
			zap.L().Warn("config_validation_warning", zap.String("path", configPath), zap.Error(verr))
		}
	}

	for _, v := range config.DropLayerScopeViolations(layer, raw) {
		b.Warn(config.WarnKindLayerScope, "%s", v.Message(appPath, homeAppPath))
		zap.L().Warn("config_layer_scope_warning", zap.String("path", configPath), zap.Strings("key", v.Path))
	}

	for _, w := range dropEnginelessAgents(configPath, raw) {
		b.Warn(w.Kind, "%s", w.Text)
		zap.L().Warn("config_engineless_agent_warning", zap.String("path", configPath), zap.String("warning", w.Text))
	}

	zap.L().Debug("config_loaded", zap.String("path", configPath))
	return raw, pending, nil
}
