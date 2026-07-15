package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/resources"
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
	Version  int              `mapstructure:"version" yaml:"version"` // config schema version (integer; distinct from app version)
	LM       LMConfig         `mapstructure:"llm" yaml:"llm"`
	Editor   EditorConfig     `mapstructure:"editor" yaml:"editor,omitempty"`
	Settings SettingsConfig   `mapstructure:"config" yaml:"config,omitempty"`
	Sync     SyncConfig       `mapstructure:"sync" yaml:"sync,omitempty"`
	Hooks    wire.HooksConfig `mapstructure:"hooks" yaml:"hooks,omitempty"`
	MCP      wire.MCPConfig   `mapstructure:"mcp" yaml:"mcp,omitempty"`
	Profiles ProfilesConfig   `mapstructure:"profiles" yaml:"profiles,omitempty"`
	// Agents is the LOCAL-ONLY engine↔profile binding map, the in-config half
	// of the agent entity (the other half is .ctxloom/agents/*.yaml). Keyed
	// by agent name. It is NEVER a bundle item kind and NEVER remote — there is
	// no Bundle.Agents and no remote path. Read the merged set via
	// LoadAgents / Agent, which folds in the directory source too.
	Agents map[string]agents.Agent `mapstructure:"agents" yaml:"agents,omitempty"`
	// DefaultAgent names the always-bound default agent: the key in Agents (or a
	// .ctxloom/agents/*.yaml file) whose binding a bare `ctxloom run` (no --agent,
	// no -p/-f/-t) resolves — its composed profiles become the context and its
	// engine + runtime + permissions the transport. It replaces the retired
	// profiles.defaults: "the default profile set" is now whatever this agent
	// composes (DefaultAgentProfiles). Empty or naming an undefined agent degrades
	// to empty context (a warning, never a hard stop — CLAUDE.md fault tolerance).
	DefaultAgent string `mapstructure:"default_agent" yaml:"default_agent,omitempty"`
	// Workspace is the project-wide DEFAULT for the SESSION-level workspace
	// axis (none | worktree): where a session's working directory lives.
	// Empty means "none" (the shared live project dir — today's behaviour).
	// A session-creating invocation (run/map/weave `--workspace`) overrides
	// it per session. Deliberately NOT an agent trait: needing a private cwd
	// is a property of how a session is launched, not of who the agent is.
	Workspace string `mapstructure:"workspace" yaml:"workspace,omitempty"`
	// Runtime is the project-wide DEFAULT for the AGENT-level runtime axis
	// (host | container): where an agent's engine process executes. Empty
	// means "host". An agent binding's own `runtime:` overrides it; the
	// precedence (agent → this default → host) is resolved in
	// operations.resolveAgentBinding. The two axes are independent and meet
	// only at launch (isolation.Axes).
	Runtime string `mapstructure:"runtime" yaml:"runtime,omitempty"`
	// IsolationImages maps a backend name (claude-code | kiro | ...) to a
	// USER-PROVIDED agent image for containerized runs. An entry overrides the
	// built-in per-backend default tag and is run AS-IS: never locally built or
	// overlaid (the user owns it), so an absent override degrades with a warning
	// instead of triggering the on-the-fly build. Missing entries keep the
	// built-in default (which IS auto-built when absent).
	IsolationImages map[string]string `mapstructure:"isolation_images" yaml:"isolation_images,omitempty"`
	// IsolationBaseContainerfile is a USER-PROVIDED base Containerfile for
	// locally-built agent images: the on-the-fly build (and `ctxloom container
	// build`) layers the engine's agent stage onto a base built from this file
	// instead of the embedded default base (container/base/Containerfile).
	// Relative paths resolve against the project root.
	IsolationBaseContainerfile string `mapstructure:"isolation_base_containerfile" yaml:"isolation_base_containerfile,omitempty"`
	// UI configures the interactive-run terminal layer (the prefix-key viewer
	// and the persistent surround bar). Flag/env never lives here — only
	// presentation preferences; `run --plain-terminal` disables the layer
	// entirely regardless of this section.
	UI UIConfig `mapstructure:"ui" yaml:"ui,omitempty"`

	// Runtime-only fields: populated during Load, never part of the persisted
	// config. yaml:"-" keeps them out of every marshal — notably `config show`,
	// which would otherwise dump resolved paths, load warnings, and (worst) the
	// PendingUpgrade's raw []byte config as an integer array.
	AppPaths []string     `yaml:"-"` // Resolved .ctxloom directory (at most one)
	AppRoot  string       `yaml:"-"` // Project root (parent of .ctxloom directory)
	AppDir   string       `yaml:"-"` // Full path to the .ctxloom directory
	Source   ConfigSource `yaml:"-"` // Where the configuration was loaded from
	Warnings []Warning    `yaml:"-"` // Kind-tagged warnings collected during load

	// PendingUpgrade is set when Load upgraded an older on-disk schema to the
	// current one in memory. The upgraded bytes are NOT persisted automatically;
	// an interactive caller may prompt the user and call CommitUpgrade. Nil when
	// the file was already current.
	PendingUpgrade *upgrade.Pending `yaml:"-"`

	fs afero.Fs // Filesystem for file operations (nil = OS filesystem)

	// execGate gates the bundle EXECUTABLE surfaces (bundle MCP servers + bundle
	// hooks resolved by ResolveBundleMCPServers/ResolveBundleHooks, and prompt
	// command-file exports via LoadCommandExports) when set. nil = no enforcement,
	// matching the gate-free management/listing paths. The operations/run
	// consumers inject it before writing backend settings (trust rework, TR5);
	// operations can't be imported here, so the gate is a plain bundles.ContentGate
	// func. Never persisted.
	execGate bundles.ContentGate

	// companionSeed memoizes companionBundleSeed (ProbeCompanionLoadouts) for
	// this Config's LIFETIME: probing execs a subprocess per discovered
	// companion, and SeededBundleLoader is called repeatedly within one
	// process (hooks, MCP, fragments, assembly) — without this, each call
	// would re-pay that cost. Deliberately per-Config (not a package var):
	// tests construct fresh Configs and must never observe another test's
	// fake companion output.
	//
	// Held by POINTER, not as a value sync.Once field: Config is copied by
	// value in existing callers (e.g. table-driven tests ranging over a
	// []struct{... config Config ...}), and a struct containing a sync.Once
	// value must never be copied (govet copylocks) — a value field here
	// would break every one of those callers. companionSeedInitMu (a
	// package-level lock, not a Config field, so copying Config is still
	// cheap and safe) guards the lazy allocation of the pointer; the actual
	// probe is still memoized exactly once via the pointee's sync.Once.
	companionSeed *companionSeedState

	// companionProbe overrides companion-loadout discovery; nil means the real
	// ProbeCompanionLoadouts. The real probe execs whatever companion binaries
	// happen to be on the HOST's PATH, so any test that sets AppPaths (the only
	// guard) silently inherits the developer's machine: the same test passes
	// where ltk is not installed and fails where it is. Tests that assert on an
	// exact command/bundle set must pin this (see DisableCompanionProbe) so the
	// result depends on the fixture, never the host.
	companionProbe func(signing.TrustRoot) map[string]*bundles.Bundle

	// lmDefaultOverlay snapshots what mergeDefaultConfig overlaid into LM (nil
	// when the user configured their own registry). Save strips values that
	// still match it: the overlay is a runtime fallback, and persisting it
	// would pin the user to a snapshot of shipped model defaults.
	lmDefaultOverlay *LMConfig
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

