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

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/collections"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/schema"
)

const (
	AppDirName     = ".ctxloom"
	ConfigFileName = "config"
	BundlesDir     = "bundles"
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
	fs       afero.Fs           // Filesystem for file operations (nil = OS filesystem)
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

// GetDefaultLLMModel returns the default LLM model name.
// Returns empty string if not configured (backend will use its own default).
func (c *Config) GetDefaultLLMModel() string {
	return c.Defaults.LLMModel
}

// SetDefaultLLMPlugin sets the default LLM plugin name.
func (c *Config) SetDefaultLLMPlugin(name string) {
	c.Defaults.LLMPlugin = name
}

// GetCompactionPlugin returns the plugin to use for session compaction.
// Defaults to the default LLM plugin, or "claude-code" if not set.
func (c *Config) GetCompactionPlugin() string {
	if c.Defaults.CompactionPlugin != "" {
		return c.Defaults.CompactionPlugin
	}
	if c.Defaults.LLMPlugin != "" {
		return c.Defaults.LLMPlugin
	}
	return "claude-code"
}

// GetCompactionModel returns the model to use for session compaction.
// Defaults to "haiku".
func (c *Config) GetCompactionModel() string {
	if c.Defaults.CompactionModel != "" {
		return c.Defaults.CompactionModel
	}
	return "haiku"
}

// GetCompactionChunkSize returns the target chunk size for compaction.
// Defaults to 8000 tokens.
func (c *Config) GetCompactionChunkSize() int {
	if c.Defaults.CompactionChunks > 0 {
		return c.Defaults.CompactionChunks
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
	SCM     string `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`                              // Hash identifying ctxloom-managed hooks
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
	SCM          string            `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`                                      // Marker for ctxloom-managed servers
}

// MCPConfig holds MCP server configuration.
type MCPConfig struct {
	// AutoRegisterCtxloom controls whether ctxloom's own MCP server is auto-registered.
	// Defaults to true if not specified.
	AutoRegisterCtxloom *bool `mapstructure:"auto_register_ctxloom" yaml:"auto_register_ctxloom,omitempty"`

	// Servers defines MCP servers to register (unified across backends).
	Servers map[string]MCPServer `mapstructure:"servers" yaml:"servers,omitempty"`

	// Plugins holds backend-specific MCP server overrides (passthrough).
	// Keys are backend names (e.g., "claude-code", "gemini").
	Plugins map[string]map[string]MCPServer `mapstructure:"plugins" yaml:"plugins,omitempty"`
}

// ShouldAutoRegisterCtxloom returns whether to auto-register the ctxloom MCP server.
// Defaults to true if not explicitly set.
func (m *MCPConfig) ShouldAutoRegisterCtxloom() bool {
	if m == nil || m.AutoRegisterCtxloom == nil {
		return true
	}
	return *m.AutoRegisterCtxloom
}

// MergeMCPConfig merges src MCP config into dest.
// Later sources override earlier ones for the same server name.
func MergeMCPConfig(dest *MCPConfig, src *MCPConfig) {
	if src == nil || dest == nil {
		return
	}

	// Merge auto_register_ctxloom (later wins)
	if src.AutoRegisterCtxloom != nil {
		dest.AutoRegisterCtxloom = src.AutoRegisterCtxloom
	}

	// Merge unified servers
	if dest.Servers == nil {
		dest.Servers = make(map[string]MCPServer)
	}
	for name, server := range src.Servers {
		dest.Servers[name] = server
	}

	// Merge plugin-specific servers
	if dest.Plugins == nil {
		dest.Plugins = make(map[string]map[string]MCPServer)
	}
	for backend, servers := range src.Plugins {
		if dest.Plugins[backend] == nil {
			dest.Plugins[backend] = make(map[string]MCPServer)
		}
		for name, server := range servers {
			dest.Plugins[backend][name] = server
		}
	}
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

// FragmentRef references a fragment with optional priority for context ordering.
// Higher priority fragments are placed at the beginning/end of context (bookend strategy)
// to address the "lost in the middle" problem where LLMs poorly attend to middle content.
type FragmentRef struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority,omitempty"` // Higher = more important (default: 0)
}

// UnmarshalYAML supports both string and struct formats for backward compatibility.
// Examples:
//
//	fragments:
//	  - go-style              # String format, priority defaults to 0
//	  - name: testing
//	    priority: 10          # Struct format with explicit priority
func (f *FragmentRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		f.Name = node.Value
		f.Priority = 0
		return nil
	}
	// Struct format
	type plain FragmentRef
	return node.Decode((*plain)(f))
}

// MarshalYAML outputs as string if priority is 0, otherwise as struct.
func (f FragmentRef) MarshalYAML() (interface{}, error) {
	if f.Priority == 0 {
		return f.Name, nil
	}
	type plain FragmentRef
	return plain(f), nil
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
	Fragments   []FragmentRef     `mapstructure:"fragments" yaml:"fragments,omitempty"`       // Fragment references with optional priority
	Variables   map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Hooks       HooksConfig       `mapstructure:"hooks" yaml:"hooks,omitempty"`               // Hooks for this profile (inherited)
	MCP         MCPConfig         `mapstructure:"mcp" yaml:"mcp,omitempty"`                   // MCP servers for this profile (inherited)
	MCPServers  []string          `mapstructure:"mcp_servers" yaml:"mcp_servers,omitempty"`   // Remote MCP server references (legacy)

	// Exclusions - items to filter out after inheritance resolution
	ExcludeFragments []string `mapstructure:"exclude_fragments" yaml:"exclude_fragments,omitempty"`
	ExcludePrompts   []string `mapstructure:"exclude_prompts" yaml:"exclude_prompts,omitempty"`
	ExcludeMCP       []string `mapstructure:"exclude_mcp" yaml:"exclude_mcp,omitempty"`
}

// Defaults holds default settings applied when no explicit values are specified.
type Defaults struct {
	Profiles          []string `mapstructure:"profiles" yaml:"profiles,omitempty"`                     // Default profiles to load (supports multiple)
	LLMPlugin         string   `mapstructure:"llm_plugin" yaml:"llm_plugin,omitempty"`                 // Default LLM plugin name (e.g., "claude-code", "gemini")
	LLMModel          string   `mapstructure:"llm_model" yaml:"llm_model,omitempty"`                   // Default LLM model (e.g., "opus", "sonnet", "haiku")
	UseDistilled      *bool    `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"`           // Prefer .distilled.md versions (default true)
	CompactionPlugin  string   `mapstructure:"compaction_plugin" yaml:"compaction_plugin,omitempty"`   // LLM plugin for session compaction (default: llm_plugin)
	CompactionModel   string   `mapstructure:"compaction_model" yaml:"compaction_model,omitempty"`     // Model for session compaction (default: "haiku")
	CompactionChunks  int      `mapstructure:"compaction_chunks" yaml:"compaction_chunks,omitempty"`   // Target tokens per chunk (default: 8000)
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

	configPath := filepath.Join(appPath, ConfigFileName+".yaml")
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

// findAppDir locates the .ctxloom directory.
// Priority:
//  1. Walk up from cwd looking for .ctxloom directory
//  2. Fall back to user home ~/.ctxloom directory
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

// GetBundleDirs returns bundles directories.
func (c *Config) GetBundleDirs() []string {
	var dirs []string
	for _, appPath := range c.AppPaths {
		bundleDir := filepath.Join(appPath, BundlesDir)
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
// Defaults to .ctxloom/plugins if not configured.
func (c *Config) GetPluginPaths() []string {
	if len(c.LM.PluginPaths) > 0 {
		return c.LM.PluginPaths
	}
	// Default plugin paths from project .ctxloom
	var paths []string
	for _, appPath := range c.AppPaths {
		paths = append(paths, filepath.Join(appPath, "plugins"))
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
// Uses the closest project .ctxloom directory.
func (c *Config) GetConfigFilePath() (string, error) {
	if len(c.AppPaths) == 0 {
		return "", fmt.Errorf("no .ctxloom directory found; run 'ctxloom init --local' first")
	}
	return filepath.Join(c.AppPaths[0], ConfigFileName+".yaml"), nil
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
	existingData, err := afero.ReadFile(fs, configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing config: %w", err)
	}
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		if err := yaml.Unmarshal(existingData, &existing); err != nil {
			// Warn but continue - fault tolerance principle: don't block operations
			fmt.Fprintf(os.Stderr, "ctxloom: warning: existing config may be corrupted, unknown fields may be lost: %v\n", err)
		}
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

	if len(c.MCP.Servers) > 0 || len(c.MCP.Plugins) > 0 || c.MCP.AutoRegisterCtxloom != nil {
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
	fragmentsOrder   []FragmentRef
	// Track fragment priorities (keep highest when same fragment referenced multiple times)
	fragmentPriorities map[string]int
	// Track seen hooks by key (command+matcher) for deduplication
	seenHooks collections.Set[string]
	// Exclusion sets - accumulate through inheritance
	ExcludeFragments collections.Set[string]
	ExcludePrompts   collections.Set[string]
	ExcludeMCP       collections.Set[string]
}

func newProfileBuilder() *profileBuilder {
	return &profileBuilder{
		Tags:               collections.NewSet[string](),
		Bundles:            collections.NewSet[string](),
		BundleItems:        collections.NewSet[string](),
		Fragments:          collections.NewSet[string](),
		Variables:          make(map[string]string),
		fragmentPriorities: make(map[string]int),
		Hooks: HooksConfig{
			Plugins: make(map[string]BackendHooks),
		},
		MCP: MCPConfig{
			Servers: make(map[string]MCPServer),
			Plugins: make(map[string]map[string]MCPServer),
		},
		seenHooks:        collections.NewSet[string](),
		ExcludeFragments: collections.NewSet[string](),
		ExcludePrompts:   collections.NewSet[string](),
		ExcludeMCP:       collections.NewSet[string](),
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

func (b *profileBuilder) addFragment(frag FragmentRef) {
	if !b.Fragments.Has(frag.Name) {
		b.Fragments.Add(frag.Name)
		b.fragmentsOrder = append(b.fragmentsOrder, frag)
		b.fragmentPriorities[frag.Name] = frag.Priority
	} else if frag.Priority > b.fragmentPriorities[frag.Name] {
		// Update priority if higher (child profile can override parent's priority)
		b.fragmentPriorities[frag.Name] = frag.Priority
		// Update the priority in fragmentsOrder
		for i := range b.fragmentsOrder {
			if b.fragmentsOrder[i].Name == frag.Name {
				b.fragmentsOrder[i].Priority = frag.Priority
				break
			}
		}
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
	MergeMCPConfig(&b.MCP, &source)
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
	// Filter excluded fragments
	var filteredFragments []FragmentRef
	for _, frag := range b.fragmentsOrder {
		if !b.ExcludeFragments.Has(frag.Name) {
			filteredFragments = append(filteredFragments, frag)
		}
	}

	// Filter excluded MCP servers
	filteredMCP := b.MCP
	if len(b.ExcludeMCP.Items()) > 0 && filteredMCP.Servers != nil {
		filteredServers := make(map[string]MCPServer)
		for name, server := range filteredMCP.Servers {
			if !b.ExcludeMCP.Has(name) {
				filteredServers[name] = server
			}
		}
		filteredMCP.Servers = filteredServers
	}

	return &Profile{
		Description:      b.Description,
		Tags:             b.tagsOrder,
		Bundles:          b.bundlesOrder,
		BundleItems:      b.bundleItemsOrder,
		Fragments:        filteredFragments,
		Variables:        b.Variables,
		Hooks:            b.Hooks,
		MCP:              filteredMCP,
		ExcludeFragments: b.ExcludeFragments.Items(),
		ExcludePrompts:   b.ExcludePrompts.Items(),
		ExcludeMCP:       b.ExcludeMCP.Items(),
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

	// Accumulate exclusions (exclusions always win - cannot un-exclude)
	for _, frag := range profile.ExcludeFragments {
		builder.ExcludeFragments.Add(frag)
	}
	for _, prompt := range profile.ExcludePrompts {
		builder.ExcludePrompts.Add(prompt)
	}
	for _, mcp := range profile.ExcludeMCP {
		builder.ExcludeMCP.Add(mcp)
	}

	// Set description from the leaf profile (will be overwritten by each child)
	builder.Description = profile.Description

	return nil
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

	// Get the base .ctxloom directory
	if len(c.AppPaths) == 0 {
		return result
	}
	appDir := c.AppPaths[0]

	// Load each default profile and collect MCP servers
	profileLoader := c.GetProfileLoader()
	bundleDirs := []string{filepath.Join(appDir, BundlesDir)}
	bundleLoader := bundles.NewLoader(bundleDirs, false)

	for _, defaultProfile := range defaultProfiles {
		profile, err := profileLoader.Load(defaultProfile)
		if err != nil {
			continue
		}

		// Process each bundle URL in the profile
		for _, bundleRef := range profile.Bundles {
			servers := loadMCPFromBundleRef(bundleRef, appDir, bundleLoader)
			for name, server := range servers {
				result[name] = server
			}
		}
	}

	return result
}

// loadMCPFromBundleRef loads MCP servers from a bundle reference (URL or name).
func loadMCPFromBundleRef(bundleRef string, appDir string, loader *bundles.Loader) map[string]MCPServer {
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
	localPath := ref.LocalPath(appDir, remote.ItemTypeBundle)

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
