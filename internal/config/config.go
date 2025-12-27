package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/mlcm/internal/logging"
	"github.com/benjaminabbitt/mlcm/internal/ml"
	"github.com/benjaminabbitt/mlcm/internal/schema"
	"github.com/benjaminabbitt/mlcm/resources"
)

const (
	MLCMDirName         = ".mlcm"
	ConfigFileName      = "config"
	ContextFragmentsDir = "context-fragments"
	PromptsDir          = "prompts"
)

// Config holds the MLCM configuration.
type Config struct {
	LM         LMConfig             `mapstructure:"lm"`
	Editor     EditorConfig         `mapstructure:"editor"`
	Defaults   Defaults             `mapstructure:"defaults"`
	Personas   map[string]Persona   `mapstructure:"personas"`
	Generators map[string]Generator `mapstructure:"generators"`
	MLCMPaths  []string             // Resolved project .mlcm directories (closest to pwd first)
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

// Load finds and loads configuration from project .mlcm directories.
// It walks up from the current directory looking for .mlcm directories.
// Home directory (~/.mlcm) is only used as a template source for init.
func Load() (*Config, error) {
	cfg := &Config{
		LM: LMConfig{
			DefaultPlugin: "claude-code",
			Plugins:       make(map[string]ml.PluginConfig),
		},
		Personas:   make(map[string]Persona),
		Generators: make(map[string]Generator),
	}

	// Find all .mlcm directories
	mlcmPaths, err := findMLCMDirs()
	if err != nil {
		return nil, fmt.Errorf("failed to find .mlcm directories: %w", err)
	}
	cfg.MLCMPaths = mlcmPaths

	// Create config validator for schema validation
	configValidator, err := schema.NewConfigValidator()
	if err != nil {
		// If schema validator fails to load, log warning but continue
		// This allows the tool to work even if schema is missing
		logging.L().Warn("failed to create config validator",
			logging.ErrorField(err))
		configValidator = nil
	}

	// Load configuration from each .mlcm directory (later ones override earlier)
	// We load in reverse order so project-local config takes precedence
	for i := len(mlcmPaths) - 1; i >= 0; i-- {
		mlcmPath := mlcmPaths[i]
		configPath := filepath.Join(mlcmPath, ConfigFileName+".yaml")

		// Read config file for validation
		data, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			logging.L().Warn("failed to read config file",
				logging.FilePath(configPath),
				logging.ErrorField(err))
			continue
		}

		// Validate against schema before parsing
		if configValidator != nil {
			if err := configValidator.ValidateBytes(data); err != nil {
				return nil, fmt.Errorf("config validation failed at %s: %w", configPath, err)
			}
		}

		v := viper.New()
		v.SetConfigFile(configPath)
		v.SetConfigType("yaml")

		if err := v.ReadInConfig(); err != nil {
			// Config file is optional, continue if not found
			if _, ok := err.(viper.ConfigFileNotFoundError); ok {
				continue
			}
			if os.IsNotExist(err) {
				continue
			}
			// Log warning for other read errors (permissions, malformed YAML, etc.)
			logging.L().Warn("failed to read config file",
				logging.FilePath(configPath),
				logging.ErrorField(err))
			continue
		}

		// Unmarshal into config
		if err := v.Unmarshal(cfg); err != nil {
			logging.L().Warn("failed to parse config file",
				logging.FilePath(configPath),
				logging.ErrorField(err))
			return nil, fmt.Errorf("failed to parse config at %s: %w", configPath, err)
		}

		logging.L().Debug(logging.MsgConfigLoaded, logging.FilePath(configPath))
	}

	return cfg, nil
}

// findMLCMDirs locates all .mlcm directories by walking up from pwd.
// Only project directories are searched; ~/.mlcm is not included.
func findMLCMDirs() ([]string, error) {
	var dirs []string
	seen := make(map[string]bool)

	// Walk up from current directory
	pwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	current := pwd
	for {
		mlcmPath := filepath.Join(current, MLCMDirName)
		if info, err := os.Stat(mlcmPath); err == nil && info.IsDir() {
			absPath, _ := filepath.Abs(mlcmPath)
			if !seen[absPath] {
				dirs = append(dirs, absPath)
				seen[absPath] = true
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			break // Reached root
		}
		current = parent
	}

	return dirs, nil
}

// GetFragmentDirs returns all context-fragments directories in priority order.
func (c *Config) GetFragmentDirs() []string {
	var dirs []string
	for _, mlcmPath := range c.MLCMPaths {
		fragDir := filepath.Join(mlcmPath, ContextFragmentsDir)
		if info, err := os.Stat(fragDir); err == nil && info.IsDir() {
			dirs = append(dirs, fragDir)
		}
	}
	return dirs
}

// GetPromptDirs returns all prompts directories in priority order.
func (c *Config) GetPromptDirs() []string {
	var dirs []string
	for _, mlcmPath := range c.MLCMPaths {
		promptDir := filepath.Join(mlcmPath, PromptsDir)
		if info, err := os.Stat(promptDir); err == nil && info.IsDir() {
			dirs = append(dirs, promptDir)
		}
	}
	return dirs
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
// Uses the closest project .mlcm directory.
func (c *Config) GetConfigFilePath() (string, error) {
	if len(c.MLCMPaths) == 0 {
		return "", fmt.Errorf("no .mlcm directory found; run 'mlcm init --local' first")
	}
	return filepath.Join(c.MLCMPaths[0], ConfigFileName+".yaml"), nil
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

// LoadHomeConfig loads configuration from ~/.mlcm/config.yaml if it exists.
// Returns nil config (not error) if home config doesn't exist.
func LoadHomeConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configPath := filepath.Join(home, MLCMDirName, ConfigFileName+".yaml")
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