// UIConfig holds the interactive-run terminal-layer preferences.
type UIConfig struct {
	// PrefixKey is the keystroke that engages the agent-observation viewer
	// during an interactive run ("ctrl-]" by default; press it twice to send
	// one literal prefix byte to the engine). Control keys only — a printable
	// prefix would swallow ordinary typing.
	PrefixKey string `mapstructure:"prefix_key" yaml:"prefix_key,omitempty"`
	// Surround toggles the persistent bottom status bar (harp · agent · engine
	// │ children digest │ prefix hint). Default true; nil means unset.
	Surround *bool `mapstructure:"surround" yaml:"surround,omitempty"`
}

// DefaultUIPrefixKey is the default viewer prefix key (decision O2 of the
// agent-io-observation plan: Ctrl-], explicitly not ESC).
const DefaultUIPrefixKey = "ctrl-]"

// UIPrefixKey returns the configured viewer prefix key, defaulting to Ctrl-].
func (c *Config) UIPrefixKey() string {
	if c.UI.PrefixKey == "" {
		return DefaultUIPrefixKey
	}
	return c.UI.PrefixKey
}

// UISurroundEnabled reports whether the persistent surround bar is enabled
// (default true; `ui.surround: false` opts out).
func (c *Config) UISurroundEnabled() bool {
	return c.UI.Surround == nil || *c.UI.Surround
}

// GetEditorCommand returns the editor binary and arguments to use. This is the
// single editor-resolution policy: config (editor.command, with editor.args
// appended), then the VISUAL and EDITOR environment variables, then nano.
// Multi-word values like "code --wait" are whitespace-split into binary +
// leading args (strings.Fields — full shell quoting is not supported).
// IsolationImageFor returns the user-provided agent image override for the
// named backend's containerized runs, or "" when the backend keeps the built-in
// default image (nil-safe).
func (c *Config) IsolationImageFor(backend string) string {
	if c == nil {
		return ""
	}
	return c.IsolationImages[backend]
}

// IsolationBaseContainerfilePath returns the user-provided base Containerfile
// for locally-built agent images, resolved against the project root when
// relative ("" = the embedded default base; nil-safe).
func (c *Config) IsolationBaseContainerfilePath() string {
	if c == nil || c.IsolationBaseContainerfile == "" {
		return ""
	}
	p := c.IsolationBaseContainerfile
	if !filepath.IsAbs(p) && c.AppRoot != "" {
		p = filepath.Join(c.AppRoot, p)
	}
	return p
}

func (c *Config) GetEditorCommand() (string, []string) {
	if bin, args := splitEditorCommand(c.Editor.Command); bin != "" {
		return bin, append(args, c.Editor.Args...)
	}
	return EditorFromEnv()
}

// EditorFromEnv resolves the editor from the environment alone: VISUAL, then
// EDITOR, then nano. It exists for callers that must run BEFORE any config is
// loaded (e.g. `manage config edit`, which edits a possibly-broken config), so
// they share the env half of GetEditorCommand's policy instead of duplicating
// it. Values are whitespace-split like GetEditorCommand.
func EditorFromEnv() (string, []string) {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if bin, args := splitEditorCommand(os.Getenv(key)); bin != "" {
			return bin, args
		}
	}
	return "nano", nil
}

// splitEditorCommand splits an editor value into binary + args on whitespace.
// Quoting is intentionally not supported; a binary whose path contains spaces
// must be configured via editor.command + editor.args instead. An empty or
// blank value returns "".
func splitEditorCommand(value string) (string, []string) {
	fields := strings.Fields(value)
	switch len(fields) {
	case 0:
		return "", nil
	case 1:
		return fields[0], nil
	default:
		return fields[0], fields[1:]
	}
}

// ProfileDefinition returns the named profile definition and whether it exists.
func (c *Config) ProfileDefinition(name string) (Profile, bool) {
	p, ok := c.Profiles.Definitions[name]
	return p, ok
}

// DefaultAgentProfiles returns the profiles composed by the always-bound
// default agent (Config.DefaultAgent) — the single "the default profile set"
// accessor that replaced GetDefaultProfiles/ExplicitDefaultProfiles after
// profiles.defaults was retired. It resolves through the MERGED agent lookup
// (Config.Agent → config-key `agents:` folded with .ctxloom/agents/*.yaml), so a
// default agent defined either way drives the default set identically to how a
// bare `ctxloom run` binds it (operations.ResolveAgent also goes through Agent).
// Returns nil when no default agent is configured or the named agent is not
// defined by either source.
func (c *Config) DefaultAgentProfiles() []string {
	if c == nil || c.DefaultAgent == "" {
		return nil
	}
	sub, ok := c.Agent(c.DefaultAgent)
	if !ok {
		return nil
	}
	return sub.Profiles
}

// PrimaryLabel returns the config label playing the primary (coding/
// interactive) role. Fallback chain: the configured defaults.primary, else
// the sole configured label if exactly one exists, else "" — callers then
// resolve to the built-in default backend.
func (c *Config) PrimaryLabel() string {
	if c.LM.Defaults.Primary != "" {
		return c.LM.Defaults.Primary
	}
	if len(c.LM.Configs) == 1 {
		for label := range c.LM.Configs {
			return label
		}
	}
	return ""
}

// FastLabel returns the config label playing the fast (compression) role.
// Fallback chain: defaults.fast → defaults.primary (via PrimaryLabel).
func (c *Config) FastLabel() string {
	if c.LM.Defaults.Fast != "" {
		return c.LM.Defaults.Fast
	}
	return c.PrimaryLabel()
}

// ResolveLLM looks a config label up in the registry and returns the backend
// type and model it specifies. A missing label or empty type degrades to the
// built-in default backend with no model (backend default). The model is read
// only from the entry's own body — never by branching on the backend name.
func (c *Config) ResolveLLM(label string) (backend, model string) {
	entry, ok := c.LM.Configs[label]
	if !ok {
		return DefaultLLM, ""
	}
	backend = entry.EffectiveType()
	if m, ok := entry.Body["model"].(string); ok {
		model = m
	}
	return backend, model
}

// GetDefaultLLM returns the backend type for the primary role's label.
func (c *Config) GetDefaultLLM() string {
	backend, _ := c.ResolveLLM(c.PrimaryLabel())
	return backend
}

// GetDefaultLLMModel returns the model for the primary role's label.
// Empty means the backend uses its own default.
func (c *Config) GetDefaultLLMModel() string {
	_, model := c.ResolveLLM(c.PrimaryLabel())
	return model
}

// SetPrimaryLabel points the primary role at the given config label.
func (c *Config) SetPrimaryLabel(label string) {
	c.LM.Defaults.Primary = label
}

// GetCompactionLLM returns the backend type for the fast (compression) role.
func (c *Config) GetCompactionLLM() string {
	backend, _ := c.ResolveLLM(c.FastLabel())
	return backend
}

