package config

import (
	"path/filepath"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// PendingUpgrade is a schema upgrade the reader applied in memory to one
// config layer and has NOT written back: Data is the upgraded document, Path
// the file it came from. CommitUpgrade / CommitHomeUpgrade persist it after
// the caller has prompted.
type PendingUpgrade struct {
	Path    string
	Data    []byte
	Applied []string
}

// Builder is the reading half's hand-off into the value: adapters/configload
// learns WHICH directories participate, reads and merges the layers, and
// records what it saw here; Build hands out the Config. Nothing but the
// reader constructs one, and nothing mutates a Config after Build — the
// Owner binds the generation's catalog and gate before publication and that
// is the whole of it.
type Builder struct {
	cfg *Config
}

// NewBuilder starts a Config for the layer set rooted at appDir. injectedFS
// records whether fs was supplied by the caller (Save and Update skip the
// cross-process file lock for an injected filesystem, which has no other
// process reading it).
func NewBuilder(fs afero.Fs, injectedFS bool, appDir string, source ConfigSource) *Builder {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	cfg := &Config{
		lm:         LMConfig{Configs: make(map[string]LLMConfig)},
		fs:         fs,
		injectedFS: injectedFS,
		appPaths:   []string{appDir},
		appDir:     appDir,
		appRoot:    filepath.Dir(appDir),
		source:     source,
	}
	return &Builder{cfg: cfg}
}

// Shell is the Config under construction, for the reader's steps that need
// the resolved directories before any layer is decoded (the remotes
// registry the profile-ref canonicalizer consults lives under appDir).
func (b *Builder) Shell() *Config { return b.cfg }

// Warn records one load-path degradation of kind k. The strict-startup gate
// keys exclusively on these, so every "record and continue" in the reader
// goes through here rather than through a log line nothing can see.
func (b *Builder) Warn(k WarningKind, format string, args ...any) {
	b.cfg.warn(k, "", format, args...)
}

// WarnRemedy is Warn for a degradation whose raise site knows a fix more
// specific than its kind's generic one (see Warning.Remedy).
func (b *Builder) WarnRemedy(k WarningKind, remedy, format string, args ...any) {
	b.cfg.warn(k, remedy, format, args...)
}

// SetPendingUpgrades records the project (or sole) layer's and the home
// layer's pending schema upgrades; nil means that layer was current.
func (b *Builder) SetPendingUpgrades(project, home *PendingUpgrade) {
	b.cfg.pendingUpgrade = project
	b.cfg.homePendingUpgrade = home
}

// Decode populates the value from the merged, override-resolved document.
// It goes through YAML rather than a map walk so the value decodes exactly
// as ParseConfig decodes a single file (case-preserving keys, the same
// UnmarshalYAML), and a document the value cannot decode is a warning, not a
// refusal: the layers were read and validated — this failure is ours.
func (b *Builder) Decode(merged map[string]any) {
	mergedYAML, err := yaml.Marshal(merged)
	if err != nil {
		b.cfg.warn(WarnKindParse, "", "failed to remarshal layered config: %v", err)
		zap.L().Warn("config_layer_remarshal_warning", zap.Error(err))
		return
	}
	if err := yaml.Unmarshal(mergedYAML, b.cfg); err != nil {
		b.cfg.warn(WarnKindParse, "", "failed to parse layered config: %v", err)
		zap.L().Warn("config_parse_warning", zap.Error(err))
	}
}

// OverlayDefaultRegistry fills the LLM registry from the shipped default
// config when the user configured no LLMs at all. It is a whole-registry
// fallback, never a per-key overlay: injecting default labels into a
// non-empty user registry would defeat the single-entry selection rule, so a
// non-empty registry is left untouched. The overlay is remembered so Save can
// strip what the user never authored (userAuthoredLM).
func (b *Builder) OverlayDefaultRegistry(defaultConfig []byte) {
	cfg := b.cfg
	if len(defaultConfig) == 0 {
		return
	}
	var def Config
	if err := yaml.Unmarshal(defaultConfig, &def); err != nil {
		zap.L().Warn("default_config_parse_failed", zap.Error(err))
		return
	}
	if len(cfg.lm.Configs) > 0 {
		return
	}
	// cfg gets its own DEEP copy: the overlay snapshot must stay pristine so a
	// later in-place registry mutation isn't mistaken for "still the default"
	// and stripped by Save. Each entry's Body map would otherwise be shared.
	overlay := LMConfig{Configs: def.lm.Configs}
	cfg.lm.Configs = make(map[string]LLMConfig, len(def.lm.Configs))
	for label, entry := range def.lm.Configs {
		entry.Body = deepCopyBody(entry.Body)
		cfg.lm.Configs[label] = entry
	}
	if cfg.lm.Defaults.Primary == "" {
		cfg.lm.Defaults.Primary = def.lm.Defaults.Primary
		overlay.Defaults.Primary = def.lm.Defaults.Primary
	}
	if cfg.lm.Defaults.Fast == "" {
		cfg.lm.Defaults.Fast = def.lm.Defaults.Fast
		overlay.Defaults.Fast = def.lm.Defaults.Fast
	}
	cfg.lmDefaultOverlay = &overlay
}

// BindVersionResolver attaches the resolver that materializes a pinned
// historical version of a remote bundle; the reader supplies it because
// fetching is an adapter's job.
func (b *Builder) BindVersionResolver(r bundles.BundleVersionResolver) {
	b.cfg.versionResolver = r
}

// BindProfileResolvers attaches the generation's remotes-registry lookups:
// remote maps an installed profile's local name to the short remote it came
// from, remoteURL maps a remote alias to its repository URL. The reader opens
// the registry once per read, before any layer is decoded, because the layer
// upgrade's profile-ref canonicalizer already consults it through Shell. A nil
// function means no registry.
func (b *Builder) BindProfileResolvers(remote, remoteURL func(string) string) {
	b.cfg.profileRemote = remote
	b.cfg.profileRemoteURL = remoteURL
}

// Build hands out the value. The Builder is spent.
func (b *Builder) Build() *Config {
	cfg := b.cfg
	b.cfg = nil
	return cfg
}
