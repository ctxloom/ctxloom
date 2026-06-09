package config

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/upgrade"
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/collections"
	"github.com/ctxloom/shared/wire"
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
	AppPaths []string         // Resolved .ctxloom directory (at most one)
	AppRoot  string           // Project root (parent of .ctxloom directory)
	AppDir   string           // Full path to the .ctxloom directory
	Source   ConfigSource     // Where the configuration was loaded from
	Warnings []string         // Non-fatal warnings collected during load

	// PendingUpgrade is set when Load upgraded an older on-disk schema to the
	// current one in memory. The upgraded bytes are NOT persisted automatically;
	// an interactive caller may prompt the user and call CommitUpgrade. Nil when
	// the file was already current.
	PendingUpgrade *upgrade.Pending

	fs afero.Fs // Filesystem for file operations (nil = OS filesystem)

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

// ExplicitDefaultProfiles returns profiles named in the canonical
// defaults.profiles array. This does NOT apply the single-profile fallback
// used by GetDefaultProfiles. Use this when deciding whether to auto-promote
// a newly-installed profile — auto-promote should only trigger when the
// user has made no explicit choice.
func (c *Config) ExplicitDefaultProfiles() []string {
	seen := collections.NewSet[string]()
	var defaults []string
	for _, name := range c.Profiles.Defaults {
		if name != "" && !seen.Has(name) {
			seen.Add(name)
			defaults = append(defaults, name)
		}
	}
	return defaults
}

// EffectiveDefaultProfiles returns the configured default profile labels
// verbatim (profiles.defaults). Unlike GetDefaultProfiles it applies no
// single-profile fallback.
func (c *Config) EffectiveDefaultProfiles() []string {
	return c.Profiles.Defaults
}

// ProfileDefinition returns the named profile definition and whether it exists.
func (c *Config) ProfileDefinition(name string) (Profile, bool) {
	p, ok := c.Profiles.Definitions[name]
	return p, ok
}

// GetDefaultProfiles returns the default profiles to load for `ctxloom run`.
// Reads the canonical defaults.profiles array. As a last-resort fallback, if
// no default is configured but exactly one profile is installed locally, that
// profile is returned — otherwise `ctxloom run` would launch with empty
// context.
func (c *Config) GetDefaultProfiles() []string {
	defaults := c.ExplicitDefaultProfiles()
	if len(defaults) > 0 {
		return defaults
	}

	// Fallback: if exactly one profile is installed, treat it as the default.
	if all, err := c.GetProfileLoader().List(); err == nil && len(all) == 1 {
		return []string{all[0].Name}
	}
	return nil
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
	backend = entry.Type
	if backend == "" {
		backend = DefaultLLM
	}
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

// GetProfileLoader returns a profiles.Loader for this config's ctxloom paths.
// It wires a remote resolver from the remotes registry so the loader can qualify
// legacy bare bundle refs with the remote each profile was installed from.
func (c *Config) GetProfileLoader() *profiles.Loader {
	profileDirs := profiles.GetProfileDirs(c.AppPaths)
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
	if seed := c.loadRemoteProfileSeed(); len(seed) > 0 {
		opts = append(opts, profiles.WithSeededProfiles(seed))
	}
	return profiles.NewLoader(profileDirs, opts...)
}

// loadRemoteProfileSeed reads every lockfile-listed profile from the local git
// clone cache at its locked SHA, parses it, resolves its short same-repo bundle
// and parent refs against its repo URL, and returns them keyed by canonical ref
// ready to seed a profiles.Loader. Remote profiles are pure references — never
// materialized to disk — so this is how they enter the loader. Returns nil when
// there is no lockfile (caller treats nil as "no remote profiles").
func (c *Config) loadRemoteProfileSeed() map[string]*profiles.Profile {
	if len(c.AppPaths) == 0 {
		return nil
	}
	baseDir := c.AppPaths[0]

	lock, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil || lock.IsEmpty() {
		return nil
	}
	auth := remote.LoadAuth(baseDir)
	cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
	factory := remote.NewCachedFetcherFactory(cache)
	reader := remote.NewProfileReader(factory, auth, lock)

	loaded := make(map[string]*profiles.Profile)
	for _, canonical := range reader.ListProfileNames() {
		data, rerr := reader.ReadProfileBytes(context.Background(), canonical)
		if rerr != nil {
			warnOncePerRun(fmt.Sprintf("ctxloom: warning: failed to load remote profile %q from cache: %v\n", canonical, rerr))
			continue
		}
		p, perr := profiles.ParseProfile(data)
		if perr != nil {
			warnOncePerRun(fmt.Sprintf("ctxloom: warning: failed to parse remote profile %q: %v\n", canonical, perr))
			continue
		}
		entry := lock.Profiles[canonical]
		// The profile's own repo URL is the source its short sibling refs resolve
		// against — it's intrinsic to the canonical lockfile key.
		repoURL := entry.URL
		if ref, e := remote.ParseReference(canonical); e == nil && ref.URL != "" {
			repoURL = ref.URL
		}
		p.ResolveShortRefs(repoURL, entry.SHA)
		p.Name = canonical
		p.Path = fmt.Sprintf("<remote>:%s@%s", canonical, entry.SHA)
		loaded[canonical] = p
	}
	if len(loaded) == 0 {
		return nil
	}
	return loaded
}

// ProfileRemoteResolver returns a function mapping a profile's local name to the
// short remote it was installed from, backed by the remotes registry. Nil when no
// registry is available (the loader then reads profiles verbatim). Exposed so
// other profile-loader factories (e.g. operations) wire the same qualification.
func (c *Config) ProfileRemoteResolver() func(string) string {
	if len(c.AppPaths) == 0 {
		return nil
	}
	var ropts []remote.RegistryOption
	if c.fs != nil {
		ropts = append(ropts, remote.WithRegistryFS(c.fs))
	}
	registry, err := remote.NewRegistry(paths.RemotesPath(c.AppPaths[0]), ropts...)
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
	var ropts []remote.RegistryOption
	if c.fs != nil {
		ropts = append(ropts, remote.WithRegistryFS(c.fs))
	}
	registry, err := remote.NewRegistry(paths.RemotesPath(c.AppPaths[0]), ropts...)
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
	// cfg gets its own entry map: the overlay snapshot must stay pristine so a
	// later in-place registry mutation isn't mistaken for "still the default"
	// and stripped by Save.
	overlay := LMConfig{Configs: def.LM.Configs}
	cfg.LM.Configs = make(map[string]LLMConfig, len(def.LM.Configs))
	maps.Copy(cfg.LM.Configs, def.LM.Configs)
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
		// transient I/O error) must not block startup any more than a malformed one
		// does: warn and continue with the default-overlaid empty config. CLAUDE.md
		// is explicit that missing/unreadable files produce warnings, never a hard
		// stop — and the sibling parse/validate branches below already degrade.
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("failed to read config at %s: %v", configPath, err))
		zap.L().Warn("config_read_warning", zap.String("path", configPath), zap.Error(err))
		return nil
	}

	// Upgrade older on-disk schema generations to the current one *in memory*
	// before validation/parse, so old configs neither warn nor silently drop
	// settings. We do NOT rewrite the file here: an interactive caller may prompt
	// the user and persist via CommitUpgrade (see cmd/run.go). This keeps
	// non-interactive contexts (MCP server, scripts) from silently rewriting a
	// user's config — the exact failure mode that motivated this layer.
	if upgraded, applied := configUpgrades.Run(data); len(applied) > 0 {
		data = upgraded
		cfg.PendingUpgrade = &upgrade.Pending{Path: configPath, Data: upgraded, Applied: applied}
		zap.L().Info("config_upgrade_pending", zap.String("path", configPath), zap.Strings("applied", applied))
	}

	// Validate against schema before parsing - warn but continue on failure
	if validator != nil {
		if err := validator.ValidateBytes(data); err != nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("config validation warning at %s: %v", configPath, err))
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

