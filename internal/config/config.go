package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/fsys"
	"github.com/benjaminabbitt/scm/internal/gitutil"
	"github.com/benjaminabbitt/scm/internal/logging"
	"github.com/benjaminabbitt/scm/internal/ml"
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
	Personas   map[string]Persona   `mapstructure:"personas"`
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
type Generator struct {
	Description string   `mapstructure:"description" yaml:"description,omitempty"`
	Command     string   `mapstructure:"command" yaml:"command"`     // Command to execute (can be path or binary name)
	Args        []string `mapstructure:"args" yaml:"args,omitempty"` // Additional arguments
}

// LMConfig holds LM (language model) configuration.
type LMConfig struct {
	DefaultPlugin string                     `mapstructure:"default_plugin" yaml:"default_plugin"`
	Plugins       map[string]ml.PluginConfig `mapstructure:"plugins" yaml:"plugins"`
}

// Persona is a named collection of context fragments, variables, and context generators.
// Fragments can be specified directly by path, or dynamically via tags.
// Personas can inherit from parent personas using the Parents field.
type Persona struct {
	Description string            `mapstructure:"description" yaml:"description,omitempty"`
	Parents     []string          `mapstructure:"parents" yaml:"parents,omitempty"`     // Parent personas to inherit from
	Tags        []string          `mapstructure:"tags" yaml:"tags,omitempty"`           // Fragment tags to include
	Fragments   []string          `mapstructure:"fragments" yaml:"fragments,omitempty"` // Explicit fragment paths
	Variables   map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Generators  []string          `mapstructure:"generators" yaml:"generators,omitempty"` // Plugin binaries that output context
}

// Defaults holds default settings applied when no explicit values are specified.
type Defaults struct {
	Personas     []string `mapstructure:"personas" yaml:"personas,omitempty"`           // Default personas to load when none specified
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
			Plugins:       make(map[string]ml.PluginConfig),
		},
		Personas:   make(map[string]Persona),
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

	// Merge embedded config into cfg
	cfg.LM = embeddedCfg.LM
	cfg.Defaults = embeddedCfg.Defaults
	cfg.Personas = embeddedCfg.Personas
	cfg.Generators = embeddedCfg.Generators

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

// ConfigFile represents the structure for saving config.yaml
type ConfigFile struct {
	LM         LMConfig             `yaml:"lm"`
	Editor     EditorConfig         `yaml:"editor,omitempty"`
	Defaults   Defaults             `yaml:"defaults,omitempty"`
	Personas   map[string]Persona   `yaml:"personas,omitempty"`
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
	if len(c.Defaults.Personas) > 0 || len(c.Defaults.Fragments) > 0 || len(c.Defaults.Generators) > 0 {
		existing["defaults"] = c.Defaults
	}
	if len(c.Personas) > 0 {
		existing["personas"] = c.Personas
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
		Personas:   make(map[string]Persona),
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
		Personas:   make(map[string]Persona),
		Generators: make(map[string]Generator),
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse home config: %w", err)
	}

	return cfg, nil
}

// MergePersonas merges personas from source into target.
// Source personas override target personas with the same name.
func MergePersonas(target, source map[string]Persona) {
	for name, persona := range source {
		target[name] = persona
	}
}

// MergeGenerators merges generators from source into target.
// Source generators override target generators with the same name.
func MergeGenerators(target, source map[string]Generator) {
	for name, gen := range source {
		target[name] = gen
	}
}

// CollectFragmentsForPersonas returns a deduplicated list of all fragments
// referenced by the specified personas.
func CollectFragmentsForPersonas(personas map[string]Persona, personaNames []string) ([]string, error) {
	seen := make(map[string]bool)
	var fragments []string

	for _, name := range personaNames {
		persona, ok := personas[name]
		if !ok {
			return nil, fmt.Errorf("unknown persona: %s", name)
		}
		for _, frag := range persona.Fragments {
			if !seen[frag] {
				seen[frag] = true
				fragments = append(fragments, frag)
			}
		}
	}

	return fragments, nil
}

