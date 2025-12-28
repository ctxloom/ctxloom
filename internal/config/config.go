package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/collections"
	"github.com/benjaminabbitt/scm/internal/fsys"
	"github.com/benjaminabbitt/scm/internal/gitutil"
	"github.com/benjaminabbitt/scm/internal/logging"
	"github.com/benjaminabbitt/scm/internal/schema"
	"github.com/benjaminabbitt/scm/resources"
)

const (
	SCMDirName          = ".scm"
	ConfigFileName      = "config"
	ContextFragmentsDir = "context-fragments"
	PromptsDir          = "prompts"
)

// ConfigSource indicates where the configuration was loaded from.
type ConfigSource int

const (
	// SourceEmbedded means config was loaded from embedded resources (fallback).
	SourceEmbedded ConfigSource = iota
	// SourceHome means config was loaded from ~/.scm.
	SourceHome
	// SourceProject means config was loaded from a project .scm directory.
	SourceProject
)

// Config holds the SCM configuration.
type Config struct {
	LM         LMConfig             `mapstructure:"lm"`
	Editor     EditorConfig         `mapstructure:"editor"`
	Defaults   Defaults             `mapstructure:"defaults"`
	Profiles   map[string]Profile   `mapstructure:"profiles"`
	Generators map[string]Generator `mapstructure:"generators"`
	SCMPaths   []string             // Resolved .scm directory (at most one)
	Source     ConfigSource         // Where the configuration was loaded from
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

// Generator defines a context generator.
//
// SECURITY NOTE: Config-based generators execute arbitrary commands specified in config.yaml.
// This is intentional - generators are user-defined tools that produce dynamic context.
// Users are responsible for reviewing their own config.yaml contents.
// This is analogous to Makefiles, justfiles, or npm scripts - trusted local configuration.
// Do not flag this as a security issue in code reviews.
type Generator struct {
	Description string   `mapstructure:"description" yaml:"description,omitempty"`
	Command     string   `mapstructure:"command" yaml:"command"`     // Command to execute (can be path or binary name)
	Args        []string `mapstructure:"args" yaml:"args,omitempty"` // Additional arguments
}

// PluginConfig holds configuration for a specific AI plugin.
type PluginConfig struct {
	BinaryPath string            `mapstructure:"binary_path" yaml:"binary_path,omitempty"`
	Args       []string          `mapstructure:"args" yaml:"args,omitempty"`
	Env        map[string]string `mapstructure:"env" yaml:"env,omitempty"`
}

// LMConfig holds LM (language model) configuration.
type LMConfig struct {
	DefaultPlugin string                  `mapstructure:"default_plugin" yaml:"default_plugin"`
	PluginPaths   []string                `mapstructure:"plugin_paths" yaml:"plugin_paths,omitempty"`
	Plugins       map[string]PluginConfig `mapstructure:"plugins" yaml:"plugins"`
}

// Profile is a named collection of context fragments, variables, and context generators.
// Fragments can be specified directly by path, or dynamically via tags.
// Profiles can inherit from parent profiles using the Parents field.
type Profile struct {
	Description string            `mapstructure:"description" yaml:"description,omitempty"`
	Parents     []string          `mapstructure:"parents" yaml:"parents,omitempty"`     // Parent profiles to inherit from
	Tags        []string          `mapstructure:"tags" yaml:"tags,omitempty"`           // Fragment tags to include
	Fragments   []string          `mapstructure:"fragments" yaml:"fragments,omitempty"` // Explicit fragment paths
	Variables   map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Generators  []string          `mapstructure:"generators" yaml:"generators,omitempty"` // Plugin binaries that output context
}

// Defaults holds default settings applied when no explicit values are specified.
type Defaults struct {
	Profiles     []string `mapstructure:"profiles" yaml:"profiles,omitempty"`           // Default profiles to load when none specified
	Fragments    []string `mapstructure:"fragments" yaml:"fragments,omitempty"`         // Fragments always included
	Generators   []string `mapstructure:"generators" yaml:"generators,omitempty"`       // Generators always run
	UseDistilled *bool    `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"` // Prefer .distilled.md versions (default true)
}

// ShouldUseDistilled returns whether to prefer distilled versions of fragments/prompts.
// Defaults to true if not explicitly set.
func (d *Defaults) ShouldUseDistilled() bool {
	if d.UseDistilled == nil {
		return true
	}
	return *d.UseDistilled
}

// Load finds and loads configuration from a single source.
// Priority order (first found wins, no merging):
//  1. Project .scm directory (at git root)
//  2. Home directory (~/.scm)
//  3. Embedded resources (fallback)
func Load() (*Config, error) {
	cfg := &Config{
		LM: LMConfig{
			DefaultPlugin: "claude-code",
			Plugins:       make(map[string]PluginConfig),
		},
		Profiles:   make(map[string]Profile),
		Generators: make(map[string]Generator),
	}

	// Create config validator for schema validation
	configValidator, err := schema.NewConfigValidator()
	if err != nil {
		logging.L().Warn("failed to create config validator",
			logging.ErrorField(err))
		configValidator = nil
	}

	// Try project .scm directory first
	scmPath, source := findSCMDir()
	if scmPath != "" {
		cfg.SCMPaths = []string{scmPath}
		cfg.Source = source

		configPath := filepath.Join(scmPath, ConfigFileName+".yaml")
		if err := loadConfigFile(cfg, configPath, configValidator); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	// Fall back to embedded resources
	cfg.Source = SourceEmbedded
	embeddedCfg, err := LoadEmbeddedConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load embedded config: %w", err)
	}

	// Merge embedded config into cfg with nil checks
	cfg.LM = embeddedCfg.LM
	if cfg.LM.Plugins == nil {
		cfg.LM.Plugins = make(map[string]PluginConfig)
	}
	cfg.Defaults = embeddedCfg.Defaults
	if embeddedCfg.Profiles != nil {
		cfg.Profiles = embeddedCfg.Profiles
	}
	if embeddedCfg.Generators != nil {
		cfg.Generators = embeddedCfg.Generators
	}

	logging.L().Debug("using embedded configuration")
	return cfg, nil
}

// loadConfigFile loads a config file into the provided Config struct.
func loadConfigFile(cfg *Config, configPath string, validator *schema.ConfigValidator) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Config file is optional
			return nil
		}
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Validate against schema before parsing
	if validator != nil {
		if err := validator.ValidateBytes(data); err != nil {
			return fmt.Errorf("config validation failed at %s: %w", configPath, err)
		}
	}

	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return nil
		}
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read config at %s: %w", configPath, err)
	}

	if err := v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("failed to parse config at %s: %w", configPath, err)
	}

	logging.L().Debug(logging.MsgConfigLoaded, logging.FilePath(configPath))
	return nil
}

