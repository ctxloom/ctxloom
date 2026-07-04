package config

import (
	"slices"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"gopkg.in/yaml.v3"
)

// LLMConfig is the backend-agnostic envelope for one labeled LLM config entry.
// Type is the discriminator naming the backend implementation (claude-code /
// antigravity / codex); Body carries that backend's own type-specific fields,
// decoded and validated by the backend registry — the config package never
// imports backend structs. The map label that keys this entry is an arbitrary
// user string with zero semantics; the backend is determined ONLY by Type.
type LLMConfig struct {
	Type string `mapstructure:"type" yaml:"type,omitempty"`
	// Role marks an entry as its backend type's default primary or fast pick in
	// the shipped registry. It is registry-only metadata: init reads it to wire a
	// freshly-selected engine, and the persist path strips it so user configs
	// stay plain {type, model}. It never affects runtime label resolution.
	Role string `mapstructure:"role" yaml:"role,omitempty"`
	// Permissions is this backend's launch-time permission posture
	// (default|acceptEdits|plan|bypass). Empty defers to the resolver's built-in
	// default (claude-code → bypass, others → default). An agent binding and the
	// `run --permissions` flag override it.
	Permissions string         `mapstructure:"permissions" yaml:"permissions,omitempty"`
	Body        map[string]any `mapstructure:",remain" yaml:",inline"`
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

// EffectiveType returns the backend type the entry drives, degrading to
// DefaultLLM when Type is unset. Every consumer of LLMConfig.Type must
// resolve it through this method so the defaulting rule lives in one place.
func (c LLMConfig) EffectiveType() string {
	if c.Type == "" {
		return DefaultLLM
	}
	return c.Type
}

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
	// Version is the optional pinned content version ("@<commit>") this ref
	// resolves to. It is populated transiently during assembly resolution (from
	// a ref's "@<commit>" suffix or from bundle expansion), never serialized:
	// Name stays the version-agnostic canonical identity so dedup/exclusion/
	// ordering and the lockfile remain version-agnostic, and only the read path
	// honors Version. The authored "@<commit>" lives in the stored ref string.
	Version string `yaml:"-"`
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
func (f FragmentRef) MarshalYAML() (any, error) {
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
	Description string        `mapstructure:"description" yaml:"description,omitempty"`
	LLM         string        `mapstructure:"llm" yaml:"llm,omitempty"`                   // Preferred config label/backend (overridable by -l)
	Parents     []string      `mapstructure:"parents" yaml:"parents,omitempty"`           // Parent profiles to inherit from
	Tags        []string      `mapstructure:"tags" yaml:"tags,omitempty"`                 // Fragment tags to include
	Bundles     []string      `mapstructure:"bundles" yaml:"bundles,omitempty"`           // Bundle references (e.g., "remote/go-tools")
	BundleItems []string      `mapstructure:"bundle_items" yaml:"bundle_items,omitempty"` // Cherry-pick items (e.g., "remote/bundle:fragments/name")
	Fragments   []FragmentRef `mapstructure:"fragments" yaml:"fragments,omitempty"`       // Fragment references with optional priority
	// Prompts curates the slash-command prompt exports for this profile. When a
	// resolved active profile declares a NON-EMPTY list, ONLY these prompts are
	// exported (each optionally version-pinned with a trailing "@<commit>"),
	// suppressing the global flag-based auto-export for that profile; an empty
	// list keeps today's global auto-export (opt-in). Each entry is a prompt ref
	// ("<bundle>#prompts/<name>") whose version-agnostic identity is the stored
	// string — like bundle_items, any "@<commit>" is parsed transiently at
	// assembly and the lockfile stays untouched.
	Prompts   []string          `mapstructure:"prompts" yaml:"prompts,omitempty"`
	Variables map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Hooks     wire.HooksConfig  `mapstructure:"hooks" yaml:"hooks,omitempty"` // Hooks for this profile (inherited)
	MCP       wire.MCPConfig    `mapstructure:"mcp" yaml:"mcp,omitempty"`     // MCP servers for this profile (inherited)

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

	// inheritedDefaults holds defaults inherited from the HOME config for this
	// run only (operations.EnsureDefaultProfiles). They are deliberately
	// unexported and yaml-invisible: GetDefaultProfiles consults them as a
	// fallback, but Save never persists them and ExplicitDefaultProfiles never
	// reports them — otherwise an unrelated cfg.Save() would silently
	// materialize home defaults into the project config.yaml, and a run-only
	// inheritance would suppress first-profile auto-promotion.
	inheritedDefaults []string
	// inheritedResolved records that the home-defaults lookup already ran this
	// run (even if it found none), so repeated EnsureDefaultProfiles calls are
	// read-only — concurrent profile assembly (operations.MapProfiles) relies
	// on this after the main goroutine resolves once.
	inheritedResolved bool
}

// SetInheritedDefaults records run-only defaults inherited from the home
// config (pass nil to record "looked, found none"). See inheritedDefaults.
func (p *ProfilesConfig) SetInheritedDefaults(names []string) {
	p.inheritedDefaults = append([]string(nil), names...)
	p.inheritedResolved = true
}

// InheritedDefaults returns the run-only defaults inherited from the home
// config, or nil when none were inherited.
func (p *ProfilesConfig) InheritedDefaults() []string {
	return p.inheritedDefaults
}

// InheritedDefaultsResolved reports whether the home-defaults inheritance
// lookup already ran this run.
func (p *ProfilesConfig) InheritedDefaultsResolved() bool {
	return p.inheritedResolved
}

// SettingsConfig holds misc behavioral settings (mapstructure key "config").
type SettingsConfig struct {
	UseDistilled     *bool `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"`         // Prefer .distilled.md versions (default true)
	CompactionChunks int   `mapstructure:"compaction_chunks" yaml:"compaction_chunks,omitempty"` // Target tokens per compaction chunk (default 8000)
	Statusline       *bool `mapstructure:"statusline" yaml:"statusline,omitempty"`               // Manage the ctxloom HUD statusline (default true)
}

// hasAny reports whether any setting is set, so Save can prune the block when
// empty. It MUST cover every field, or setting only an uncovered field would
// silently drop the whole block on the next Save.
func (s SettingsConfig) hasAny() bool {
	return s.UseDistilled != nil || s.CompactionChunks > 0 || s.Statusline != nil
}

// ShouldUseDistilled returns whether to prefer distilled versions of
// fragments/prompts. Defaults to true if not explicitly set.
func (s *SettingsConfig) ShouldUseDistilled() bool {
	if s == nil || s.UseDistilled == nil {
		return true
	}
	return *s.UseDistilled
}

// ShouldManageStatusline reports whether ctxloom should install and maintain its
// HUD statusline. Defaults to true; set statusline:false in config to opt out and
// keep your own (or no) statusline.
func (s *SettingsConfig) ShouldManageStatusline() bool {
	if s == nil || s.Statusline == nil {
		return true
	}
	return *s.Statusline
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
	return slices.Contains(p.Defaults, name)
}

// SetExclusiveDefaultProfile makes name the sole default, dropping any others in
// one step. Returns true if the default set changed (false when name was already
// the only default).
func (p *ProfilesConfig) SetExclusiveDefaultProfile(name string) bool {
	if len(p.Defaults) == 1 && p.Defaults[0] == name {
		return false
	}
	p.Defaults = []string{name}
	return true
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
