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

// LLMConfig is the backend-agnostic envelope for one labeled LLM config entry.
// Type is the discriminator naming the backend implementation (claude-code /
// gemini / codex); Body carries that backend's own type-specific fields,
// decoded and validated by the backend registry — the config package never
// imports backend structs. The map label that keys this entry is an arbitrary
// user string with zero semantics; the backend is determined ONLY by Type.
type LLMConfig struct {
	Type string                 `mapstructure:"type" yaml:"type,omitempty"`
	Body map[string]interface{} `mapstructure:",remain" yaml:",inline"`
}

// RoleDefaults maps a role to the config label that plays it. Roles select
// which labeled config serves which part; labels are looked up verbatim in
// LMConfig.Configs. "primary" is the coding/interactive role; "fast" is the
// compression role (distillation, session compaction).
type RoleDefaults struct {
	Primary string `mapstructure:"primary" yaml:"primary,omitempty"`
	Fast    string `mapstructure:"fast" yaml:"fast,omitempty"`
}

// LMConfig holds all large-language-model configuration: a registry of
// arbitrarily-labeled, fully-specified backend configs and a role→label map.
type LMConfig struct {
	Configs  map[string]LLMConfig `mapstructure:"configs" yaml:"configs,omitempty"` // labeled backend configs
	Defaults RoleDefaults         `mapstructure:"defaults" yaml:"defaults,omitempty"`
}

// DefaultLLM is the backend type used when no config resolves a label.
const DefaultLLM = "claude-code"

// hasAny reports whether the LM config carries anything worth persisting.
func (c LMConfig) hasAny() bool {
	return len(c.Configs) > 0 || c.Defaults.Primary != "" || c.Defaults.Fast != ""
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
	Hooks       HooksConfig       `mapstructure:"hooks" yaml:"hooks,omitempty"` // Hooks for this profile (inherited)
	MCP         MCPConfig         `mapstructure:"mcp" yaml:"mcp,omitempty"`     // MCP servers for this profile (inherited)

	// Exclusions - items to filter out after inheritance resolution
	ExcludeFragments []string `mapstructure:"exclude_fragments" yaml:"exclude_fragments,omitempty"`
	ExcludeMCP       []string `mapstructure:"exclude_mcp" yaml:"exclude_mcp,omitempty"`
}

// ProfilesConfig holds the default-profile list and the named profile
// definitions. Defaults was the old top-level defaults.profiles array;
// Definitions was the old root-level profiles map.
type ProfilesConfig struct {
	Defaults    []string           `mapstructure:"defaults" yaml:"defaults,omitempty"`       // Default profiles to load (supports multiple)
	Definitions map[string]Profile `mapstructure:"definitions" yaml:"definitions,omitempty"` // Named profile definitions
}

// SettingsConfig holds misc behavioral settings (mapstructure key "config").
type SettingsConfig struct {
	UseDistilled     *bool `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"`         // Prefer .distilled.md versions (default true)
	CompactionChunks int   `mapstructure:"compaction_chunks" yaml:"compaction_chunks,omitempty"` // Target tokens per compaction chunk (default 8000)
}

// hasAny reports whether any setting is set, so Save can prune the block when
// empty. It MUST cover every field, or setting only an uncovered field would
// silently drop the whole block on the next Save.
func (s SettingsConfig) hasAny() bool {
	return s.UseDistilled != nil || s.CompactionChunks > 0
}

// ShouldUseDistilled returns whether to prefer distilled versions of
// fragments/prompts. Defaults to true if not explicitly set.
func (s *SettingsConfig) ShouldUseDistilled() bool {
	if s == nil || s.UseDistilled == nil {
		return true
	}
	return *s.UseDistilled
}

// hasAny reports whether any profile config is set, so Save can prune the block.
func (p ProfilesConfig) hasAny() bool {
	return len(p.Defaults) > 0 || len(p.Definitions) > 0
}

// AddDefaultProfile adds a profile to the defaults list if not already present.
func (p *ProfilesConfig) AddDefaultProfile(name string) bool {
	if p.IsDefaultProfile(name) {
		return false
	}
	p.Defaults = append(p.Defaults, name)
	return true
}

// RemoveDefaultProfile removes a profile from the defaults list.
// Returns true if the profile was removed, false if it wasn't present.
func (p *ProfilesConfig) RemoveDefaultProfile(name string) bool {
	for i, name2 := range p.Defaults {
		if name2 == name {
			p.Defaults = append(p.Defaults[:i], p.Defaults[i+1:]...)
			return true
		}
	}
	return false
}

// IsDefaultProfile checks if a profile is in the defaults list.
func (p *ProfilesConfig) IsDefaultProfile(name string) bool {
	for _, name2 := range p.Defaults {
		if name2 == name {
			return true
		}
	}
	return false
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
