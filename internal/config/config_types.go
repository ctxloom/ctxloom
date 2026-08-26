package config

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"gopkg.in/yaml.v3"
)

// LLMConfig is the backend-agnostic envelope for one labeled LLM config entry.
// Type is the discriminator naming the backend implementation (claude-code /
// codex / kiro); Body carries that backend's own type-specific fields,
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

// BackendClaudeCode is the claude-code backend type — the reference engine with
// ambient host auth, and the subject of the host-bypass permission stopgap.
// Compare a backend type against THIS (not DefaultLLM) when the intent is
// "is this claude-code specifically", so the check doesn't silently follow a
// change to the default-label concept.
const BackendClaudeCode = "claude-code"

// DefaultLLM is the backend type used when no config resolves a label. It is
// claude-code today; DefaultLLM and BackendClaudeCode name distinct concepts
// (the default fallback vs. the claude-code engine) that happen to coincide.
const DefaultLLM = BackendClaudeCode

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
		// A null/empty scalar ("- " on its own line, `- ""`, or whitespace)
		// would otherwise decode to FragmentRef{Name: ""} and flow silently
		// through the resolver to select nothing (U049-F19). An empty entry is
		// a typo, never an intent — reject it at decode with the line named.
		if strings.TrimSpace(node.Value) == "" {
			return fmt.Errorf("empty fragment reference at line %d: a fragments list entry must name a fragment", node.Line)
		}
		f.Name = node.Value
		f.Priority = 0
		return nil
	}
	// Struct format
	type plain FragmentRef
	if err := node.Decode((*plain)(f)); err != nil {
		return err
	}
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("empty fragment reference at line %d: a fragments list entry must name a fragment", node.Line)
	}
	return nil
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
	Tags        []string      `mapstructure:"tags" yaml:"tags,omitempty"`                 // Descriptive tags (listing/discovery only; NOT content-selecting)
	SelectTags  []string      `mapstructure:"select_tags" yaml:"select_tags,omitempty"`   // Fragment tags to select content by
	Bundles     []string      `mapstructure:"bundles" yaml:"bundles,omitempty"`           // Bundle references (e.g., "remote/go-tools")
	BundleItems []string      `mapstructure:"bundle_items" yaml:"bundle_items,omitempty"` // Cherry-pick items (e.g., "remote/bundle:fragments/name")
	Fragments   []FragmentRef `mapstructure:"fragments" yaml:"fragments,omitempty"`       // Fragment references with optional priority
	// Commands curates the slash-command exports for this profile (D2: renamed
	// from prompts). When a resolved active profile declares a NON-EMPTY list,
	// ONLY these commands are exported (each optionally version-pinned with a
	// trailing "@<commit>"), suppressing the global flag-based auto-export for
	// that profile; an empty list keeps today's global auto-export (opt-in).
	// Each entry is a command ref ("<bundle>#commands/<name>") whose
	// version-agnostic identity is the stored string — like bundle_items, any
	// "@<commit>" is parsed transiently at assembly and the lockfile stays
	// untouched.
	Commands []string `mapstructure:"commands" yaml:"commands,omitempty"`
	// Skills curates the Agent Skill exports for this profile (skill/command
	// split plan §3.2), mirroring Commands: when a resolved active profile
	// declares a NON-EMPTY list, ONLY these skills are exported per-engine
	// (force-enabled), suppressing the global bundle-wide auto-export
	// (config.ResolveBundleSkills) for that profile; an empty list keeps
	// today's global auto-export (every profile-referenced bundle's skills,
	// each still gated by its own per-engine enablement). Each entry is a
	// skill ref ("<bundle>#skills/<name>") — no version pin (a skill carries
	// no historical-content resolution the way a command's "@<commit>" does).
	Skills    []string          `mapstructure:"skills" yaml:"skills,omitempty"`
	Variables map[string]string `mapstructure:"variables" yaml:"variables,omitempty"`
	Hooks     wire.HooksConfig  `mapstructure:"hooks" yaml:"hooks,omitempty"` // Hooks for this profile (inherited)

	// Exclusions - items to filter out after inheritance resolution
	ExcludeFragments []string `mapstructure:"exclude_fragments" yaml:"exclude_fragments,omitempty"`
	ExcludeMCP       []string `mapstructure:"exclude_mcp" yaml:"exclude_mcp,omitempty"`

	// DenyTools names per-engine tool identifiers this profile denies at
	// launch (e.g. "Task" for Claude Code's built-in sub-agent tool, which
	// ctxloom cannot mediate — it spawns children IN-PROCESS that inherit the
	// coordinator's system prompt rather than going through ctxloom's own
	// agent_run path). Currently reaches only the claude-code backend's
	// settings.json permissions.deny (the one engine with a native per-tool
	// deny surface); other backends silently ignore it. Accumulates through
	// profile inheritance exactly like ExcludeMCP — a child cannot un-deny
	// what a parent denied, and it is safety-only (never gated by the
	// executable trust gate: a deny entry can only make a run MORE
	// restrictive, never execute anything).
	DenyTools []string `mapstructure:"deny_tools" yaml:"deny_tools,omitempty"`
}

