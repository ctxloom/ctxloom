package config

import "gopkg.in/yaml.v3"

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
	SCM     string `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`                      // Hash identifying ctxloom-managed hooks
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
	Unified UnifiedHooks            `mapstructure:"unified" yaml:"unified,omitempty"`
	Plugins map[string]BackendHooks `mapstructure:"plugins" yaml:"plugins,omitempty"`
}

// hasAny reports whether any hook is configured. Used by Save() to decide
// whether to emit the `hooks` key at all (vs. delete it from the file).
func (h HooksConfig) hasAny() bool {
	u := h.Unified
	if len(u.PreTool)+len(u.PostTool)+len(u.SessionStart)+len(u.SessionEnd)+len(u.PreShell)+len(u.PostFileEdit) > 0 {
		return true
	}
	for _, backend := range h.Plugins {
		for _, hooks := range backend {
			if len(hooks) > 0 {
				return true
			}
		}
	}
	return false
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
	Command      string            `mapstructure:"command" yaml:"command" json:"command"`                                    // Command to execute
	Args         []string          `mapstructure:"args" yaml:"args,omitempty" json:"args,omitempty"`                         // Command arguments
	Env          map[string]string `mapstructure:"env" yaml:"env,omitempty" json:"env,omitempty"`                            // Environment variables
	Notes        string            `mapstructure:"notes" yaml:"notes,omitempty" json:"notes,omitempty"`                      // Human-readable notes, not sent to AI
	Installation string            `mapstructure:"installation" yaml:"installation,omitempty" json:"installation,omitempty"` // Setup/installation instructions, not sent to AI
	SCM          string            `yaml:"_ctxloom,omitempty" json:"_ctxloom,omitempty"`                                     // Marker for ctxloom-managed servers
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

// LLMConfig holds configuration for a specific LLM backend.
type LLMConfig struct {
	Model      string            `mapstructure:"model" yaml:"model,omitempty"` // Default model for this LLM
	BinaryPath string            `mapstructure:"binary_path" yaml:"binary_path,omitempty"`
	Args       []string          `mapstructure:"args" yaml:"args,omitempty"`
	Env        map[string]string `mapstructure:"env" yaml:"env,omitempty"`
}

// LMConfig holds all large-language-model configuration: the default LLM and
// model, per-LLM overrides, and session-compaction settings.
type LMConfig struct {
	Default    string               `mapstructure:"default" yaml:"default,omitempty"`       // Default LLM name (e.g., "claude-code", "gemini")
	Model      string               `mapstructure:"model" yaml:"model,omitempty"`           // Default model (e.g., "opus", "sonnet")
	Configs    map[string]LLMConfig `mapstructure:"configs" yaml:"configs,omitempty"`       // Per-LLM overrides, keyed by name
	Compaction CompactionConfig     `mapstructure:"compaction" yaml:"compaction,omitempty"` // Session-compaction LLM settings
}

// CompactionConfig holds the LLM settings used for session compaction.
type CompactionConfig struct {
	LLM    string `mapstructure:"llm" yaml:"llm,omitempty"`       // LLM for compaction (default: the default LLM)
	Model  string `mapstructure:"model" yaml:"model,omitempty"`   // Model for compaction (default: "haiku")
	Chunks int    `mapstructure:"chunks" yaml:"chunks,omitempty"` // Target tokens per chunk (default: 8000)
}

// DefaultLLM is the LLM used when none is configured.
const DefaultLLM = "claude-code"

// GetDefaultLLM returns the configured default LLM, or DefaultLLM if unset.
func (c *LMConfig) GetDefaultLLM() string {
	if c != nil && c.Default != "" {
		return c.Default
	}
	return DefaultLLM
}

// GetConfiguredLLMs returns the names of Configs with explicit config.
// If none are configured, returns the default LLM.
func (c *LMConfig) GetConfiguredLLMs() []string {
	if len(c.Configs) == 0 {
		return []string{c.GetDefaultLLM()}
	}
	var names []string
	for name := range c.Configs {
		names = append(names, name)
	}
	return names
}

// GetDefaultModel returns the per-LLM default model for the named LLM.
// Returns empty string if no override is configured.
func (c *LMConfig) GetDefaultModel(llmName string) string {
	if cfg, ok := c.Configs[llmName]; ok {
		return cfg.Model
	}
	return ""
}

// FragmentRef references a fragment with optional priority for context ordering.
// Higher priority fragments are placed at the beginning/end of context (bookend strategy)
// to address the "lost in the middle" problem where Configs poorly attend to middle content.
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
	Description string            `mapstructure:"description" yaml:"description,omitempty"`
	Parents     []string          `mapstructure:"parents" yaml:"parents,omitempty"`           // Parent profiles to inherit from
	Tags        []string          `mapstructure:"tags" yaml:"tags,omitempty"`                 // Fragment tags to include
	Bundles     []string          `mapstructure:"bundles" yaml:"bundles,omitempty"`           // Bundle references (e.g., "remote/go-tools")
	BundleItems []string          `mapstructure:"bundle_items" yaml:"bundle_items,omitempty"` // Cherry-pick items (e.g., "remote/bundle:fragments/name")
	Fragments   []FragmentRef     `mapstructure:"fragments" yaml:"fragments,omitempty"`       // Fragment references with optional priority
	Variables   map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Hooks       HooksConfig       `mapstructure:"hooks" yaml:"hooks,omitempty"`             // Hooks for this profile (inherited)
	MCP         MCPConfig         `mapstructure:"mcp" yaml:"mcp,omitempty"`                 // MCP servers for this profile (inherited)

	// Exclusions - items to filter out after inheritance resolution
	ExcludeFragments []string `mapstructure:"exclude_fragments" yaml:"exclude_fragments,omitempty"`
	ExcludeMCP       []string `mapstructure:"exclude_mcp" yaml:"exclude_mcp,omitempty"`
}

// Defaults holds default settings applied when no explicit values are specified.
type Defaults struct {
	Profiles     []string `mapstructure:"profiles" yaml:"profiles,omitempty"`          // Default profiles to load (supports multiple)
	UseDistilled *bool    `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"` // Prefer .distilled.md versions (default true)
}

// hasAny reports whether any default is set. Save uses this to decide whether
// to persist the `defaults` block; it MUST cover every field, or setting only
// an uncovered field would silently drop the whole block on the next Save.
func (d Defaults) hasAny() bool {
	return len(d.Profiles) > 0 ||
		d.UseDistilled != nil
}

// SyncConfig holds configuration for dependency sync behavior.
type SyncConfig struct {
	// AutoSync enables automatic sync of remote dependencies on startup.
	// Defaults to true if not specified.
	AutoSync *bool `mapstructure:"auto_sync" yaml:"auto_sync,omitempty"`
}

// ShouldAutoSync returns whether to auto-sync dependencies on startup.
// Defaults to true if not explicitly set.
func (s *SyncConfig) ShouldAutoSync() bool {
	if s == nil || s.AutoSync == nil {
		return true
	}
	return *s.AutoSync
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