// GetBundleDirs returns bundles directories (in cache/).
func (c *Config) GetBundleDirs() []string {
	var dirs []string
	for _, appPath := range c.AppPaths {
		bundleDir := paths.BundlesPath(appPath)
		if info, err := os.Stat(bundleDir); err == nil && info.IsDir() {
			dirs = append(dirs, bundleDir)
		}
	}
	return dirs
}

// SeededBundleLoader returns a bundles.Loader that sees fs-installed local
// bundles plus every remote bundle in the active lockfile, pre-loaded from
// the local git clone cache and SHA-pinned. This is the read-path loader
// every caller should use after PR 1: remote bundles no longer live on disk
// as extracted YAML, so the seeding step is what makes them visible at all.
//
// Failures are degraded gracefully (CLAUDE.md fault tolerance): a missing
// lockfile, unregistered remote, or single bad SHA produces a stderr
// warning and the loader returns the rest.
func (c *Config) SeededBundleLoader(preferDistilled bool, opts ...bundles.LoaderOption) *bundles.Loader {
	if seed := c.loadRemoteBundleSeed(); len(seed) > 0 {
		opts = append(append([]bundles.LoaderOption(nil), opts...), bundles.WithSeededBundles(seed))
	}
	return bundles.NewLoader(c.GetBundleDirs(), preferDistilled, opts...)
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

	registry, err := remote.NewRegistry(paths.RemotesPath(baseDir))
	if err != nil {
		return nil
	}
	lock, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		return nil
	}
	if lock.IsEmpty() {
		return nil
	}
	auth := remote.LoadAuth(baseDir)
	cache := remote.NewRepoCache(paths.ReposCachePath(baseDir), auth)
	factory := remote.NewCachedFetcherFactory(cache)
	// Wrap in the caching decorator so repeated SeededBundleLoader calls
	// within a session don't re-walk the clone for the same SHAs.
	reader := remote.NewCachingBundleReader(remote.NewBundleReader(registry, factory, auth, lock))

	rawBytes, failures := remote.LoadAllBytes(context.Background(), reader)
	for name, err := range failures {
		warnOncePerRun(fmt.Sprintf("ctxloom: warning: failed to load remote bundle %q from cache: %v\n", name, err))
	}

	loaded := make(map[string]*bundles.Bundle, len(rawBytes))
	for canonical, data := range rawBytes {
		entry, ok := lock.Bundles[canonical]
		if !ok {
			continue
		}
		b, perr := bundles.ParseBundle(data)
		if perr != nil {
			warnOncePerRun(fmt.Sprintf("ctxloom: warning: failed to parse remote bundle %q: %v\n", canonical, perr))
			continue
		}
		// Lockfile keys are canonical refs — the sole seed/resolution identity.
		b.Name = canonical
		b.Path = fmt.Sprintf("<remote>:%s@%s", canonical, entry.SHA)
		loaded[canonical] = b
	}
	return loaded
}

var (
	warnedOnceMu   sync.Mutex
	warnedOnceSeen = map[string]struct{}{}
)

// warnOncePerRun writes msg to stderr at most once per process for identical
// text, collapsing the duplicate remote-bundle warnings emitted when profile
// resolution re-seeds the same bundles several times during one startup.
func warnOncePerRun(msg string) {
	warnedOnceMu.Lock()
	defer warnedOnceMu.Unlock()
	if _, seen := warnedOnceSeen[msg]; seen {
		return
	}
	warnedOnceSeen[msg] = struct{}{}
	fmt.Fprint(os.Stderr, msg)
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