// GetCompactionModel returns the model for the fast (compression) role.
// Empty means the backend substitutes its own lightweight model.
func (c *Config) GetCompactionModel() string {
	_, model := c.ResolveLLM(c.FastLabel())
	return model
}

// GetCompactionChunkSize returns the target chunk size for compaction.
// Defaults to 8000 tokens.
func (c *Config) GetCompactionChunkSize() int {
	if c.Settings.CompactionChunks > 0 {
		return c.Settings.CompactionChunks
	}
	return 8000
}

// ShouldUseDistilled reports whether to prefer distilled fragment/prompt
// versions. Defaults to true.
func (c *Config) ShouldUseDistilled() bool {
	return c.Settings.ShouldUseDistilled()
}

// ShouldSignByDefault reports whether publish commands (fragment push,
// command push) should sign unless --no-sign is given (spec §7A.3,
// sign.default). Defaults to false.
func (c *Config) ShouldSignByDefault() bool {
	return c.Settings.ShouldSignByDefault()
}

// SignKey returns the configured sign.key override (a --key-equivalent
// fingerprint, public key path, or ssh-agent key name/comment), or "" when
// unset — meaning the zero-config discovery chain (internal/signing/agentkey)
// should be used instead.
func (c *Config) SignKey() string {
	return c.Settings.SignKey()
}

// GetProfileLoader returns a profiles.Loader for this config's ctxloom paths.
// It wires a remote resolver from the remotes registry so the loader can qualify
// legacy bare bundle refs with the remote each profile was installed from.
func (c *Config) GetProfileLoader() *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(c.fs, c.AppPaths)
	var opts []profiles.LoaderOption
	if c.fs != nil {
		opts = append(opts, profiles.WithFS(c.fs))
	}
	if resolve := c.ProfileRemoteResolver(); resolve != nil {
		opts = append(opts, profiles.WithRemoteResolver(resolve))
	}
	if resolveURL := c.ProfileRemoteURLResolver(); resolveURL != nil {
		opts = append(opts, profiles.WithRemoteURLResolver(resolveURL))
	}
	// Seed remote profiles read from the git clone cache at their locked SHA, so
	// every consumer of the loader sees them as references without a materialized
	// copy on disk (the profile-side mirror of SeededBundleLoader).
	opts = append(opts, c.ProfileSeedOptions()...)
	return profiles.NewLoader(profileDirs, opts...)
}

// ProfileSeedOptions returns the loader option that seeds the profiles shipped
// INSIDE bundles (the ungated, compound bundle item kind), keyed by their
// "<bundle>#profiles/<name>" ref, or nil when there are none. Exposed (like
// ProfileRemoteResolver/ProfileRemoteURLResolver) so other profile-loader
// factories — e.g. operations.profileLoader — wire the exact same seed as
// GetProfileLoader and the two never disagree about which profiles exist.
//
// Top-level remote "<url>@profiles/<name>" distribution was retired: profiles
// now arrive ONLY inside bundles, so this is the sole profile seed source.
func (c *Config) ProfileSeedOptions() []profiles.LoaderOption {
	bundleSeed := c.loadBundleProfileSeed()
	if len(bundleSeed) == 0 {
		return nil
	}
	return []profiles.LoaderOption{profiles.WithSeededProfiles(bundleSeed)}
}

// loadBundleProfileSeed walks every bundle visible to this config — fs-installed
// local bundles plus lockfile-listed remote bundles read from the git clone
// cache — and returns the profiles they ship, parsed and keyed by their
// canonical "<bundle>#profiles/<name>" ref, ready to seed a profiles.Loader.
//
// Profiles are an ungated, COMPOUND bundle item kind: they travel inside the
// bundle YAML, so a pulled bundle's profiles are already on disk / in cache —
// this is the step that surfaces them to the SHARED profile loader, so a bundle
// profile resolves, lists, and runs exactly like a top-level or local profile.
// The profile DEFINITION is never trust-gated here (there is no trust.ItemKind
// for profiles, and nothing is baselined); its constituent fragments/commands
// still gate at content assembly and any mcp/hooks it pulls in still gate at the
// exec choke. Returns nil when no visible bundle ships a profile.
func (c *Config) loadBundleProfileSeed() map[string]*profiles.Profile {
	if len(c.AppPaths) == 0 {
		return nil
	}
	loader := c.SeededBundleLoader(false)
	infos, err := loader.List()
	if err != nil {
		return nil
	}
	loaded := make(map[string]*profiles.Profile)
	for _, info := range infos {
		if info.Deleted || info.ProfileCount == 0 {
			continue
		}
		bundle, lerr := loader.LoadFile(info.Path)
		if lerr != nil {
			strictness.FailOnce(strictness.ClassBundle, "run `ctxloom remote pull` or fix the bundle ref, or pass --degraded", "failed to load bundle %q for its profiles: %v", info.Name, lerr)
			continue
		}
		// info.Name is the bundle's full resolution identity (the canonical ref for
		// a seeded remote bundle, the relative path for a local one); bundle.Name
		// from LoadFile is only the file's base, so canonicalize from info.Name.
		bundleRef := remote.CanonicalBundleRef(info.Name)
		sourceURL := bundleProfileSourceURL(bundleRef)
		for _, profName := range bundle.ProfileNames() {
			p := cloneBundleProfile(bundle.Profiles[profName])
			key := bundleRef + remote.ProfileSelector + profName
			// Resolve the profile's short same-repo leaf refs (bundles/fragments/
			// prompts/bundle_items) against the bundle's own source, exactly as a
			// seeded top-level remote profile does; a canonical "<bundle>#profiles/
			// <name>" parent ref passes through unchanged. No version is pinned here:
			// the lockfile already pins the bundle, and the version-agnostic leaf
			// identities let the read path honor that pin.
			p.ResolveShortRefs(sourceURL, "")
			p.Name = key
			// Sentinel path marks the profile read-only (Save/Delete refuse): like a
			// remote profile, a bundle profile is edited at its source, not locally.
			p.Path = profiles.SeededProfilePathPrefix + key
			// The VERIFIED publisher identity of the bundle this profile ships
			// inside (bundle.Signer() — stamped only by a load path that already
			// checked a signature against the trust root; "" for unsigned/
			// untrusted). resolveProfileRecursive threads this into
			// ResolvedProfile.Signer so a trusted-publisher profile's directly-
			// declared hooks/mcp are trusted-signer-allowed exactly like
			// bundle-declared ones (B2, gateProfileExec parity).
			p.Signer = bundle.Signer()
			loaded[key] = &p
		}
	}
	if len(loaded) == 0 {
		return nil
	}
	rewriteRetiredSeedParents(loaded)
	return loaded
}

// rewriteRetiredSeedParents rewrites seeded bundle-profile parents authored in
// the retired top-level "@profiles/" grammar to their bundle-shipped successor.
// Seeded profiles arrive already parsed and never pass through the loader's
// document upgrade pipeline, so this applies the same discovery-based rewrite
// against the full seed: to the one seeded bundle profile the repo ships under
// that name, verbatim when unmatched or ambiguous (profiles/upgrade.go owns the
// rule). In-memory only — a seeded profile is read-only and migrates at its
// source.
func rewriteRetiredSeedParents(loaded map[string]*profiles.Profile) {
	for _, p := range loaded {
		for i, parent := range p.Parents {
			if url, name, ok := remote.SplitRetiredProfileRef(parent); ok {
				if successor, found := profiles.FindBundleProfileKey(loaded, url, name); found {
					p.Parents[i] = successor
				}
			}
		}
	}
}

