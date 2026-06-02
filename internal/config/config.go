package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/collections"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/upgrade"
)

// Re-export path constants for backwards compatibility
const (
	AppDirName     = paths.AppDirName
	ConfigFileName = paths.ConfigFileName
	BundlesDir     = paths.BundlesDir
)

// ConfigSource indicates where the configuration was loaded from.
type ConfigSource int

const (
	// SourceProject means config was loaded from a project .ctxloom directory.
	SourceProject ConfigSource = iota
	// SourceHome means config was loaded from user home ~/.ctxloom directory.
	SourceHome
)

// Config holds the ctxloom configuration.
type Config struct {
	Version  int                `mapstructure:"version" yaml:"version"` // config schema version (integer; distinct from app version)
	LM       LMConfig           `mapstructure:"llm" yaml:"llm"`
	Editor   EditorConfig       `mapstructure:"editor"`
	Defaults Defaults           `mapstructure:"defaults"`
	Sync     SyncConfig         `mapstructure:"sync"`
	Hooks    HooksConfig        `mapstructure:"hooks"`
	MCP      MCPConfig          `mapstructure:"mcp"`
	Profiles map[string]Profile `mapstructure:"profiles"`
	AppPaths []string           // Resolved .ctxloom directory (at most one)
	AppRoot  string             // Project root (parent of .ctxloom directory)
	AppDir   string             // Full path to the .ctxloom directory
	Source   ConfigSource       // Where the configuration was loaded from
	Warnings []string           // Non-fatal warnings collected during load

	// PendingUpgrade is set when Load upgraded an older on-disk schema to the
	// current one in memory. The upgraded bytes are NOT persisted automatically;
	// an interactive caller may prompt the user and call CommitUpgrade. Nil when
	// the file was already current.
	PendingUpgrade *upgrade.Pending

	fs afero.Fs // Filesystem for file operations (nil = OS filesystem)
}

// LoadOption is a functional option for Load.
type LoadOption func(*loadOptions)

type loadOptions struct {
	fs     afero.Fs
	appDir string // Override ctxloom directory discovery
}

// WithFS sets the filesystem for config operations.
func WithFS(fs afero.Fs) LoadOption {
	return func(o *loadOptions) {
		o.fs = fs
	}
}

// WithAppDir sets a specific ctxloom directory instead of discovering it.
func WithAppDir(dir string) LoadOption {
	return func(o *loadOptions) {
		o.appDir = dir
	}
}

// EditorConfig holds editor-related configuration.
type EditorConfig struct {
	Command string   `mapstructure:"command" yaml:"command,omitempty"` // Editor command (default: nano)
	Args    []string `mapstructure:"args" yaml:"args,omitempty"`       // Additional arguments
}

// GetEditorCommand returns the editor command to use.
// Checks in order: config, VISUAL env, EDITOR env, then defaults to nano.
func (c *Config) GetEditorCommand() (string, []string) {
	// Config takes precedence
	if c.Editor.Command != "" {
		return c.Editor.Command, c.Editor.Args
	}

	// Check VISUAL environment variable
	if visual := os.Getenv("VISUAL"); visual != "" {
		return visual, nil
	}

	// Check EDITOR environment variable
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor, nil
	}

	// Default to nano
	return "nano", nil
}

// ExplicitDefaultProfiles returns profiles named in the canonical
// defaults.profiles array. This does NOT apply the single-profile fallback
// used by GetDefaultProfiles. Use this when deciding whether to auto-promote
// a newly-installed profile — auto-promote should only trigger when the
// user has made no explicit choice.
func (c *Config) ExplicitDefaultProfiles() []string {
	seen := collections.NewSet[string]()
	var defaults []string
	for _, name := range c.Defaults.Profiles {
		if name != "" && !seen.Has(name) {
			seen.Add(name)
			defaults = append(defaults, name)
		}
	}
	return defaults
}

