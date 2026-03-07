package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/collections"
	"github.com/benjaminabbitt/scm/internal/profiles"
	"github.com/benjaminabbitt/scm/internal/remote"
	"github.com/benjaminabbitt/scm/internal/schema"
)

const (
	SCMDirName     = ".scm"
	ConfigFileName = "config"
	BundlesDir     = "bundles"
)

// ConfigSource indicates where the configuration was loaded from.
type ConfigSource int

const (
	// SourceProject means config was loaded from a project .scm directory.
	SourceProject ConfigSource = iota
	// SourceHome means config was loaded from user home ~/.scm directory.
	SourceHome
)

// Config holds the SCM configuration.
type Config struct {
	LM       LMConfig           `mapstructure:"llm" yaml:"llm"`
	Editor   EditorConfig       `mapstructure:"editor"`
	Defaults Defaults           `mapstructure:"defaults"`
	Sync     SyncConfig         `mapstructure:"sync"`
	Hooks    HooksConfig        `mapstructure:"hooks"`
	MCP      MCPConfig          `mapstructure:"mcp"`
	Profiles map[string]Profile `mapstructure:"profiles"`
	SCMPaths []string           // Resolved .scm directory (at most one)
	SCMRoot  string             // Project root (parent of .scm directory)
	SCMDir   string             // Full path to the .scm directory
	Source   ConfigSource       // Where the configuration was loaded from
	Warnings []string           // Non-fatal warnings collected during load
	fs       afero.Fs           // Filesystem for file operations (nil = OS filesystem)
}

// LoadOption is a functional option for Load.
type LoadOption func(*loadOptions)

type loadOptions struct {
	fs     afero.Fs
	scmDir string // Override SCM directory discovery
}

// WithFS sets the filesystem for config operations.
func WithFS(fs afero.Fs) LoadOption {
	return func(o *loadOptions) {
		o.fs = fs
	}
}