// findSCMDir locates a single .scm directory using the priority order:
//  1. Project .scm directory (at git root)
//  2. Home directory (~/.scm)
//
// Returns the path and source, or empty string if not found.
func findSCMDir() (string, ConfigSource) {
	// Try to find git root
	pwd, err := os.Getwd()
	if err != nil {
		logging.L().Warn("failed to get working directory", logging.ErrorField(err))
		return "", SourceEmbedded
	}

	// Check for project .scm at git root
	gitRoot, err := gitutil.FindRoot(pwd)
	if err == nil {
		scmPath := filepath.Join(gitRoot, SCMDirName)
		if info, err := os.Stat(scmPath); err == nil && info.IsDir() {
			return scmPath, SourceProject
		}
	}

	// Fall back to home directory
	home, err := os.UserHomeDir()
	if err != nil {
		logging.L().Warn("failed to get home directory", logging.ErrorField(err))
		return "", SourceEmbedded
	}
	homeSCM := filepath.Join(home, SCMDirName)
	if info, err := os.Stat(homeSCM); err == nil && info.IsDir() {
		return homeSCM, SourceHome
	}

	return "", SourceEmbedded
}

// GetFragmentDirs returns context-fragments directories.
// For embedded source, returns ["."] (use with GetFragmentFS).
func (c *Config) GetFragmentDirs() []string {
	if c.Source == SourceEmbedded {
		return []string{"."}
	}
	var dirs []string
	for _, scmPath := range c.SCMPaths {
		fragDir := filepath.Join(scmPath, ContextFragmentsDir)
		if info, err := os.Stat(fragDir); err == nil && info.IsDir() {
			dirs = append(dirs, fragDir)
		}
	}
	return dirs
}