// GetDefaultProfiles returns the default profiles to load for `ctxloom run`.
// Reads the canonical defaults.profiles array. As a last-resort fallback, if
// no default is configured but exactly one profile is installed locally, that
// profile is returned — otherwise `ctxloom run` would launch with empty
// context.
func (c *Config) GetDefaultProfiles() []string {
	defaults := c.ExplicitDefaultProfiles()
	if len(defaults) > 0 {
		return defaults
	}

	// Fallback: if exactly one profile is installed, treat it as the default.
	if all, err := c.GetProfileLoader().List(); err == nil && len(all) == 1 {
		return []string{all[0].Name}
	}
	return nil
}

// GetDefaultLLM returns the default LLM name.
// Returns "claude-code" as fallback if not configured.
func (c *Config) GetDefaultLLM() string {
	return c.LM.GetDefaultLLM()
}

// GetDefaultLLMModel returns the default LLM model name.
// Returns empty string if not configured (backend will use its own default).
func (c *Config) GetDefaultLLMModel() string {
	return c.LM.Model
}

// SetDefaultLLM sets the default LLM name.
func (c *Config) SetDefaultLLM(name string) {
	c.LM.Default = name
}

// GetCompactionLLM returns the LLM to use for session compaction.
// Defaults to the default LLM if not set.
func (c *Config) GetCompactionLLM() string {
	if c.LM.Compaction.LLM != "" {
		return c.LM.Compaction.LLM
	}
	return c.LM.GetDefaultLLM()
}

// GetCompactionModel returns the model to use for session compaction.
// "haiku" is a Claude-specific model name, so it is only a safe default when the
// compaction LLM is Claude. For any other backend, return empty and let that
// backend resolve its own default model (e.g. Gemini → gemini-2.5-flash) rather
// than handing it a model name it doesn't recognize (which fails with
// ModelNotFoundError). An explicitly configured model always wins.
func (c *Config) GetCompactionModel() string {
	if c.LM.Compaction.Model != "" {
		return c.LM.Compaction.Model
	}
	if c.GetCompactionLLM() == "claude-code" {
		return "haiku"
	}
	return ""
}

// GetCompactionChunkSize returns the target chunk size for compaction.
// Defaults to 8000 tokens.
func (c *Config) GetCompactionChunkSize() int {
	if c.LM.Compaction.Chunks > 0 {
		return c.LM.Compaction.Chunks
	}
	return 8000
}

// GetProfileLoader returns a profiles.Loader for this config's ctxloom paths.
func (c *Config) GetProfileLoader() *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(c.AppPaths)
	var opts []profiles.LoaderOption
	if c.fs != nil {
		opts = append(opts, profiles.WithFS(c.fs))
	}
	return profiles.NewLoader(profileDirs, opts...)
}