// cloneBundleProfile returns a copy of a bundle profile safe to mutate
// (ResolveShortRefs rewrites refs in place). The bundle loader caches parsed
// bundles, so the profile's slices are shared with that cache and concurrent
// profile-loader builds — clone exactly the slices ResolveShortRefs touches so
// canonicalization never corrupts the cached bundle or races another reader.
func cloneBundleProfile(bp bundles.BundleProfile) bundles.BundleProfile {
	p := bp
	p.Bundles = append([]string(nil), bp.Bundles...)
	p.Parents = append([]string(nil), bp.Parents...)
	p.Commands = append([]string(nil), bp.Commands...)
	p.Skills = append([]string(nil), bp.Skills...)
	p.BundleItems = append([]string(nil), bp.BundleItems...)
	p.Fragments = append([]profiles.FragmentRef(nil), bp.Fragments...)
	return p
}

// bundleProfileSourceURL returns the source a bundle profile's short same-repo
// refs resolve against: the bundle's repo URL for a remote bundle, or the
// ctxloom:local token for a project-local bundle.
func bundleProfileSourceURL(bundleRef string) string {
	if ref, err := remote.ParseReference(bundleRef); err == nil && ref.URL != "" {
		return ref.URL
	}
	return remote.LocalSource
}

// FS returns the injected filesystem, or nil for the OS default. It lets callers
// outside this package (e.g. operations' trust store + gate) thread the same
// filesystem the config's own loaders use, so a virtualized fs in tests — and
// the OS fs in production — stay consistent across every store read/write.
func (c *Config) FS() afero.Fs {
	return c.fs
}

// registryFSOptions threads the injected filesystem into a remote registry
// constructor (matching the resolvers below). Empty for the OS default.
func (c *Config) registryFSOptions() []remote.RegistryOption {
	if c.fs != nil {
		return []remote.RegistryOption{remote.WithRegistryFS(c.fs)}
	}
	return nil
}

// lockfileFSOptions threads the injected filesystem into a remote lockfile
// manager so lockfile reads honor c.fs alongside the registry reads. Empty for
// the OS default.
func (c *Config) lockfileFSOptions() []remote.LockfileOption {
	if c.fs != nil {
		return []remote.LockfileOption{remote.WithLockfileFS(c.fs)}
	}
	return nil
}

// ProfileRemoteResolver returns a function mapping a profile's local name to the
// short remote it was installed from, backed by the remotes registry. Nil when no
// registry is available (the loader then reads profiles verbatim). Exposed so
// other profile-loader factories (e.g. operations) wire the same qualification.
func (c *Config) ProfileRemoteResolver() func(string) string {
	if len(c.AppPaths) == 0 {
		return nil
	}
	registry, err := remote.NewRegistry(paths.RemotesPath(c.AppPaths[0]), c.registryFSOptions()...)
	if err != nil {
		return nil
	}
	return func(name string) string {
		short, _ := registry.ResolveItemRemote(name)
		return short
	}
}

// ProfileRemoteURLResolver returns a function mapping a remote alias to its
// canonical repo URL, backed by the remotes registry. Paired with
// ProfileRemoteResolver, it lets the profile loader rewrite a legacy profile's
// bare/alias bundle refs to their canonical URL form on load. Nil when no
// registry is available (the loader then reads bundle refs verbatim).
func (c *Config) ProfileRemoteURLResolver() func(string) string {
	if len(c.AppPaths) == 0 {
		return nil
	}
	registry, err := remote.NewRegistry(paths.RemotesPath(c.AppPaths[0]), c.registryFSOptions()...)
	if err != nil {
		return nil
	}
	return func(alias string) string {
		rem, err := registry.Get(alias)
		if err != nil || rem == nil {
			return ""
		}
		return rem.URL
	}
}

// Load returns the AMBIENT project config: the one config a process reads.
//
// The no-arg call is memoized, so the ~35 call sites across the CLI share one
// parse instead of each re-walking the directory tree, re-parsing the YAML and
// re-running schema validation. The memo is validated against config.yaml's
// stat (mtime+size) on every call rather than frozen at first read, because two
// behaviours depend on seeing a rewritten file WITHIN one process:
//
//   - read-after-write: init scaffolds config.yaml between two Loads;
//   - hot reload: agent_run re-loads on every spawn so edited agent definitions
//     take effect mid-session.
//
// Validating by stat (rather than invalidating at each writer) is deliberate:
// it self-corrects for ANY writer — Save, init's scaffold, another process, a
// user's editor — so a missed invalidation cannot serve a stale config.
//
// Passing options (WithAppDir/WithFS) means a DIFFERENT config — an explicit
// --app-dir, a worktree's .ctxloom, an injected fs — so those loads are never
// served from, nor written into, the ambient memo.
//
// Callers that MUTATE a config before Save must use LoadFresh: mutating the
// shared ambient instance would let a mutation abandoned on an error path leak
// into every later reader.
//
// Source priority (first found wins, no merging):
//  1. Project .ctxloom directory (walking up from cwd)
//  2. User home ~/.ctxloom directory (fallback)
func Load(opts ...LoadOption) (*Config, error) {
	// An explicit target/fs is a different config: never cached.
	if len(opts) > 0 {
		return loadUncached(opts...)
	}

	ambientMu.Lock()
	defer ambientMu.Unlock()

	stamp := ambientStamp()
	if ambientCfg != nil && ambientErr == nil && stamp == ambientAt {
		return ambientCfg, nil
	}

	cfg, err := loadUncached()
	ambientCfg, ambientErr, ambientAt = cfg, err, stamp
	return cfg, err
}

// LoadFresh loads a config WITHOUT consulting or populating the ambient memo.
// It is the mutator's entry point: Load hands back a shared instance, so a
// caller that mutates before Save (agent/llm/mcp/tooling writes) must own its
// own copy or an abandoned mutation would poison every later reader.
func LoadFresh(opts ...LoadOption) (*Config, error) {
	return loadUncached(opts...)
}

// Invalidate drops the memoized ambient config, so the next Load re-reads from
// disk. Load's stat check already covers ordinary writes; this is the explicit
// escape hatch (and what tests use to isolate from one another).
func Invalidate() {
	ambientMu.Lock()
	defer ambientMu.Unlock()
	ambientCfg, ambientErr, ambientAt = nil, nil, ""
}

// ambient* memoize the no-arg Load. ambientAt is the stat stamp of the
// config.yaml the memo was built from; a mismatch (or an unfindable file) means
// re-read. Errors are NOT memoized as successes: a failed load re-attempts.
var (
	ambientMu  sync.Mutex
	ambientCfg *Config
	ambientErr error
	ambientAt  string
)

// ambientStamp returns a cheap identity for the config file the ambient memo
// was built from: path + mtime + size. An empty stamp (no discoverable config)
// never matches a populated one, so the no-config case simply doesn't cache.
func ambientStamp() string {
	fs := afero.NewOsFs()
	appPath, _ := findAppDir(fs)
	if appPath == "" {
		return ""
	}
	path := paths.ConfigPath(appPath)
	info, err := fs.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
}