// UnmarshalYAML decodes a profile and then backstops the fragments list against
// empty entries (U049-F19). FragmentRef.UnmarshalYAML rejects an empty or
// whitespace scalar, but yaml.v3 never invokes a value's Unmarshaler for a NULL
// node — a bare "- " list item decodes straight to the zero FragmentRef,
// bypassing that check — so an empty-named fragment is caught here after the
// whole profile has decoded, where the null slot is visible as Name == "".
func (p *Profile) UnmarshalYAML(node *yaml.Node) error {
	type plain Profile
	if err := node.Decode((*plain)(p)); err != nil {
		return err
	}
	// Inspect the RAW fragments sequence: yaml.v3 silently DROPS a null list
	// item ("- ") during sequence decode (its Unmarshaler is never called for a
	// null node), so an empty entry never reaches p.Fragments to be caught
	// there. Walk the source nodes so the typo fails loudly instead of vanishing.
	return profiles.RefuseEmptyFragmentEntries(node)
}

// SettingsConfig holds misc behavioral settings (mapstructure key "config").
type SettingsConfig struct {
	UseDistilled     *bool `mapstructure:"use_distilled" yaml:"use_distilled,omitempty"`         // Prefer .distilled.md versions (default true)
	CompactionChunks int   `mapstructure:"compaction_chunks" yaml:"compaction_chunks,omitempty"` // Target tokens per compaction chunk (default 8000)
	Statusline       *bool `mapstructure:"statusline" yaml:"statusline,omitempty"`               // Manage the ctxloom HUD statusline (default true)
	// ToolReflectBytes is the tool-result size, in bytes, at or above which the
	// PostToolUse reflect hook fires. Distillation reduces a tool result to its
	// SHAPE, so whatever the agent does not say it learned is not recoverable
	// from the essence. Firing on every call would cost more than the bodies it
	// replaces; firing only on large results targets where the loss actually
	// is. 0 means the default (see Config.GetToolReflectBytes); negative
	// disables the hook.
	ToolReflectBytes int `mapstructure:"tool_reflect_bytes" yaml:"tool_reflect_bytes,omitempty"`
	// EssenceMaxChars is the target size of a finished session essence. 0 means
	// the default (see Config.GetEssenceMaxChars). The essence is re-injected
	// into a fresh context window on resume, so this trades detail retained
	// against context spent on every resume.
	EssenceMaxChars int `mapstructure:"essence_max_chars" yaml:"essence_max_chars,omitempty"`
	// SilenceUnsupported suppresses capability-loss reporting: the "NOT
	// carried" lines naming what the selected engine has no structural place
	// for. Default false, because a loss the user's own bundles asked for is
	// worth hearing once. Set true when the answer is known and the line is
	// just noise -- someone who runs opencode deliberately and does not want
	// to be told it has no hook mechanism on every check.
	//
	// It silences only DECLARED losses; ctxloom's own machinery is already
	// excluded upstream (ManagedHooks.WireDeclared).
	SilenceUnsupported bool        `mapstructure:"silence_unsupported" yaml:"silence_unsupported,omitempty"`
	Sign               *SignConfig `mapstructure:"sign" yaml:"sign,omitempty"` // Publisher-signing defaults (spec §7A.3)
}

// SignConfig holds the publisher-signing defaults (signature-envelope spec
// §7A.3): `sign.default` makes signing ride every push the way `git commit
// -S` rides every commit ("the best signing ceremony is the one that
// already happened"), and `sign.key` pins the explicit key/fingerprint the
// zero-config discovery chain (internal/signing/agentkey) should use when
// set, overriding git config user.signingkey and ssh-agent auto-detection.
type SignConfig struct {
	// Default: when true, `fragment push`/`command push` sign unless --no-sign
	// is given. Defaults to false — signing must be opted into, exactly like
	// git commit -S is opt-in until gpg.commit.sign flips it.
	Default bool `mapstructure:"default" yaml:"default,omitempty"`
	// Key is an explicit --key-equivalent: a SHA256:... fingerprint, a path
	// to a public key, or a ssh-agent key's comment/name (matched
	// case-insensitively, substring OK — the name printed in the
	// "multiple keys in ssh-agent" error). Empty means "use the zero-config
	// discovery chain" (git config user.signingkey, then the sole ssh-agent
	// identity).
	Key string `mapstructure:"key" yaml:"key,omitempty"`
}

// hasAny reports whether any setting is set, so Save can prune the block when
// empty. It MUST cover every field, or setting only an uncovered field would
// silently drop the whole block on the next Save.
func (s SettingsConfig) hasAny() bool {
	return s.UseDistilled != nil || s.CompactionChunks > 0 || s.Statusline != nil || s.Sign != nil || s.ToolReflectBytes != 0 || s.EssenceMaxChars != 0 || s.SilenceUnsupported
}

// ShouldSignByDefault reports whether publish commands should sign unless
// --no-sign is given (spec §7A.3, `sign.default`). Defaults to false.
func (s *SettingsConfig) ShouldSignByDefault() bool {
	if s == nil || s.Sign == nil {
		return false
	}
	return s.Sign.Default
}

// SignKey returns the configured sign.key override, or "" when unset.
func (s *SettingsConfig) SignKey() string {
	if s == nil || s.Sign == nil {
		return ""
	}
	return s.Sign.Key
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