// WithSCMDir sets a specific SCM directory instead of discovering it.
func WithSCMDir(dir string) LoadOption {
	return func(o *loadOptions) {
		o.scmDir = dir
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

// GetDefaultProfiles returns the default profiles from config.
// Checks defaults.profiles array and profiles with default: true.
func (c *Config) GetDefaultProfiles() []string {
	seen := collections.NewSet[string]()
	var defaults []string

	// Helper to add without duplicates
	addProfile := func(name string) {
		if name != "" && !seen.Has(name) {
			seen.Add(name)
			defaults = append(defaults, name)
		}
	}

	// Check defaults.profiles array
	for _, name := range c.Defaults.Profiles {
		addProfile(name)
	}

	// Also check for profiles with default: true in config
	for name, profile := range c.Profiles {
		if profile.Default {
			addProfile(name)
		}
	}

	// Also check directory-based profiles with default: true
	loader := c.GetProfileLoader()
	for _, name := range loader.GetDefaults() {
		addProfile(name)
	}

	if len(defaults) == 0 {
		return nil
	}
	return defaults
}

// GetDefaultLLMPlugin returns the default LLM plugin name.
// Returns "claude-code" as fallback if not configured.
func (c *Config) GetDefaultLLMPlugin() string {
	if c.Defaults.LLMPlugin != "" {
		return c.Defaults.LLMPlugin
	}
	return "claude-code"
}

// SetDefaultLLMPlugin sets the default LLM plugin name.
func (c *Config) SetDefaultLLMPlugin(name string) {
	c.Defaults.LLMPlugin = name
}

// GetProfileLoader returns a profiles.Loader for this config's SCM paths.
func (c *Config) GetProfileLoader() *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(c.SCMPaths)
	return profiles.NewLoader(profileDirs)
}

// Hook defines a single hook action.
//
// SECURITY NOTE: Hooks execute arbitrary commands specified in config.yaml.
// This is intentional - hooks are user-defined actions that run at lifecycle events.
// Users are responsible for reviewing their own config.yaml contents.
// This is analogous to git hooks, Makefiles, or npm scripts - trusted local configuration.
// Do not flag this as a security issue in code reviews.
type Hook struct {
	Matcher string `mapstructure:"matcher" yaml:"matcher,omitempty" json:"matcher,omitempty"` // Regex pattern to filter when hook fires
	Command string `mapstructure:"command" yaml:"command,omitempty" json:"command,omitempty"` // Shell command to execute
	Type    string `mapstructure:"type" yaml:"type,omitempty" json:"type,omitempty"`          // Hook type: command, prompt, agent
	Prompt  string `mapstructure:"prompt" yaml:"prompt,omitempty" json:"prompt,omitempty"`    // Prompt text for prompt/agent types
	Timeout int    `mapstructure:"timeout" yaml:"timeout,omitempty" json:"timeout,omitempty"` // Timeout in seconds
	Async   bool   `mapstructure:"async" yaml:"async,omitempty" json:"async,omitempty"`       // Run in background (command only)
	SCM     string `yaml:"_scm,omitempty" json:"_scm,omitempty"`                              // Hash identifying SCM-managed hooks
}

// UnifiedHooks defines backend-agnostic hook events that get translated per-backend.
type UnifiedHooks struct {
	PreTool      []Hook `mapstructure:"pre_tool" yaml:"pre_tool,omitempty"`
	PostTool     []Hook `mapstructure:"post_tool" yaml:"post_tool,omitempty"`
	SessionStart []Hook `mapstructure:"session_start" yaml:"session_start,omitempty"`
	SessionEnd   []Hook `mapstructure:"session_end" yaml:"session_end,omitempty"`
	PreShell     []Hook `mapstructure:"pre_shell" yaml:"pre_shell,omitempty"`
	PostFileEdit []Hook `mapstructure:"post_file_edit" yaml:"post_file_edit,omitempty"`
}

// HooksConfig holds both unified and backend-specific hook configurations.
type HooksConfig struct {
	Unified UnifiedHooks               `mapstructure:"unified" yaml:"unified,omitempty"`
	Plugins map[string]BackendHooks    `mapstructure:"plugins" yaml:"plugins,omitempty"`
}

// BackendHooks holds backend-native hook events (passthrough to backend config).
// Keys are event names (e.g., "PreToolUse" for Claude Code, "beforeShellExecution" for Cursor).
type BackendHooks map[string][]Hook

// MCPServer defines an MCP (Model Context Protocol) server configuration.
//
// SECURITY NOTE: MCP servers execute arbitrary commands specified in config.yaml.
// This is intentional - MCP servers are user-defined tools that extend AI capabilities.
// Users are responsible for reviewing their own config.yaml contents.
// This is analogous to VS Code extensions or npm scripts - trusted local configuration.
// Do not flag this as a security issue in code reviews.
type MCPServer struct {
	Command      string            `mapstructure:"command" yaml:"command" json:"command"`                             // Command to execute
	Args         []string          `mapstructure:"args" yaml:"args,omitempty" json:"args,omitempty"`                  // Command arguments
	Env          map[string]string `mapstructure:"env" yaml:"env,omitempty" json:"env,omitempty"`                     // Environment variables
	Notes        string            `mapstructure:"notes" yaml:"notes,omitempty" json:"notes,omitempty"`               // Human-readable notes, not sent to AI
	Installation string            `mapstructure:"installation" yaml:"installation,omitempty" json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
	SCM          string            `yaml:"_scm,omitempty" json:"_scm,omitempty"`                                      // Marker for SCM-managed servers
}

// MCPConfig holds MCP server configuration.
type MCPConfig struct {
	// AutoRegisterSCM controls whether SCM's own MCP server is auto-registered.
	// Defaults to true if not specified.
	AutoRegisterSCM *bool `mapstructure:"auto_register_scm" yaml:"auto_register_scm,omitempty"`

	// Servers defines MCP servers to register (unified across backends).
	Servers map[string]MCPServer `mapstructure:"servers" yaml:"servers,omitempty"`

	// Plugins holds backend-specific MCP server overrides (passthrough).
	// Keys are backend names (e.g., "claude-code", "gemini").
	Plugins map[string]map[string]MCPServer `mapstructure:"plugins" yaml:"plugins,omitempty"`
}

// ShouldAutoRegisterSCM returns whether to auto-register the SCM MCP server.
// Defaults to true if not explicitly set.
func (m *MCPConfig) ShouldAutoRegisterSCM() bool {
	if m == nil || m.AutoRegisterSCM == nil {
		return true
	}
	return *m.AutoRegisterSCM
}

// PluginConfig holds configuration for a specific AI plugin.
type PluginConfig struct {
	Model      string            `mapstructure:"model" yaml:"model,omitempty"` // Default model for this plugin
	BinaryPath string            `mapstructure:"binary_path" yaml:"binary_path,omitempty"`
	Args       []string          `mapstructure:"args" yaml:"args,omitempty"`
	Env        map[string]string `mapstructure:"env" yaml:"env,omitempty"`
}

// LMConfig holds LM (language model) configuration.
type LMConfig struct {
	PluginPaths []string                `mapstructure:"plugin_paths" yaml:"plugin_paths,omitempty"`
	Plugins     map[string]PluginConfig `mapstructure:"plugins" yaml:"plugins"`
}

// GetDefaultPlugin returns the name of the default plugin.
// Returns "claude-code" as fallback if not configured.
func (c *LMConfig) GetDefaultPlugin() string {
	return "claude-code"
}

// GetConfiguredPlugins returns the list of configured plugin names.
// If no plugins are configured, returns the default plugin.
func (c *LMConfig) GetConfiguredPlugins() []string {
	if len(c.Plugins) == 0 {
		return []string{c.GetDefaultPlugin()}
	}
	var names []string
	for name := range c.Plugins {
		names = append(names, name)
	}
	return names
}

// SetDefaultPlugin is deprecated - use Config.Defaults.LLMPlugin instead.
func (c *LMConfig) SetDefaultPlugin(name string) {
	// No-op - default is now set via Defaults.LLMPlugin
}

// GetDefaultModel returns the default model for the specified plugin.
// Returns empty string if no default is configured.
func (c *LMConfig) GetDefaultModel(pluginName string) string {
	if cfg, ok := c.Plugins[pluginName]; ok {
		return cfg.Model
	}
	return ""
}

// Profile is a named collection of context fragments and variables.
// Fragments can be specified directly by path, or dynamically via tags.
// Profiles can inherit from parent profiles using the Parents field.
type Profile struct {
	Default     bool              `mapstructure:"default" yaml:"default,omitempty"`           // Whether this is a default profile
	Description string            `mapstructure:"description" yaml:"description,omitempty"`
	Parents     []string          `mapstructure:"parents" yaml:"parents,omitempty"`           // Parent profiles to inherit from
	Tags        []string          `mapstructure:"tags" yaml:"tags,omitempty"`                 // Fragment tags to include
	Bundles     []string          `mapstructure:"bundles" yaml:"bundles,omitempty"`           // Bundle references (e.g., "remote/go-tools")
	BundleItems []string          `mapstructure:"bundle_items" yaml:"bundle_items,omitempty"` // Cherry-pick items (e.g., "remote/bundle:fragments/name")
	Fragments   []string          `mapstructure:"fragments" yaml:"fragments,omitempty"`       // Explicit fragment paths (local/legacy)
	Variables   map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Hooks       HooksConfig       `mapstructure:"hooks" yaml:"hooks,omitempty"`               // Hooks for this profile (inherited)
	MCP         MCPConfig         `mapstructure:"mcp" yaml:"mcp,omitempty"`                   // MCP servers for this profile (inherited)
	MCPServers  []string          `mapstructure:"mcp_servers" yaml:"mcp_servers,omitempty"`   // Remote MCP server references (legacy)
}

// Defaults holds default settings applied when no explicit values are specified.
type Defaults struct {
	Profiles     []string `mapstructure:"profiles" yaml:"profiles,omitempty"`           // Default profiles to load (supports multiple)
	LLMPlugin    string   `mapstructure:"llm_plugin" yaml:"llm_plugin,omitempty"`       // Default LLM plugin name
	UseDistilled *bool    `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"` // Prefer .distilled.md versions (default true)
}

// SyncConfig holds configuration for dependency sync behavior.
type SyncConfig struct {
	// AutoSync enables automatic sync of remote dependencies on startup.
	// Defaults to true if not specified.
	AutoSync *bool `mapstructure:"auto_sync" yaml:"auto_sync,omitempty"`

	// Lock controls whether to update lockfile after sync.
	// Defaults to true if not specified.
	Lock *bool `mapstructure:"lock" yaml:"lock,omitempty"`

	// ApplyHooks controls whether to apply hooks after sync.
	// Defaults to true if not specified.
	ApplyHooks *bool `mapstructure:"apply_hooks" yaml:"apply_hooks,omitempty"`
}

// ShouldAutoSync returns whether to auto-sync dependencies on startup.
// Defaults to true if not explicitly set.
func (s *SyncConfig) ShouldAutoSync() bool {
	if s == nil || s.AutoSync == nil {
		return true
	}
	return *s.AutoSync
}

// ShouldLock returns whether to update lockfile after sync.
// Defaults to true if not explicitly set.
func (s *SyncConfig) ShouldLock() bool {
	if s == nil || s.Lock == nil {
		return true
	}
	return *s.Lock
}

// ShouldApplyHooks returns whether to apply hooks after sync.
// Defaults to true if not explicitly set.
func (s *SyncConfig) ShouldApplyHooks() bool {
	if s == nil || s.ApplyHooks == nil {
		return true
	}
	return *s.ApplyHooks
}

// ShouldUseDistilled returns whether to prefer distilled versions of fragments/prompts.
// Defaults to true if not explicitly set.
func (d *Defaults) ShouldUseDistilled() bool {
	if d.UseDistilled == nil {
		return true
	}
	return *d.UseDistilled
}

// AddDefaultProfile adds a profile to the defaults list if not already present.
func (d *Defaults) AddDefaultProfile(name string) bool {
	if d.IsDefaultProfile(name) {
		return false
	}
	d.Profiles = append(d.Profiles, name)
	return true
}

// RemoveDefaultProfile removes a profile from the defaults list.
// Returns true if the profile was removed, false if it wasn't present.
func (d *Defaults) RemoveDefaultProfile(name string) bool {
	for i, p := range d.Profiles {
		if p == name {
			d.Profiles = append(d.Profiles[:i], d.Profiles[i+1:]...)
			return true
		}
	}
	return false
}

// IsDefaultProfile checks if a profile is in the defaults list.
func (d *Defaults) IsDefaultProfile(name string) bool {
	for _, p := range d.Profiles {
		if p == name {
			return true
		}
	}
	return false
}

// Load finds and loads configuration from a single source.
// Priority order (first found wins, no merging):
//  1. Project .scm directory (walking up from cwd)
//  2. User home ~/.scm directory (fallback)
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
			Plugins: make(map[string]PluginConfig),
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

	// Find or use provided .scm directory
	var scmPath string
	var source ConfigSource
	if options.scmDir != "" {
		scmPath = options.scmDir
		source = SourceProject
	} else {
		scmPath, source = findSCMDir(fs)
	}
	cfg.SCMPaths = []string{scmPath}
	cfg.SCMDir = scmPath
	cfg.SCMRoot = filepath.Dir(scmPath) // Project root is parent of .scm
	cfg.Source = source

	configPath := filepath.Join(scmPath, ConfigFileName+".yaml")
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

// findSCMDir locates the .scm directory.
// Priority:
//  1. Walk up from cwd looking for .scm directory
//  2. Fall back to user home ~/.scm directory
// Always returns a path (creates user home .scm if needed).
func findSCMDir(fs afero.Fs) (string, ConfigSource) {
	// Try to find project .scm by walking up from cwd
	pwd, err := os.Getwd()
	if err == nil {
		// Walk up the directory tree looking for .scm
		dir := pwd
		for {
			scmPath := filepath.Join(dir, SCMDirName)
			if info, err := fs.Stat(scmPath); err == nil && info.IsDir() {
				return scmPath, SourceProject
			}

			parent := filepath.Dir(dir)
			if parent == dir {
				// Reached root
				break
			}
			dir = parent
		}
	}

	// Fall back to user home ~/.scm
	home, err := os.UserHomeDir()
	if err != nil {
		zap.L().Warn("failed to get home directory", zap.Error(err))
		// Last resort: use cwd
		if pwd != "" {
			return filepath.Join(pwd, SCMDirName), SourceProject
		}
		return SCMDirName, SourceProject
	}

	homeSCM := filepath.Join(home, SCMDirName)

	// Ensure the directory exists
	if err := fs.MkdirAll(homeSCM, 0755); err != nil {
		zap.L().Warn("failed to create home .scm directory", zap.Error(err))
	}

	return homeSCM, SourceHome
}

// GetBundleDirs returns bundles directories.
func (c *Config) GetBundleDirs() []string {
	var dirs []string
	for _, scmPath := range c.SCMPaths {
		bundleDir := filepath.Join(scmPath, BundlesDir)
		if info, err := os.Stat(bundleDir); err == nil && info.IsDir() {
			dirs = append(dirs, bundleDir)
		}
	}
	return dirs
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

// GetPluginPaths returns the paths where external plugins are searched for.
// Defaults to .scm/plugins if not configured.
func (c *Config) GetPluginPaths() []string {
	if len(c.LM.PluginPaths) > 0 {
		return c.LM.PluginPaths
	}
	// Default plugin paths from project .scm
	var paths []string
	for _, scmPath := range c.SCMPaths {
		paths = append(paths, filepath.Join(scmPath, "plugins"))
	}
	return paths
}

// ConfigFile represents the structure for saving config.yaml
type ConfigFile struct {
	LM       LMConfig           `yaml:"llm"`
	Editor   EditorConfig       `yaml:"editor,omitempty"`
	Defaults Defaults           `yaml:"defaults,omitempty"`
	Sync     SyncConfig         `yaml:"sync,omitempty"`
	Hooks    HooksConfig        `yaml:"hooks,omitempty"`
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
}

// GetConfigFilePath returns the path to the primary config file.
// Uses the closest project .scm directory.
func (c *Config) GetConfigFilePath() (string, error) {
	if len(c.SCMPaths) == 0 {
		return "", fmt.Errorf("no .scm directory found; run 'scm init --local' first")
	}
	return filepath.Join(c.SCMPaths[0], ConfigFileName+".yaml"), nil
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

// Save writes the configuration to the primary config file.
func (c *Config) Save() error {
	configPath, err := c.GetConfigFilePath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	fs := c.getFS()

	// Read existing config to preserve unknown fields
	existingData, _ := afero.ReadFile(fs, configPath)
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		_ = yaml.Unmarshal(existingData, &existing)
	}

	// Update with current values (delete keys when empty to clean up config)
	existing["llm"] = c.LM
	delete(existing, "lm") // Remove old key if present

	if len(c.Defaults.Profiles) > 0 || c.Defaults.LLMPlugin != "" || c.Defaults.UseDistilled != nil {
		existing["defaults"] = c.Defaults
	} else {
		delete(existing, "defaults")
	}

	if len(c.Profiles) > 0 {
		existing["profiles"] = c.Profiles
	} else {
		delete(existing, "profiles")
	}

	// Save sync config if any values are set
	if c.Sync.AutoSync != nil || c.Sync.Lock != nil || c.Sync.ApplyHooks != nil {
		existing["sync"] = c.Sync
	} else {
		delete(existing, "sync")
	}

	// Remove generators key if present (no longer supported)
	delete(existing, "generators")

	if len(c.MCP.Servers) > 0 || len(c.MCP.Plugins) > 0 || c.MCP.AutoRegisterSCM != nil {
		existing["mcp"] = c.MCP
	} else {
		delete(existing, "mcp")
	}

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := afero.WriteFile(fs, configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// LoadFromDir loads config from a specific .scm directory.
// Returns a valid config with warnings on parse errors for resilient startup.
func LoadFromDir(scmDir string, opts ...LoadOption) (*Config, error) {
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
		Profiles: make(map[string]Profile),
		fs:       fs,
	}

	configPath := filepath.Join(scmDir, ConfigFileName+".yaml")
	data, err := afero.ReadFile(fs, configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty config if no config file exists
			cfg.SCMPaths = []string{scmDir}
			cfg.SCMDir = scmDir
			cfg.SCMRoot = filepath.Dir(scmDir)
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config from %s: %w", configPath, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		// Add warning but continue with empty config for resilient startup
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("failed to parse config from %s: %v", configPath, err))
		zap.L().Warn("config_parse_warning", zap.String("path", configPath), zap.Error(err))
	}

	cfg.SCMPaths = []string{scmDir}
	cfg.SCMDir = scmDir
	cfg.SCMRoot = filepath.Dir(scmDir)
	return cfg, nil
}

// MergeProfiles merges profiles from source into target.
// Source profiles override target profiles with the same name.
func MergeProfiles(target, source map[string]Profile) {
	for name, profile := range source {
		target[name] = profile
	}
}

// CollectFragmentsForProfiles returns a deduplicated list of all fragments
// referenced by the specified profiles.
func CollectFragmentsForProfiles(profiles map[string]Profile, profileNames []string) ([]string, error) {
	seen := collections.NewSet[string]()
	var fragments []string

	for _, name := range profileNames {
		profile, ok := profiles[name]
		if !ok {
			return nil, fmt.Errorf("unknown profile: %s", name)
		}
		for _, frag := range profile.Fragments {
			if !seen.Has(frag) {
				seen.Add(frag)
				fragments = append(fragments, frag)
			}
		}
	}

	return fragments, nil
}

// CollectBundlesForProfiles returns a deduplicated list of all bundles
// referenced by the specified profiles.
func CollectBundlesForProfiles(profiles map[string]Profile, profileNames []string) ([]string, error) {
	seen := collections.NewSet[string]()
	var bundles []string

	for _, name := range profileNames {
		profile, ok := profiles[name]
		if !ok {
			return nil, fmt.Errorf("unknown profile: %s", name)
		}
		for _, bundle := range profile.Bundles {
			if !seen.Has(bundle) {
				seen.Add(bundle)
				bundles = append(bundles, bundle)
			}
		}
	}

	return bundles, nil
}

// CollectBundleItemsForProfiles returns a deduplicated list of all cherry-picked
// bundle items referenced by the specified profiles.
func CollectBundleItemsForProfiles(profiles map[string]Profile, profileNames []string) ([]string, error) {
	seen := collections.NewSet[string]()
	var items []string

	for _, name := range profileNames {
		profile, ok := profiles[name]
		if !ok {
			return nil, fmt.Errorf("unknown profile: %s", name)
		}
		for _, item := range profile.BundleItems {
			if !seen.Has(item) {
				seen.Add(item)
				items = append(items, item)
			}
		}
	}

	return items, nil
}

// FilterProfiles returns only the specified profiles from the full map.
func FilterProfiles(all map[string]Profile, names []string) map[string]Profile {
	filtered := make(map[string]Profile)
	for _, name := range names {
		if profile, ok := all[name]; ok {
			filtered[name] = profile
		}
	}
	return filtered
}

// profileBuilder collects profile fields using sets to avoid duplicates during inheritance.
type profileBuilder struct {
	Description string
	Tags        collections.Set[string]
	Bundles     collections.Set[string]
	BundleItems collections.Set[string]
	Fragments   collections.Set[string]
	Variables   map[string]string
	Hooks       HooksConfig
	MCP         MCPConfig
	// Track insertion order for stable output
	tagsOrder        []string
	bundlesOrder     []string
	bundleItemsOrder []string
	fragmentsOrder   []string
	// Track seen hooks by key (command+matcher) for deduplication
	seenHooks collections.Set[string]
}

func newProfileBuilder() *profileBuilder {
	return &profileBuilder{
		Tags:        collections.NewSet[string](),
		Bundles:     collections.NewSet[string](),
		BundleItems: collections.NewSet[string](),
		Fragments:   collections.NewSet[string](),
		Variables:   make(map[string]string),
		Hooks: HooksConfig{
			Plugins: make(map[string]BackendHooks),
		},
		MCP: MCPConfig{
			Servers: make(map[string]MCPServer),
			Plugins: make(map[string]map[string]MCPServer),
		},
		seenHooks: collections.NewSet[string](),
	}
}

func (b *profileBuilder) addTag(tag string) {
	if !b.Tags.Has(tag) {
		b.Tags.Add(tag)
		b.tagsOrder = append(b.tagsOrder, tag)
	}
}

func (b *profileBuilder) addBundle(bundle string) {
	if !b.Bundles.Has(bundle) {
		b.Bundles.Add(bundle)
		b.bundlesOrder = append(b.bundlesOrder, bundle)
	}
}

func (b *profileBuilder) addBundleItem(item string) {
	if !b.BundleItems.Has(item) {
		b.BundleItems.Add(item)
		b.bundleItemsOrder = append(b.bundleItemsOrder, item)
	}
}

func (b *profileBuilder) addFragment(frag string) {
	if !b.Fragments.Has(frag) {
		b.Fragments.Add(frag)
		b.fragmentsOrder = append(b.fragmentsOrder, frag)
	}
}

// hookKey returns a unique key for deduplication based on command and matcher.
func hookKey(h Hook) string {
	return h.Command + "|" + h.Matcher
}

// addHook adds a hook if not already present (by command+matcher key).
func (b *profileBuilder) addHook(hooks *[]Hook, h Hook) {
	key := hookKey(h)
	if !b.seenHooks.Has(key) {
		b.seenHooks.Add(key)
		*hooks = append(*hooks, h)
	}
}

// mergeMCP merges MCP config from source into the builder.
// Later sources override earlier ones for the same server name.
func (b *profileBuilder) mergeMCP(source MCPConfig) {
	// Merge auto_register_scm (later wins)
	if source.AutoRegisterSCM != nil {
		b.MCP.AutoRegisterSCM = source.AutoRegisterSCM
	}

	// Merge unified servers
	for name, server := range source.Servers {
		b.MCP.Servers[name] = server
	}

	// Merge plugin-specific servers
	for backend, servers := range source.Plugins {
		if b.MCP.Plugins[backend] == nil {
			b.MCP.Plugins[backend] = make(map[string]MCPServer)
		}
		for name, server := range servers {
			b.MCP.Plugins[backend][name] = server
		}
	}
}

// mergeHooks merges hooks from source into the builder.
func (b *profileBuilder) mergeHooks(source HooksConfig) {
	// Merge unified hooks
	for _, h := range source.Unified.PreTool {
		b.addHook(&b.Hooks.Unified.PreTool, h)
	}
	for _, h := range source.Unified.PostTool {
		b.addHook(&b.Hooks.Unified.PostTool, h)
	}
	for _, h := range source.Unified.SessionStart {
		b.addHook(&b.Hooks.Unified.SessionStart, h)
	}
	for _, h := range source.Unified.SessionEnd {
		b.addHook(&b.Hooks.Unified.SessionEnd, h)
	}
	for _, h := range source.Unified.PreShell {
		b.addHook(&b.Hooks.Unified.PreShell, h)
	}
	for _, h := range source.Unified.PostFileEdit {
		b.addHook(&b.Hooks.Unified.PostFileEdit, h)
	}

	// Merge plugin-specific hooks
	for pluginName, backendHooks := range source.Plugins {
		if b.Hooks.Plugins[pluginName] == nil {
			b.Hooks.Plugins[pluginName] = make(BackendHooks)
		}
		for eventName, hooks := range backendHooks {
			for _, h := range hooks {
				key := pluginName + ":" + eventName + ":" + hookKey(h)
				if !b.seenHooks.Has(key) {
					b.seenHooks.Add(key)
					b.Hooks.Plugins[pluginName][eventName] = append(b.Hooks.Plugins[pluginName][eventName], h)
				}
			}
		}
	}
}

func (b *profileBuilder) toProfile() *Profile {
	return &Profile{
		Description: b.Description,
		Tags:        b.tagsOrder,
		Bundles:     b.bundlesOrder,
		BundleItems: b.bundleItemsOrder,
		Fragments:   b.fragmentsOrder,
		Variables:   b.Variables,
		Hooks:       b.Hooks,
		MCP:         b.MCP,
	}
}

// maxProfileDepth is the maximum allowed depth for profile inheritance.
// This prevents stack overflow from deeply nested or malformed configurations.
// The value 64 is arbitrary but well beyond any reasonable inheritance chain.
const maxProfileDepth = 64

// ResolveProfile resolves a profile by recursively merging all parent profiles.
// Parents are processed depth-first, with later parents and the child overriding earlier values.
// Uses sets internally to handle diamond inheritance (shared ancestors) without duplicates.
// Returns an error if the profile doesn't exist or if circular dependencies are detected.
func ResolveProfile(profiles map[string]Profile, name string) (*Profile, error) {
	visited := collections.NewSet[string]()
	builder := newProfileBuilder()
	if err := resolveProfileRecursive(profiles, name, visited, builder, 0); err != nil {
		return nil, err
	}
	return builder.toProfile(), nil
}

func resolveProfileRecursive(profiles map[string]Profile, name string, visited collections.Set[string], builder *profileBuilder, depth int) error {
	// Check depth limit
	if depth > maxProfileDepth {
		return fmt.Errorf("profile inheritance depth exceeds maximum (%d): possible misconfiguration", maxProfileDepth)
	}

	// Check for circular dependency
	if visited.Has(name) {
		return fmt.Errorf("circular profile inheritance detected: %s", name)
	}
	visited.Add(name)

	profile, ok := profiles[name]
	if !ok {
		return fmt.Errorf("unknown profile: %s", name)
	}

	// Resolve parents first (depth-first)
	for _, parentName := range profile.Parents {
		if err := resolveProfileRecursive(profiles, parentName, visited.Clone(), builder, depth+1); err != nil {
			return fmt.Errorf("failed to resolve parent %s: %w", parentName, err)
		}
	}

	// Merge this profile's values (child overrides parents for variables)
	for _, tag := range profile.Tags {
		builder.addTag(tag)
	}
	for _, bundle := range profile.Bundles {
		builder.addBundle(bundle)
	}
	for _, item := range profile.BundleItems {
		builder.addBundleItem(item)
	}
	for _, frag := range profile.Fragments {
		builder.addFragment(frag)
	}
	for k, v := range profile.Variables {
		builder.Variables[k] = v
	}

	// Merge hooks (deduplicated by command+matcher)
	builder.mergeHooks(profile.Hooks)

	// Merge MCP config (later wins for same server names)
	builder.mergeMCP(profile.MCP)

	// Set description from the leaf profile (will be overwritten by each child)
	builder.Description = profile.Description

	return nil
}

// DedupeStrings removes duplicates from a string slice while preserving order.
func DedupeStrings(items []string) []string {
	seen := collections.NewSet[string]()
	result := make([]string, 0, len(items))
	for _, item := range items {
		if !seen.Has(item) {
			seen.Add(item)
			result = append(result, item)
		}
	}
	return result
}

// ResolveBundleMCPServers loads MCP servers from bundles referenced in the default profiles.
// It returns a map of server name to MCPServer configuration.
func (c *Config) ResolveBundleMCPServers() map[string]MCPServer {
	result := make(map[string]MCPServer)

	// Get the default profile names
	defaultProfiles := c.GetDefaultProfiles()
	if len(defaultProfiles) == 0 {
		return result
	}

	// Get the base .scm directory
	if len(c.SCMPaths) == 0 {
		return result
	}
	scmDir := c.SCMPaths[0]

	// Load each default profile and collect MCP servers
	profileLoader := c.GetProfileLoader()
	bundleDirs := []string{filepath.Join(scmDir, BundlesDir)}
	bundleLoader := bundles.NewLoader(bundleDirs, false)

	for _, defaultProfile := range defaultProfiles {
		profile, err := profileLoader.Load(defaultProfile)
		if err != nil {
			continue
		}

		// Process each bundle URL in the profile
		for _, bundleRef := range profile.Bundles {
			servers := loadMCPFromBundleRef(bundleRef, scmDir, bundleLoader)
			for name, server := range servers {
				result[name] = server
			}
		}
	}

	return result
}

// loadMCPFromBundleRef loads MCP servers from a bundle reference (URL or name).
func loadMCPFromBundleRef(bundleRef string, scmDir string, loader *bundles.Loader) map[string]MCPServer {
	result := make(map[string]MCPServer)

	// Parse the reference to get the local path
	ref, err := remote.ParseReference(bundleRef)
	if err != nil {
		// Try as a local bundle name
		bundle, err := loader.Load(bundleRef)
		if err != nil {
			return result
		}
		return extractMCPFromBundle(bundle, bundleRef)
	}

	// Get the local path for this bundle
	localPath := ref.LocalPath(scmDir, remote.ItemTypeBundle)

	// Load the bundle from the local path
	bundle, err := loader.LoadFile(localPath)
	if err != nil {
		return result
	}

	return extractMCPFromBundle(bundle, bundleRef)
}

// extractMCPFromBundle extracts MCP servers from a loaded bundle.
func extractMCPFromBundle(bundle *bundles.Bundle, source string) map[string]MCPServer {
	result := make(map[string]MCPServer)

	for name, mcp := range bundle.MCP {
		result[name] = MCPServer{
			Command:      mcp.Command,
			Args:         mcp.Args,
			Env:          mcp.Env,
			Notes:        mcp.Notes,
			Installation: mcp.Installation,
			SCM:          "bundle:" + source, // Mark as coming from a bundle
		}
	}

	return result
}