// loadUncached is the real loader: it always reads and parses from disk.
func loadUncached(opts ...LoadOption) (*Config, error) {
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
		Profiles: ProfilesConfig{Definitions: make(map[string]Profile)},
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

	// Overlay the shipped default config so an empty user config still resolves
	// a primary + fast role (and so model names live in DATA, not Go). User keys
	// always win; defaults only fill gaps the user left empty.
	mergeDefaultConfig(cfg)

	return cfg, nil
}

// ParseConfig unmarshals raw YAML into a Config WITHOUT overlaying the embedded
// default registry. Unlike Load it does not read from disk, validate, upgrade,
// or merge defaults — callers that need the raw registry entries (e.g. init
// reading the shipped default-config) use this so the role markers and exact
// entries survive untouched.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{
		LM:       LMConfig{Configs: make(map[string]LLMConfig)},
		Profiles: ProfilesConfig{Definitions: make(map[string]Profile)},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if cfg.LM.Configs == nil {
		cfg.LM.Configs = make(map[string]LLMConfig)
	}
	return cfg, nil
}

// mergeDefaultConfig fills any LLM-role gaps from the embedded default config.
// Per CLAUDE.md fault tolerance a malformed/unreadable default never blocks
// startup — the merge is skipped silently. User config always wins: a default
// label is added only when absent, and a role default only when the user set
// none.
func mergeDefaultConfig(cfg *Config) {
	data, err := resources.GetDefaultConfig()
	if err != nil {
		return
	}
	var def Config
	if err := yaml.Unmarshal(data, &def); err != nil {
		zap.L().Warn("default_config_parse_failed", zap.Error(err))
		return
	}
	// The embedded default is a whole-registry fallback for users who configured
	// no LLMs — not a per-key overlay. Injecting default labels into a non-empty
	// user registry would defeat the single-entry selection rule (one configured
	// entry is the one used), so a non-empty user registry is left untouched.
	if len(cfg.LM.Configs) > 0 {
		return
	}
	// cfg gets its own DEEP copy: the overlay snapshot must stay pristine so a
	// later in-place registry mutation isn't mistaken for "still the default"
	// and stripped by Save. A top-level maps.Copy is not enough — each entry's
	// Body map would still be shared with the snapshot, so mutating e.g.
	// Body["model"] in place would also rewrite the overlay and defeat the
	// userAuthoredLM comparison.
	overlay := LMConfig{Configs: def.LM.Configs}
	cfg.LM.Configs = make(map[string]LLMConfig, len(def.LM.Configs))
	for label, entry := range def.LM.Configs {
		entry.Body = deepCopyBody(entry.Body)
		cfg.LM.Configs[label] = entry
	}
	if cfg.LM.Defaults.Primary == "" {
		cfg.LM.Defaults.Primary = def.LM.Defaults.Primary
		overlay.Defaults.Primary = def.LM.Defaults.Primary
	}
	if cfg.LM.Defaults.Fast == "" {
		cfg.LM.Defaults.Fast = def.LM.Defaults.Fast
		overlay.Defaults.Fast = def.LM.Defaults.Fast
	}
	cfg.lmDefaultOverlay = &overlay
}

// deepCopyBody clones an LLMConfig.Body recursively (nested maps and slices —
// the shapes yaml.Unmarshal produces), so a copy's mutations never reach the
// original. Nil in, nil out.
func deepCopyBody(body map[string]any) map[string]any {
	if body == nil {
		return nil
	}
	out := make(map[string]any, len(body))
	for k, v := range body {
		out[k] = deepCopyValue(v)
	}
	return out
}

// deepCopyValue clones the YAML-decoded value shapes that can alias storage.
// Scalars are returned as-is.
func deepCopyValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return deepCopyBody(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return v
	}
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
		// An existing-but-unreadable config (EACCES, a directory in its place, a
		// transient I/O error) degrades to the default-overlaid empty config with
		// a kind-tagged warning; the strict startup gate (fail-loudly) turns it
		// into a fatal finding, while --degraded launches anyway.
		cfg.Warnings = append(cfg.Warnings, Warning{Kind: WarnKindRead, Text: fmt.Sprintf("failed to read config at %s: %v", configPath, err)})
		zap.L().Warn("config_read_warning", zap.String("path", configPath), zap.Error(err))
		return nil
	}

	// Upgrade older on-disk schema generations to the current one *in memory*
	// before validation/parse, so old configs neither warn nor silently drop
	// settings. We do NOT rewrite the file here: an interactive caller may prompt
	// the user and persist via CommitUpgrade (see cmd/run.go). This keeps
	// non-interactive contexts (MCP server, scripts) from silently rewriting a
	// user's config — the exact failure mode that motivated this layer.
	// The registry-free schema upgrades (configUpgrades) plus the registry-aware
	// agent-profile canonicalization compose into one pipeline so the load parses
	// and re-encodes the document exactly once. The canonicalization step is
	// threaded the alias→URL resolver here (it depends on .ctxloom/remotes.yaml,
	// which the static pipeline cannot reach); a nil resolver makes it a no-op.
	pipeline := append(upgrade.Pipeline{}, configUpgrades...)
	pipeline = append(pipeline, agentProfileCanonicalizeUpgrade{aliasToURL: cfg.ProfileRemoteURLResolver()})
	if upgraded, applied := pipeline.Run(data); len(applied) > 0 {
		data = upgraded
		cfg.PendingUpgrade = &upgrade.Pending{Path: configPath, Data: upgraded, Applied: applied}
		zap.L().Info("config_upgrade_pending", zap.String("path", configPath), zap.Strings("applied", applied))
	}
	// A lossy upgrade (a dropped user-set value) is collected by the pipeline
	// rather than printed inline, so the loader can tag it with its kind and
	// the strict startup gate can abort on it (fail-loudly).
	for _, lost := range drainMigrationWarnings() {
		cfg.Warnings = append(cfg.Warnings, Warning{Kind: WarnKindMigrationLossy, Text: lost})
		zap.L().Warn("config_migration_lossy", zap.String("path", configPath), zap.String("warning", lost))
	}

	// Validate against schema before parsing — warn but continue on failure.
	// This runs AFTER the upgrade pipeline above, deliberately: a key an older
	// config legitimately carries is migrated forward first, so only a key the
	// CURRENT schema truly does not know can be reported. The schema is authored
	// additionalProperties:false throughout, so an unknown key is a violation;
	// classifyValidationError splits those out into named, actionable unknown-key
	// warnings (kind unknown-key → a fatal finding in strict mode) and leaves any
	// other schema breakage as the plain validate warning it has always been.
	if validator != nil {
		if err := validator.ValidateBytes(data); err != nil {
			cfg.Warnings = append(cfg.Warnings, classifyValidationError(configPath, err)...)
			zap.L().Warn("config_validation_warning", zap.String("path", configPath), zap.Error(err))
		}
	}

	// Parse with yaml directly, NOT viper. Viper lowercases every key it decodes,
	// which corrupts the case-sensitive keys captured by LLMConfig.Body's
	// `,remain`/`,inline` map: a backend `env: {GEMINI_API_KEY: ...}` would reach
	// the launched process as `gemini_api_key`, so the engine never sees its
	// credential. yaml.Unmarshal preserves key case and matches ParseConfig (the
	// init path), so both entry points decode a config identically.
	if err := yaml.Unmarshal(data, cfg); err != nil {
		cfg.Warnings = append(cfg.Warnings, Warning{Kind: WarnKindParse, Text: fmt.Sprintf("failed to parse config at %s: %v", configPath, err)})
		zap.L().Warn("config_parse_warning", zap.String("path", configPath), zap.Error(err))
		// Return nil - we have a valid (partially loaded) config with warnings
		return nil
	}

	zap.L().Debug("config_loaded", zap.String("path", configPath))
	return nil
}