// Load finds and loads configuration from a single source.
// Priority order (first found wins, no merging):
//  1. Project .ctxloom directory (walking up from cwd)
//  2. User home ~/.ctxloom directory (fallback)
func Load(opts ...LoadOption) (*Config, error) {
	// Apply options
	options := &loadOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// Use provided FS or default to OS filesystem
	fs := options.fs
	if fs == nil {
		fs = afero.NewOsFs()
	}

	cfg := &Config{
		LM: LMConfig{
			Configs: make(map[string]LLMConfig),
		},
		Profiles: make(map[string]Profile),
		fs:       fs,
	}

	// Create config validator for schema validation
	configValidator, err := schema.NewConfigValidator()
	if err != nil {
		zap.L().Warn("failed to create config validator", zap.Error(err))
		configValidator = nil
	}

	// Find or use provided .ctxloom directory
	var appPath string
	var source ConfigSource
	if options.appDir != "" {
		appPath = options.appDir
		source = SourceProject
	} else {
		appPath, source = findAppDir(fs)
	}
	cfg.AppPaths = []string{appPath}
	cfg.AppDir = appPath
	cfg.AppRoot = filepath.Dir(appPath) // Project root is parent of .ctxloom
	cfg.Source = source

	configPath := paths.ConfigPath(appPath)
	if err := loadConfigFile(cfg, configPath, configValidator, fs); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loadConfigFile loads a config file into the provided Config struct.
// Non-fatal errors (malformed YAML, schema validation) are collected as warnings.
// Returns an error only for I/O failures (except missing file, which is OK).
func loadConfigFile(cfg *Config, configPath string, validator *schema.ConfigValidator, fs afero.Fs) error {
	data, err := afero.ReadFile(fs, configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Config file is optional
			return nil
		}
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Upgrade older on-disk schema generations to the current one *in memory*
	// before validation/parse, so old configs neither warn nor silently drop
	// settings. We do NOT rewrite the file here: an interactive caller may prompt
	// the user and persist via CommitUpgrade (see cmd/run.go). This keeps
	// non-interactive contexts (MCP server, scripts) from silently rewriting a
	// user's config — the exact failure mode that motivated this layer.
	if upgraded, applied := configUpgrades.Run(data); len(applied) > 0 {
		data = upgraded
		cfg.PendingUpgrade = &upgrade.Pending{Path: configPath, Data: upgraded, Applied: applied}
		zap.L().Info("config_upgrade_pending", zap.String("path", configPath), zap.Strings("applied", applied))
	}

	// Validate against schema before parsing - warn but continue on failure
	if validator != nil {
		if err := validator.ValidateBytes(data); err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("config validation warning at %s: %v", configPath, err))
			zap.L().Warn("config_validation_warning", zap.String("path", configPath), zap.Error(err))
		}
	}

	v := viper.New()
	v.SetConfigType("yaml")

	// Use ReadConfig instead of ReadInConfig to read from the data we already have
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("failed to read config at %s: %v", configPath, err))
		zap.L().Warn("config_read_warning", zap.String("path", configPath), zap.Error(err))
		// Return nil - we have a valid (empty) config with warnings
		return nil
	}

	if err := v.Unmarshal(cfg); err != nil {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("failed to parse config at %s: %v", configPath, err))
		zap.L().Warn("config_parse_warning", zap.String("path", configPath), zap.Error(err))
		// Return nil - we have a valid (partially loaded) config with warnings
		return nil
	}

	zap.L().Debug("config_loaded", zap.String("path", configPath))
	return nil
}

// findAppDir locates the .ctxloom directory.
// Priority:
//  1. Walk up from cwd looking for .ctxloom directory
//  2. Fall back to user home ~/.ctxloom directory
//
// Always returns a path (creates user home .ctxloom if needed).
func findAppDir(fs afero.Fs) (string, ConfigSource) {
	// Try to find project .ctxloom by walking up from cwd
	pwd, err := os.Getwd()
	if err == nil {
		// Walk up the directory tree looking for .ctxloom
		dir := pwd
		for {
			appPath := filepath.Join(dir, AppDirName)
			if info, err := fs.Stat(appPath); err == nil && info.IsDir() {
				return appPath, SourceProject
			}

			parent := filepath.Dir(dir)
			if parent == dir {
				// Reached root
				break
			}
			dir = parent
		}
	}

	// Fall back to user home ~/.ctxloom
	home, err := os.UserHomeDir()
	if err != nil {
		zap.L().Warn("failed to get home directory", zap.Error(err))
		// Last resort: use cwd
		if pwd != "" {
			return filepath.Join(pwd, AppDirName), SourceProject
		}
		return AppDirName, SourceProject
	}

	homeApp := filepath.Join(home, AppDirName)

	// Ensure the directory exists
	if err := fs.MkdirAll(homeApp, 0755); err != nil {
		zap.L().Warn("failed to create home .ctxloom directory", zap.Error(err))
	}

	return homeApp, SourceHome
}

// GetBundleDirs returns bundles directories (in cache/).
func (c *Config) GetBundleDirs() []string {
	var dirs []string
	for _, appPath := range c.AppPaths {
		bundleDir := paths.BundlesPath(appPath)
		if info, err := os.Stat(bundleDir); err == nil && info.IsDir() {
			dirs = append(dirs, bundleDir)
		}
	}
	return dirs
}

