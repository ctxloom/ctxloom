package configload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/config/layerscope"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
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
// Each layer is version-gated, schema-validated and warned about
// INDEPENDENTLY before merging — never the merged result — so an unknown key
// or an unreadable format generation is diagnosed with its own file's path,
// and a key valid in one layer but not the other still fails loudly.
//
// Overrides resolve against the merged files however many exist, INCLUDING
// none: a fresh project with only env/CLI values set still gets them.
func (s *Sources) loadLayeredConfig(_ context.Context, b *config.Builder, homeConfigPath, projectConfigPath string, fs afero.Fs, source config.ConfigSource) error {
	appPath := filepath.Dir(projectConfigPath)
	homeAppPath := ""
	if homeConfigPath != "" {
		homeAppPath = filepath.Dir(homeConfigPath)
	}

	homeValues, err := s.loadHomeLayer(b, appPath, homeAppPath, homeConfigPath, fs)
	if err != nil {
		return err
	}
	layers := appendLayer(nil, homeValues)

	projectValues, err := s.loadConfigLayer(b, projectLayerScope(homeConfigPath, source), appPath, homeAppPath, projectConfigPath, fs)
	if err != nil {
		return err
	}
	layers = appendLayer(layers, projectValues)

	if len(layers) == 0 && s.noOverrides() {
		// Neither layer exists and nothing overrides: the builder's zero
		// value, which the shipped default registry then fills.
		return nil
	}
	return s.decodeMergedLayers(b, layers)
}

// loadHomeLayer is the home layer, when there is a home config to read.
func (s *Sources) loadHomeLayer(b *config.Builder, appPath, homeAppPath, homeConfigPath string, fs afero.Fs) (map[string]any, error) {
	if homeConfigPath == "" {
		return nil, nil
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

// loadConfigLayer reads and processes ONE config.yaml layer: the version
// gate on its raw bytes, the in-memory normalization, schema validation,
// layer-scope and engineless-agent drops — each recorded against this file's
// own path. An absent file is nil values and no error. A present file that
// cannot be parsed is ErrUnparsableLayer, naming the file. Under
// --write-upgrades a layer the gate or the normalization changed is written
// back.
func (s *Sources) loadConfigLayer(b *config.Builder, layer layerscope.Layer, appPath, homeAppPath, configPath string, fs afero.Fs) (map[string]any, error) {
	data, readErr := afero.ReadFile(fs, configPath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, nil
		}
		b.Warn(config.WarnKindRead, "failed to read config at %s: %v", configPath, readErr)
		zap.L().Warn("config_read_warning", zap.String("path", configPath), zap.Error(readErr))
		return nil, nil
	}

	r, refused := configKind.Upgrade(data)
	if refused != nil {
		refuseConfigVersion(configPath, refused)
		r = schemaver.Result{Data: data}
	}
	r = s.normalize(b, r)
	if refused == nil {
		if err := persistUpgrade(fs, configPath, r); err != nil {
			return nil, err
		}
	}
	var raw map[string]any
	if perr := yaml.Unmarshal(r.Data, &raw); perr != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnparsableLayer, configPath, perr)
	}
	if refused == nil {
		// A refused layer's keys belong to a generation this schema does not
		// describe; judging them would bury the one finding that matters.
		s.warnInvalidLayer(b, r.Data, configPath)
	}
	warnLayerDrops(b, layer, raw, appPath, homeAppPath, configPath)

	zap.L().Debug("config_loaded", zap.String("path", configPath))
	return raw, nil
}

// normalize runs the context-dependent normalization over a layer already
// past the version gate: the profile-ref canonicalization, when a
// canonicalizer is composed. It is not a schema step — what it rewrites
// depends on the remotes registry, not on the document's generation — but
// what it changes is persisted with the migration under --write-upgrades.
func (s *Sources) normalize(b *config.Builder, r schemaver.Result) schemaver.Result {
	if s.canonicalize == nil {
		return r
	}
	shell := b.Shell()
	pipeline := upgrade.Pipeline{profileRefCanonicalizeUpgrade{canonical: func(ref string) string { return s.canonicalize(shell, ref) }}}
	out, applied := pipeline.Run(r.Data)
	if len(applied) == 0 {
		return r
	}
	r.Data, r.Applied = out, append(r.Applied, applied...)
	return r
}

// persistUpgrade writes a changed layer back when this invocation asked for
// it (--write-upgrades); otherwise the change lives in memory only.
func persistUpgrade(fs afero.Fs, configPath string, r schemaver.Result) error {
	if len(r.Applied) == 0 {
		return nil
	}
	zap.L().Info("config_upgraded_in_memory", zap.String("path", configPath), zap.Strings("applied", r.Applied))
	if !schemaver.WriteUpgrades() {
		return nil
	}
	if err := schemaver.WriteBack(fs, configPath, r, nil); err != nil {
		return err
	}
	clidiag.Warn("ctxloom", "upgraded %s to %s %d (%s; the previous file is kept as %s%s)",
		configPath, schemaver.Key, r.To, strings.Join(r.Applied, ", "), configPath, schemaver.BackupSuffix)
	return nil
}

// refuseConfigVersion records the migration refusal for a layer whose format
// generation this binary cannot read: older than it migrates, newer than it
// knows, or unreadable.
func refuseConfigVersion(configPath string, refused error) {
	remedy := fmt.Sprintf("back up %s, then re-run `ctxloom init` to scaffold a current one and re-apply your settings", configPath)
	if errors.Is(refused, schemaver.ErrNewer) {
		remedy = fmt.Sprintf("upgrade ctxloom: %s was written by a newer one", configPath)
	}
	strictness.FailOnce(report.KindMigration, remedy, "%s: %v", configPath, refused)
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
		b.WarnRemedy(config.WarnKindLayerScope, v.Remedy(appPath, homeAppPath), "%s", v.Message(appPath, homeAppPath))
		zap.L().Warn("config_layer_scope_warning", zap.String("path", configPath), zap.Strings("key", v.Path))
	}
	for _, w := range dropEnginelessAgents(configPath, raw) {
		b.Warn(w.Kind, "%s", w.Text)
		zap.L().Warn("config_engineless_agent_warning", zap.String("path", configPath), zap.String("warning", w.Text))
	}
}