// findAppDir locates the .ctxloom directory.
// Priority:
//  1. CTXLOOM_ROOT override (when set and a valid directory)
//  2. Walk up from cwd looking for .ctxloom directory
//  3. Fall back to user home ~/.ctxloom directory
//
// Always returns a path (creates user home .ctxloom if needed).
func findAppDir(fs afero.Fs) (string, ConfigSource) {
	// CTXLOOM_ROOT is authoritative when valid: the user named the root
	// explicitly, so resolve config at $CTXLOOM_ROOT/.ctxloom and create it if
	// absent, mirroring the home fallback below. A failed MkdirAll warns and
	// continues — the path is still returned so the run isn't blocked.
	if root, ok := projectroot.FromEnv(fs); ok {
		appPath := filepath.Join(root, AppDirName)
		if err := fs.MkdirAll(appPath, 0755); err != nil {
			zap.L().Warn("failed to create CTXLOOM_ROOT .ctxloom directory", zap.String("path", appPath), zap.Error(err))
		}
		return appPath, SourceProject
	}

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

			// dir has no .ctxloom of its own. If dir is the root of a LINKED
			// git worktree, that is a signpost, not a silent walk-past:
			// resolving straight through to some unrelated ancestor's (or
			// home's) .ctxloom would silently land the session on the wrong
			// project — empty config, no profiles, no agents (task
			// brown-canal, 2026-07-09: an earlier revision had linked
			// worktrees INHERIT the main worktree's project identity; that
			// inheritance design was withdrawn in favor of this signpost).
			// worktreeSignpost records a fatal finding through strictness and
			// the walk continues exactly as it always has — the choke owners
			// (`ctxloom run`/`mcp`/`acp`) abort on it pre-launch unless
			// --degraded; management commands surface the stderr warning and
			// proceed on the fallback. The main worktree (.git is a
			// directory) and every non-worktree ancestor pass through
			// untouched.
			worktreeSignpost(fs, dir)

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

// worktreeSignpost records a fatal ClassConfig finding when dir is the root of
// a LINKED git worktree carrying no .ctxloom of its own — naming the resolved
// main worktree root and both remediation paths (run from the main worktree,
// or `ctxloom init` here to make this worktree a deliberately separate
// project). FailOnce, because findAppDir runs on every config.Load and a
// single process loads config several times — the finding must not stack up
// in one startup window. No-op (walk continues to today's fallback) when dir
// is not such a worktree root.
//
// A linked worktree WITH its own .ctxloom never reaches this call: the walk in
// findAppDir already returned on the .ctxloom check for that same dir. That is
// the one, load-bearing precedence rule for this feature — own .ctxloom always
// wins, no further worktree inspection.
func worktreeSignpost(fs afero.Fs, dir string) {
	info, err := projectroot.DetectWorktree(fs, dir)
	if err != nil {
		strictness.FailOnce(strictness.ClassConfig,
			"check permissions on the .git file in this directory",
			"%s: could not read git worktree metadata: %v", dir, err)
		return
	}
	if !info.Linked {
		return
	}
	if !info.MainRootExists {
		strictness.FailOnce(strictness.ClassConfig,
			fmt.Sprintf("restore the main worktree at %s, or prune this stale linked worktree (`git worktree prune` from a healthy checkout), or run `ctxloom init` here to make this worktree a deliberately separate project", info.MainRoot),
			"%s is a linked git worktree, but its main worktree at %s is missing or unreadable", dir, info.MainRoot)
		return
	}
	strictness.FailOnce(strictness.ClassConfig,
		fmt.Sprintf("run ctxloom from %s, or run `ctxloom init` here to make this worktree a deliberately separate project", info.MainRoot),
		"this is a linked git worktree of the project at %s (no .ctxloom of its own)", info.MainRoot)
}

// GetBundleDirs returns the project's AUTHORED bundle directories — the
// committed content tree (.ctxloom/content/bundles), NOT the gitignored cache.
// This is the set every authored-bundle path resolves against: `bundle create`
// writes here, `bundle list` lists it, and `sign --all` signs exactly it (a
// publishing repo's bundles ARE this directory). The cache
// (paths.CacheBundlesPath) holds remote-pull artifacts the project has no authority
// to author or sign, so it is deliberately absent.
//
// A cache/bundles left holding AUTHORED work from the pre-content layout is
// not silently skipped — that would delete a user's bundles from view. See
// legacyCacheBundlesSignpost.
func (c *Config) GetBundleDirs() []string {
	fs := c.getFS()
	var dirs []string
	for _, appPath := range c.AppPaths {
		legacyCacheBundlesSignpost(fs, appPath)
		bundleDir := paths.LocalBundlesPath(appPath)
		if info, err := fs.Stat(bundleDir); err == nil && info.IsDir() {
			dirs = append(dirs, bundleDir)
		}
	}
	return dirs
}

// legacyCacheBundlesSignpost records a fatal ClassMigration finding when
// .ctxloom/cache/bundles still holds AUTHORED bundles — bundles written there
// by the pre-content-tree `bundle create`, which the authored read/write path
// no longer looks at. Ignoring them silently would make a user's own work
// vanish from `bundle list` and `sign --all` with no explanation, so the move
// is demanded, not performed: ctxloom does not rewrite content it did not
// author in this run (no-backward-compat-shims — re-place, don't shim).
//
// Remote-pull artifacts in the same tree (identified by a `_source.sha`, the
// same marker operations.PurgeExtractedBundles keys on) are genuine cache and
// never fire this: they are regenerable from the lockfile + clone cache.
//
// FailOnce, because GetBundleDirs is called many times per process (every
// loader build) and the finding must not stack up inside one startup window.
func legacyCacheBundlesSignpost(fs afero.Fs, appPath string) {
	cacheBundles := paths.CacheBundlesPath(appPath)
	stranded := strandedAuthoredBundles(fs, cacheBundles)
	if len(stranded) == 0 {
		return
	}
	strictness.FailOnce(strictness.ClassMigration,
		fmt.Sprintf("move them into the committed content tree: mkdir -p %s && git mv %s/* %s/ (or plain mv outside git)",
			paths.LocalBundlesPath(appPath), cacheBundles, paths.LocalBundlesPath(appPath)),
		"%s holds %d authored bundle(s) (%s) but authored bundles now live in %s — the cache is gitignored and is no longer read, so these are invisible to `bundle list`, `run`, and `sign --all`",
		cacheBundles, len(stranded), strings.Join(stranded, ", "), paths.LocalBundlesPath(appPath))
}

// strandedAuthoredBundles walks a legacy cache/bundles tree and returns the
// base names of every YAML that is NOT a remote-pull artifact — i.e. every file
// that can only have been authored locally. Unreadable/unparseable files are
// treated as authored: a file we cannot prove is regenerable cache is work we
// must not tell the user to ignore.
func strandedAuthoredBundles(fs afero.Fs, cacheBundles string) []string {
	if info, err := fs.Stat(cacheBundles); err != nil || !info.IsDir() {
		return nil
	}
	var stranded []string
	_ = afero.Walk(fs, cacheBundles, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal
		}
		if data, rerr := afero.ReadFile(fs, path); rerr == nil {
			// Legacy remote-pull artifacts embed a `_source` block; a non-empty
			// SHA there unambiguously marks one (mirrors PurgeExtractedBundles).
			var meta struct {
				Source struct {
					SHA string `yaml:"sha"`
				} `yaml:"_source"`
			}
			if yaml.Unmarshal(data, &meta) == nil && meta.Source.SHA != "" {
				return nil
			}
		}
		rel, rerr := filepath.Rel(cacheBundles, path)
		if rerr != nil {
			rel = info.Name()
		}
		stranded = append(stranded, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(stranded)
	return stranded
}

// SeededBundleLoader returns a bundles.Loader that sees fs-installed local
// bundles, every remote bundle in the active lockfile (pre-loaded from the
// local git clone cache and SHA-pinned), and every discovered companion's
// loadout (S8 — ProbeCompanionLoadouts, seeded under its
// ctxloom:companion@<bin> ref). This is the read-path loader every caller
// should use after PR 1: remote bundles no longer live on disk as extracted
// YAML, so the seeding step is what makes them (and companion loadouts,
// which never touch disk at all) visible.
//
// Failures are degraded gracefully (CLAUDE.md fault tolerance): a missing
// lockfile, unregistered remote, single bad SHA, or unreachable/invalid
// companion loadout produces a stderr warning and the loader returns the
// rest.
func (c *Config) SeededBundleLoader(preferDistilled bool, opts ...bundles.LoaderOption) *bundles.Loader {
	if c.fs != nil {
		// Thread the injected filesystem so fs-installed local bundle discovery
		// and reads honor it, matching GetProfileLoader's profiles.WithFS(c.fs).
		opts = append(append([]bundles.LoaderOption(nil), opts...), bundles.WithFS(c.fs))
	}
	remoteSeed := c.loadRemoteBundleSeed()
	companionSeed := c.companionBundleSeed()
	if len(remoteSeed) > 0 || len(companionSeed) > 0 {
		merged := make(map[string]*bundles.Bundle, len(remoteSeed)+len(companionSeed))
		maps.Copy(merged, remoteSeed)
		maps.Copy(merged, companionSeed)
		opts = append(append([]bundles.LoaderOption(nil), opts...), bundles.WithSeededBundles(merged))
	}
	// Multi-version coexistence (trust rework, TR5): give every read-path loader
	// the capability to materialize a specific historical commit-version of a
	// remote bundle via FetchItem. This is opt-in at the loader's version-aware
	// methods only — the default (lockfile-pinned) path is unaffected — so wiring
	// it everywhere is free until a caller asks for an "@<commit>" version.
	if resolver := c.bundleVersionResolver(); resolver != nil {
		opts = append(append([]bundles.LoaderOption(nil), opts...), bundles.WithVersionResolver(resolver))
	}
	return bundles.NewLoader(c.GetBundleDirs(), preferDistilled, opts...)
}

// companionBundleSeed discovers and probes every companion's loadout
// (ProbeCompanionLoadouts) exactly once per Config — see
// companionSeedOnce/companionSeedCache — verifying any signature against
// THIS config's full trust root (embedded + user + project allowed_signers,
// same root verifyBundlePublisher uses for remote bundles below). The
// result merges into SeededBundleLoader's seed map alongside
// loadRemoteBundleSeed's remote bundles: same seam, same gate, same review
// path (operations.EffectiveTrust, unchanged) — a companion loadout is not
// a parallel trust mechanism.
//
// Skipped entirely when there is no project directory (mirrors
// loadRemoteBundleSeed's own AppPaths guard): companion content only
// matters for a real project session, and this keeps a bare/management
// Config — the shape most unit tests construct — from spawning companion
// subprocesses it has no use for.
func (c *Config) companionBundleSeed() map[string]*bundles.Bundle {
	if len(c.AppPaths) == 0 {
		return nil
	}
	companionSeedInitMu.Lock()
	if c.companionSeed == nil {
		c.companionSeed = &companionSeedState{}
	}
	state := c.companionSeed
	companionSeedInitMu.Unlock()

	// The process-wide switch (--no-companions / CTXLOOM_NO_COMPANIONS) wins over
	// everything, INCLUDING an injected probe: "off" must mean no companion code
	// runs, not "off unless something wired an override". Disabled short-circuits
	// before any probe is selected, so no companion subprocess is executed and no
	// loadout is contributed — skipping the exec, not discarding its result, is
	// the point, since probing shells out to whatever companion binaries happen
	// to be on the host's PATH.
	if CompanionsDisabled() {
		return nil
	}

	// Otherwise a Config's own override (the test seam) wins over the real probe,
	// so a parallel test can pin its own fixture without touching the global.
	probe := c.companionProbe
	if probe == nil {
		probe = ProbeCompanionLoadouts
	}

	state.once.Do(func() {
		state.cache = probe(c.TrustRoot())
	})
	return state.cache
}

// DisableCompanionProbe makes companion-loadout discovery a no-op for this
// Config. Companion probing execs the companion binaries found on the host's
// PATH, which makes any assertion over an exact command set depend on what the
// developer happens to have installed. Tests that pin such a set call this so
// the fixture — not the machine — decides the result.
func (c *Config) DisableCompanionProbe() {
	c.companionProbe = func(signing.TrustRoot) map[string]*bundles.Bundle { return nil }
}

// companionSeedState is the memoized result of one Config's companion-loadout
// probe, held by pointer from Config.companionSeed — see that field's doc for
// why this can't be a value sync.Once field on Config directly.
type companionSeedState struct {
	once  sync.Once
	cache map[string]*bundles.Bundle
}

// companionSeedInitMu guards ONLY the lazy allocation of a Config's
// companionSeed pointer (a handful of instructions); the actual probe work
// stays memoized via companionSeedState's own sync.Once, unlocked. A
// package-level lock rather than a Config field, so it never participates in
// a struct copy.
var companionSeedInitMu sync.Mutex

// bundleVersionResolver returns a bundles.BundleVersionResolver that materializes
// a bundle at a specific commit and parses the bytes into a Bundle. It dispatches
// by the ref's SOURCE — the loader's multi-version coexistence backed end to end:
//
//   - remote/canonical ref → the FetchItem primitive over the local git clone
//     cache (remote.FetchRefBytes), exactly as before;
//   - ctxloom:local ref → the file's bytes as of <commit> in the PROJECT'S OWN
//     git history (the committed .ctxloom/content/ tree), via the local working-copy
//     VCS — `git show <commit>:<path>` semantics. The unversioned local path is
//     untouched: the loader only invokes the resolver for an explicit "@<commit>".
//
// Given a version-less canonical ref and an opaque commit, it reads exactly that
// historical version. Returns nil when there is no app dir to anchor either
// source. The fetch is lazy — nothing happens until a version-aware loader method
// actually requests a pinned commit — and any failure (unknown rev, non-git
// project, path-absent-at-rev) fails closed: the caller withholds just that item.
//
// Auth and both git backends are inherently OS-backed (the remote cache shells
// out to git; the local backend opens the on-disk project .git), so they do not
// honor c.fs — matching loadRemoteBundleSeed.
func (c *Config) bundleVersionResolver() bundles.BundleVersionResolver {
	if len(c.AppPaths) == 0 {
		return nil
	}
	baseDir := c.AppPaths[0]
	// Defer the auth read + clone-cache construction to the FIRST actual remote
	// version fetch: the default (lockfile) path never invokes the resolver, and a
	// local-only pin never touches the remote cache, so neither pays for it.
	var (
		once    sync.Once
		factory remote.FetcherFactory
		auth    remote.AuthConfig
	)
	return func(canonicalRef, commit string) (*bundles.Bundle, error) {
		ref, err := remote.ParseReference(canonicalRef)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", canonicalRef, err)
		}

		// Local (project-authored) refs version against the PROJECT'S own git
		// history, not the remote clone cache. The committed .ctxloom/content/ tree
		// is read at <commit> through the working-copy VCS; a non-git project,
		// unknown rev, or path-absent-at-rev errors here and the caller withholds.
		if ref.IsLocal {
			data, err := remote.NewLocalRefFetcher(
				remote.LocalGitVCSFactory(afero.NewOsFs()),
				paths.LocalPath(baseDir),
			).FetchItem(context.Background(), ref, commit)
			if err != nil {
				return nil, err
			}
			return bundles.ParseBundle(data)
		}

		// Remote/canonical refs: FetchItem over the local clone cache (auth +
		// cache built once, lazily, on the first remote pin).
		once.Do(func() {
			auth = remote.LoadAuth(baseDir)
			cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
			factory = remote.NewCachedFetcherFactory(cache)
		})
		data, err := remote.FetchRefBytes(context.Background(), factory, auth, ref, commit)
		if err != nil {
			return nil, err
		}
		return bundles.ParseBundle(data)
	}
}

// loadRemoteBundleSeed materializes every lockfile-listed bundle from the local
// git clone cache, parsed and keyed by its CANONICAL ref ("<url>@bundles/<path>")
// ready to seed a bundles.Loader. Canonical is the sole resolution identity:
// profiles author canonical refs and resolve straight to these seeded bundles.
// Returns nil when there is no lockfile or registry — caller treats nil as "no
// remote bundles, just walk fs."
func (c *Config) loadRemoteBundleSeed() map[string]*bundles.Bundle {
	if len(c.AppPaths) == 0 {
		return nil
	}
	baseDir := c.AppPaths[0]

	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir), c.registryFSOptions()...)
	if err != nil {
		return nil
	}
	lock, err := remote.NewLockfileManager(baseDir, c.lockfileFSOptions()...).Load()
	if err != nil {
		return nil
	}
	if lock.IsEmpty() {
		return nil
	}
	// Auth config and the git clone cache are inherently OS-backed (the cache
	// shells out to git), so they intentionally do not honor c.fs.
	auth := remote.LoadAuth(baseDir)
	cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
	factory := remote.NewCachedFetcherFactory(cache)
	// Wrap in the caching decorator so repeated SeededBundleLoader calls
	// within a session don't re-walk the clone for the same SHAs.
	reader := remote.NewCachingBundleReader(remote.NewBundleReader(registry, factory, auth, lock))

	rawBytes, failures := remote.LoadAllBytes(context.Background(), reader)
	for name, err := range failures {
		// A lockfile-active bundle that fails to load is fatal-class in strict
		// mode (the user pinned it; content silently missing from a session is
		// the failure fail-loudly exists to catch). Warns and continues in
		// degraded mode.
		strictness.FailOnce(strictness.ClassBundle, "ctxloom remote pull (or remove the bundle from its profiles)",
			"failed to load remote bundle %q from cache: %v", name, err)
	}

	// The trust root (embedded + user + project allowed_signers) is resolved once
	// for the whole seed. This is the ONLY place the raw bundle bytes and their
	// detached signature are both in hand, so it is where publisher verification
	// must happen (spec §8.1) — before parse, over the file bytes.
	root := c.TrustRoot()

	loaded := make(map[string]*bundles.Bundle, len(rawBytes))
	for canonical, data := range rawBytes {
		entry, ok := lock.Bundles[canonical]
		if !ok {
			continue
		}

		// Verify BEFORE parse, over the exact file bytes. Three outcomes:
		//   unsigned/untrusted-key → signer "" → review path (the common case);
		//   verified              → the publisher principal, stamped below;
		//   TAMPER                → withhold the bundle entirely, never degrade
		//                           it to unsigned (that would let an attacker
		//                           downgrade a signed bundle by corrupting its
		//                           .sig — spec §10.2).
		signer, verr := verifyBundlePublisher(reader, canonical, data, root)
		if errors.Is(verr, signing.ErrSignatureTampered) {
			strictness.FailOnce(strictness.ClassTrust, "re-pull the bundle, or investigate the source — its signature does not cover its bytes",
				"remote bundle %q has a signature that does not verify over its content; withholding it: %v", canonical, verr)
			continue
		}

		b, perr := bundles.ParseBundle(data)
		if perr != nil {
			// Same fatal class as the read failure above: a pinned bundle whose
			// content cannot be used must not silently vanish from assembly.
			strictness.FailOnce(strictness.ClassBundle, "fix the bundle at its source, or remove it from its profiles",
				"failed to parse remote bundle %q: %v", canonical, perr)
			continue
		}
		// Lockfile keys are canonical refs — the sole seed/resolution identity.
		b.Name = canonical
		b.Path = fmt.Sprintf("<remote>:%s@%s", canonical, entry.SHA)
		b.StampSigner(signer) // "" for unsigned; the verified principal otherwise
		loaded[canonical] = b
	}
	return loaded
}

// verifyBundlePublisher reads a bundle's detached `.sig` sibling and verifies it
// over the bundle's raw file bytes against the trust root. A MISSING signature
// (the common case) is unsigned content, not an error: it returns ("", nil). A
// signature by an untrusted key is likewise unsigned-to-us. Only a trusted key's
// signature that does not cover these bytes — or a structurally invalid blob —
// is a tamper signal, returned as signing.ErrSignatureTampered for the caller to
// withhold on.
func verifyBundlePublisher(reader remote.BundleSignatureSource, canonical string, data []byte, root signing.TrustRoot) (string, error) {
	sig, sigErr := reader.ReadBundleSignature(context.Background(), canonical)
	if sigErr != nil {
		// A missing .sig — or any read failure — is treated as UNSIGNED. Absence
		// is the documented "unsigned" signal (spec §4.1); a non-not-found read
		// error is degraded the same way rather than blocking the bundle, because
		// the fail-safe direction is "more review", and an unsigned bundle is
		// withheld until a human reviews it anyway.
		return "", nil
	}
	return signing.VerifyPublisher(data, sig, root, time.Now())
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