// SeededBundleLoader returns a bundles.Loader that sees fs-installed local
// bundles plus every remote bundle in the active lockfile, pre-loaded from
// the local git clone cache and SHA-pinned. This is the read-path loader
// every caller should use after PR 1: remote bundles no longer live on disk
// as extracted YAML, so the seeding step is what makes them visible at all.
//
// Failures are degraded gracefully (CLAUDE.md fault tolerance): a missing
// lockfile, unregistered remote, or single bad SHA produces a stderr
// warning and the loader returns the rest.
func (c *Config) SeededBundleLoader(preferDistilled bool, opts ...bundles.LoaderOption) *bundles.Loader {
	if seed := c.loadRemoteBundleSeed(); len(seed) > 0 {
		opts = append(append([]bundles.LoaderOption(nil), opts...), bundles.WithSeededBundles(seed))
	}
	return bundles.NewLoader(c.GetBundleDirs(), preferDistilled, opts...)
}

// loadRemoteBundleSeed materializes every lockfile-listed bundle from the
// local git clone cache, parsed and ready to seed a bundles.Loader. Returns
// nil when there is no lockfile or registry — caller treats nil as "no
// remote bundles, just walk fs."
func (c *Config) loadRemoteBundleSeed() map[string]*bundles.Bundle {
	if len(c.AppPaths) == 0 {
		return nil
	}
	baseDir := c.AppPaths[0]

	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir))
	if err != nil {
		return nil
	}
	lock, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		return nil
	}
	if lock.IsEmpty() {
		return nil
	}
	auth := remote.LoadAuth(baseDir)
	cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
	factory := remote.NewCachedFetcherFactory(cache)
	// Wrap in the caching decorator so repeated SeededBundleLoader calls
	// within a session don't re-walk the clone for the same SHAs.
	reader := remote.NewCachingBundleReader(remote.NewBundleReader(registry, factory, auth, lock))

	rawBytes, failures := remote.LoadAllBytes(context.Background(), reader)
	for name, err := range failures {
		warnOncePerRun(fmt.Sprintf("ctxloom: warning: failed to load remote bundle %q from cache: %v\n", name, err))
	}

	loaded := make(map[string]*bundles.Bundle, len(rawBytes))
	for name, data := range rawBytes {
		b, perr := bundles.ParseBundle(data)
		if perr != nil {
			warnOncePerRun(fmt.Sprintf("ctxloom: warning: failed to parse remote bundle %q: %v\n", name, perr))
			continue
		}
		b.Name = name
		if entry, ok := lock.Bundles[name]; ok {
			b.Path = fmt.Sprintf("<remote>:%s@%s", name, entry.SHA)
		}
		loaded[name] = b
	}
	return loaded
}

var (
	warnedOnceMu   sync.Mutex
	warnedOnceSeen = map[string]struct{}{}
)

// warnOncePerRun writes msg to stderr at most once per process for identical
// text, collapsing the duplicate remote-bundle warnings emitted when profile
// resolution re-seeds the same bundles several times during one startup.
func warnOncePerRun(msg string) {
	warnedOnceMu.Lock()
	defer warnedOnceMu.Unlock()
	if _, seen := warnedOnceSeen[msg]; seen {
		return
	}
	warnedOnceSeen[msg] = struct{}{}
	fmt.Fprint(os.Stderr, msg)
}

// SourceName returns a human-readable name for the config source.
func (c *Config) SourceName() string {
	switch c.Source {
	case SourceProject:
		return "project"
	case SourceHome:
		return "home"
	default:
		return "unknown"
	}
}

// GetConfigFilePath returns the path to the primary config file.
// Uses the closest project .ctxloom directory.
func (c *Config) GetConfigFilePath() (string, error) {
	if len(c.AppPaths) == 0 {
		return "", fmt.Errorf("no .ctxloom directory found; run 'ctxloom init --local' first")
	}
	return paths.ConfigPath(c.AppPaths[0]), nil
}

// getFS returns the filesystem to use for file operations.
func (c *Config) getFS() afero.Fs {
	if c.fs != nil {
		return c.fs
	}
	return afero.NewOsFs()
}

// SetFS sets the filesystem for file operations (useful for testing).
func (c *Config) SetFS(fs afero.Fs) {
	c.fs = fs
}