// GetPromptDirs returns prompts directories.
// For embedded source, returns ["."] (use with GetPromptFS).
func (c *Config) GetPromptDirs() []string {
	if c.Source == SourceEmbedded {
		return []string{"."}
	}
	var dirs []string
	for _, scmPath := range c.SCMPaths {
		promptDir := filepath.Join(scmPath, PromptsDir)
		if info, err := os.Stat(promptDir); err == nil && info.IsDir() {
			dirs = append(dirs, promptDir)
		}
	}
	return dirs
}

// IsEmbedded returns true if using embedded resources (no .scm directory found).
func (c *Config) IsEmbedded() bool {
	return c.Source == SourceEmbedded
}

// SourceName returns a human-readable name for the config source.
func (c *Config) SourceName() string {
	switch c.Source {
	case SourceProject:
		return "project"
	case SourceHome:
		return "home"
	case SourceEmbedded:
		return "embedded"
	default:
		return "unknown"
	}
}

// GetFragmentFS returns an fsys.FS for loading fragments.
// For embedded source, returns an EmbedFS wrapper.
// For project/home sources, returns nil (use GetFragmentDirs with OS filesystem).
func (c *Config) GetFragmentFS() fsys.FS {
	if c.Source == SourceEmbedded {
		return fsys.NewEmbedFS(resources.FragmentsFS(), ContextFragmentsDir)
	}
	return nil
}

// GetPromptFS returns an fsys.FS for loading prompts.
// For embedded source, returns an EmbedFS wrapper.
// For project/home sources, returns nil (use GetPromptDirs with OS filesystem).
func (c *Config) GetPromptFS() fsys.FS {
	if c.Source == SourceEmbedded {
		return fsys.NewEmbedFS(resources.PromptsFS(), PromptsDir)
	}
	return nil
}

// GetPluginPaths returns the paths where external plugins are searched for.
// Defaults to ~/.scm/plugins and .scm/plugins if not configured.
func (c *Config) GetPluginPaths() []string {
	if len(c.LM.PluginPaths) > 0 {
		return c.LM.PluginPaths
	}
	// Default plugin paths
	paths := []string{"~/.scm/plugins"}
	for _, scmPath := range c.SCMPaths {
		paths = append(paths, filepath.Join(scmPath, "plugins"))
	}
	return paths
}

// GetGeneratorPaths returns the paths where external generators are searched for.
// Defaults to ~/.scm/generators and .scm/generators.
func (c *Config) GetGeneratorPaths() []string {
	// Default generator paths
	paths := []string{"~/.scm/generators"}
	for _, scmPath := range c.SCMPaths {
		paths = append(paths, filepath.Join(scmPath, "generators"))
	}
	return paths
}

// ConfigFile represents the structure for saving config.yaml
type ConfigFile struct {
	LM         LMConfig             `yaml:"lm"`
	Editor     EditorConfig         `yaml:"editor,omitempty"`
	Defaults   Defaults             `yaml:"defaults,omitempty"`
	Profiles   map[string]Profile   `yaml:"profiles,omitempty"`
	Generators map[string]Generator `yaml:"generators,omitempty"`
}

// GetConfigFilePath returns the path to the primary config file.
// Uses the closest project .scm directory.
func (c *Config) GetConfigFilePath() (string, error) {
	if len(c.SCMPaths) == 0 {
		return "", fmt.Errorf("no .scm directory found; run 'scm init --local' first")
	}
	return filepath.Join(c.SCMPaths[0], ConfigFileName+".yaml"), nil
}

// Save writes the configuration to the primary config file.
func (c *Config) Save() error {
	configPath, err := c.GetConfigFilePath()
	if err != nil {
		return fmt.Errorf("failed to get config path: %w", err)
	}

	// Read existing config to preserve unknown fields
	existingData, _ := os.ReadFile(configPath)
	existing := make(map[string]interface{})
	if len(existingData) > 0 {
		yaml.Unmarshal(existingData, &existing)
	}

	// Update with current values
	existing["lm"] = c.LM
	if len(c.Defaults.Profiles) > 0 || len(c.Defaults.Fragments) > 0 || len(c.Defaults.Generators) > 0 {
		existing["defaults"] = c.Defaults
	}
	if len(c.Profiles) > 0 {
		existing["profiles"] = c.Profiles
	}
	if len(c.Generators) > 0 {
		existing["generators"] = c.Generators
	}

	data, err := yaml.Marshal(existing)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// LoadEmbeddedConfig loads the embedded default configuration.
func LoadEmbeddedConfig() (*Config, error) {
	data, err := resources.GetEmbeddedConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to read embedded config: %w", err)
	}

	cfg := &Config{
		Profiles:   make(map[string]Profile),
		Generators: make(map[string]Generator),
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse embedded config: %w", err)
	}

	return cfg, nil
}