// CollectGeneratorsForPersonas returns a deduplicated list of all generators
// referenced by the specified personas.
func CollectGeneratorsForPersonas(personas map[string]Persona, personaNames []string) []string {
	seen := make(map[string]bool)
	var generators []string

	for _, name := range personaNames {
		persona, ok := personas[name]
		if !ok {
			continue
		}
		for _, gen := range persona.Generators {
			if !seen[gen] {
				seen[gen] = true
				generators = append(generators, gen)
			}
		}
	}

	return generators
}

// FilterPersonas returns only the specified personas from the full map.
func FilterPersonas(all map[string]Persona, names []string) map[string]Persona {
	filtered := make(map[string]Persona)
	for _, name := range names {
		if persona, ok := all[name]; ok {
			filtered[name] = persona
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

// personaBuilder collects persona fields using sets to avoid duplicates during inheritance.
type personaBuilder struct {
	Description string
	Tags        map[string]bool
	Fragments   map[string]bool
	Generators  map[string]bool
	Variables   map[string]string
	// Track insertion order for stable output
	tagsOrder       []string
	fragmentsOrder  []string
	generatorsOrder []string
}

func newPersonaBuilder() *personaBuilder {
	return &personaBuilder{
		Tags:       make(map[string]bool),
		Fragments:  make(map[string]bool),
		Generators: make(map[string]bool),
		Variables:  make(map[string]string),
	}
}

func (b *personaBuilder) addTag(tag string) {
	if !b.Tags[tag] {
		b.Tags[tag] = true
		b.tagsOrder = append(b.tagsOrder, tag)
	}
}

func (b *personaBuilder) addFragment(frag string) {
	if !b.Fragments[frag] {
		b.Fragments[frag] = true
		b.fragmentsOrder = append(b.fragmentsOrder, frag)
	}
}

func (b *personaBuilder) addGenerator(gen string) {
	if !b.Generators[gen] {
		b.Generators[gen] = true
		b.generatorsOrder = append(b.generatorsOrder, gen)
	}
}

func (b *personaBuilder) toPersona() *Persona {
	return &Persona{
		Description: b.Description,
		Tags:        b.tagsOrder,
		Fragments:   b.fragmentsOrder,
		Generators:  b.generatorsOrder,
		Variables:   b.Variables,
	}
}

// ResolvePersona resolves a persona by recursively merging all parent personas.
// Parents are processed depth-first, with later parents and the child overriding earlier values.
// Uses sets internally to handle diamond inheritance (shared ancestors) without duplicates.
// Returns an error if the persona doesn't exist or if circular dependencies are detected.
func ResolvePersona(personas map[string]Persona, name string) (*Persona, error) {
	visited := make(map[string]bool)
	builder := newPersonaBuilder()
	if err := resolvePersonaRecursive(personas, name, visited, builder); err != nil {
		return nil, err
	}
	return builder.toPersona(), nil
}

func resolvePersonaRecursive(personas map[string]Persona, name string, visited map[string]bool, builder *personaBuilder) error {
	// Check for circular dependency
	if visited[name] {
		return fmt.Errorf("circular persona inheritance detected: %s", name)
	}
	visited[name] = true

	persona, ok := personas[name]
	if !ok {
		return fmt.Errorf("unknown persona: %s", name)
	}

	// Resolve parents first (depth-first)
	for _, parentName := range persona.Parents {
		if err := resolvePersonaRecursive(personas, parentName, copyVisited(visited), builder); err != nil {
			return fmt.Errorf("failed to resolve parent %s: %w", parentName, err)
		}
	}

	// Merge this persona's values (child overrides parents for variables)
	for _, tag := range persona.Tags {
		builder.addTag(tag)
	}
	for _, frag := range persona.Fragments {
		builder.addFragment(frag)
	}
	for _, gen := range persona.Generators {
		builder.addGenerator(gen)
	}
	for k, v := range persona.Variables {
		builder.Variables[k] = v
	}

	// Set description from the leaf persona (will be overwritten by each child)
	builder.Description = persona.Description

	return nil
}

// copyVisited creates a copy of the visited map for branching recursion.
func copyVisited(visited map[string]bool) map[string]bool {
	result := make(map[string]bool, len(visited))
	for k, v := range visited {
		result[k] = v
	}
	return result
}

// DedupeStrings removes duplicates from a string slice while preserving order.
func DedupeStrings(items []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