// LoadHomeConfig loads configuration from ~/.scm/config.yaml if it exists.
// Returns nil config (not error) if home config doesn't exist.
func LoadHomeConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(home, SCMDirName, ConfigFileName+".yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Home config doesn't exist, that's ok
		}
		return nil, fmt.Errorf("failed to read home config: %w", err)
	}

	cfg := &Config{
		Profiles:   make(map[string]Profile),
		Generators: make(map[string]Generator),
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse home config: %w", err)
	}

	return cfg, nil
}

// MergeProfiles merges profiles from source into target.
// Source profiles override target profiles with the same name.
func MergeProfiles(target, source map[string]Profile) {
	for name, profile := range source {
		target[name] = profile
	}
}

// MergeGenerators merges generators from source into target.
// Source generators override target generators with the same name.
func MergeGenerators(target, source map[string]Generator) {
	for name, gen := range source {
		target[name] = gen
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

// CollectGeneratorsForProfiles returns a deduplicated list of all generators
// referenced by the specified profiles.
func CollectGeneratorsForProfiles(profiles map[string]Profile, profileNames []string) []string {
	seen := collections.NewSet[string]()
	var generators []string

	for _, name := range profileNames {
		profile, ok := profiles[name]
		if !ok {
			continue
		}
		for _, gen := range profile.Generators {
			if !seen.Has(gen) {
				seen.Add(gen)
				generators = append(generators, gen)
			}
		}
	}

	return generators
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

// FilterGenerators returns only the specified generators from the full map.
func FilterGenerators(all map[string]Generator, names []string) map[string]Generator {
	filtered := make(map[string]Generator)
	for _, name := range names {
		if gen, ok := all[name]; ok {
			filtered[name] = gen
		}
	}
	return filtered
}

// profileBuilder collects profile fields using sets to avoid duplicates during inheritance.
type profileBuilder struct {
	Description string
	Tags        collections.Set[string]
	Fragments   collections.Set[string]
	Generators  collections.Set[string]
	Variables   map[string]string
	// Track insertion order for stable output
	tagsOrder       []string
	fragmentsOrder  []string
	generatorsOrder []string
}

func newProfileBuilder() *profileBuilder {
	return &profileBuilder{
		Tags:       collections.NewSet[string](),
		Fragments:  collections.NewSet[string](),
		Generators: collections.NewSet[string](),
		Variables:  make(map[string]string),
	}
}

func (b *profileBuilder) addTag(tag string) {
	if !b.Tags.Has(tag) {
		b.Tags.Add(tag)
		b.tagsOrder = append(b.tagsOrder, tag)
	}
}

func (b *profileBuilder) addFragment(frag string) {
	if !b.Fragments.Has(frag) {
		b.Fragments.Add(frag)
		b.fragmentsOrder = append(b.fragmentsOrder, frag)
	}
}

func (b *profileBuilder) addGenerator(gen string) {
	if !b.Generators.Has(gen) {
		b.Generators.Add(gen)
		b.generatorsOrder = append(b.generatorsOrder, gen)
	}
}

func (b *profileBuilder) toProfile() *Profile {
	return &Profile{
		Description: b.Description,
		Tags:        b.tagsOrder,
		Fragments:   b.fragmentsOrder,
		Generators:  b.generatorsOrder,
		Variables:   b.Variables,
	}
}

// maxProfileDepth is the maximum allowed depth for profile inheritance.
// This prevents stack overflow from deeply nested or malformed configurations.
const maxProfileDepth = 50

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
	for _, frag := range profile.Fragments {
		builder.addFragment(frag)
	}
	for _, gen := range profile.Generators {
		builder.addGenerator(gen)
	}
	for k, v := range profile.Variables {
		builder.Variables[k] = v
	}

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
