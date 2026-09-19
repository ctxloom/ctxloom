// Package configload is the READING half of configuration: which directories
// participate (the project .ctxloom, else the user home), the home < project
// layering of config.yaml, the schema validation and in-memory upgrade of each
// layer, and the env / --config-set override chain. It implements
// config.Sources for the one config.Owner a process opens; core/config holds
// the value and the lifecycle and reads nothing itself.
//
// Everything that reaches beyond files — the remote readers behind the
// lockfile, companion probing, the executable trust gate — is a CONSTRUCTOR
// INPUT supplied by the composition root, so this package imports no sibling
// adapter to build a generation.
package configload

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/confload"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
	"github.com/ctxloom/ctxloom/resources"
)

// Option configures the Sources New builds.
type Option func(*Sources)

// WithFS reads through fs instead of the OS filesystem. Save and Update treat
// an injected filesystem as having no other process reading it.
func WithFS(fs afero.Fs) Option { return func(s *Sources) { s.fs = fs } }

// WithAppDir pins the .ctxloom directory instead of discovering it per Read:
// an explicit --app-dir, a worktree's .ctxloom, a test's fixture.
func WithAppDir(dir string) Option { return func(s *Sources) { s.appDir = dir } }

// WithOverrides replaces the override chain New computed from its flags and
// environment; the test seam for "these overrides, exactly".
func WithOverrides(o confload.Overrides) Option {
	return func(s *Sources) { s.overrides = o }
}

// WithProfileRefCanonicalizer supplies the step that rewrites an agent's
// profile refs to their canonical form during the in-memory upgrade of a
// layer. The remotes registry it consults lives under the shell config's
// appDir; the ref grammar belongs to adapters/remote, so the root wires it.
func WithProfileRefCanonicalizer(fn func(shell *config.Config, ref string) string) Option {
	return func(s *Sources) { s.canonicalize = fn }
}

// WithReaderSource adds a factory for bundle readers a generation's Catalog
// is resolved from, after the project and builtin readers: the lockfile's
// pinned remotes, every discovered companion's loadout.
func WithReaderSource(fn func(cfg *config.Config) []bundles.Reader) Option {
	return func(s *Sources) { s.readerSources = append(s.readerSources, fn) }
}

// WithExtraReaders appends fixed readers after every source; a test's fake
// bundle, an operation's ad-hoc tree.
func WithExtraReaders(readers ...bundles.Reader) Option {
	return func(s *Sources) { s.extraReaders = append(s.extraReaders, readers...) }
}

// WithTrustGate supplies the executable gate a generation's Trust wraps; the
// root wires the trust adapter's gate. Absent, a generation is ungated.
func WithTrustGate(fn func(cfg *config.Config) bundles.Authorizer) Option {
	return func(s *Sources) { s.trustGate = fn }
}

// Sources is the config.Sources a process builds ONCE at its composition
// root. Its flags and environment are captured at New; every Read applies
// the same overrides to whatever the files say now.
type Sources struct {
	fs     afero.Fs
	appDir string

	overrides     confload.Overrides
	validator     *schema.ConfigValidator
	validatorErr  error
	defaultConfig []byte

	canonicalize  func(shell *config.Config, ref string) string
	readerSources []func(cfg *config.Config) []bundles.Reader
	extraReaders  []bundles.Reader
	trustGate     func(cfg *config.Config) bundles.Authorizer
}

// New builds the process's Sources from its parsed flag set and environment
// (os.Environ() in production; nil means no environment overrides). The
// override chain is computed here, once, and never from a process global.
// A flag or env override that cannot be BOUND is returned as the error
// alongside a usable Sources: the root degrades it to a warning, and each
// individual override is still resolved (and warned about) per Read.
func New(flags *pflag.FlagSet, environ []string, opts ...Option) (*Sources, error) {
	s := &Sources{}
	s.validator, s.validatorErr = schema.NewConfigValidator()
	if s.validatorErr != nil {
		zap.L().Warn("failed to create config validator", zap.Error(s.validatorErr))
		s.validator = nil
	}
	if data, err := resources.GetDefaultConfig(); err == nil {
		s.defaultConfig = data
	}
	overrides, readErr := s.product().ReadOverrides(flags, environ)
	s.overrides = overrides
	for _, opt := range opts {
		opt(s)
	}
	return s, readErr
}

// Load is ONE read through a throwaway config.Owner — for tests, which need
// a Config from files (catalog and gate bound as a generation would bind
// them) without holding the owner. Production reaches configuration through
// the process's one config.Owner; tests/arch pins that this symbol appears
// in test files only.
func Load(opts ...Option) (*config.Config, error) {
	src, err := New(nil, nil, opts...)
	if err != nil {
		return nil, err
	}
	owner, err := config.Open(context.Background(), src)
	if err != nil {
		return nil, err
	}
	return owner.Current().Config, nil
}

// product describes ctxloom's own config to confload: the product name and
// dir/file names the bootstrap stage discovers, the CTXLOOM_CONFIG_ env
// prefix, the schema-derived KnownPath / ValidateValue hooks that
// distinguish a known override key from an unknown one, and the layer-scope
// policy (scopeAllows) that decides whether the env and flag layers may
// carry a given key at all.
func (s *Sources) product() confload.Product {
	p := confload.Product{
		Name:        "ctxloom",
		DirName:     config.AppDirName,
		FileName:    config.ConfigFileName,
		EnvPrefix:   "CTXLOOM_CONFIG_",
		ScopeAllows: scopeAllows,
		MergeFunc:   agentBindingMergeFunc,
	}
	if s.validator != nil {
		p.KnownPath = s.validator.KnownPath
		p.ValidateValue = s.validator.ValidateAt
	}
	return p
}

// Read resolves WHICH directories participate, then layers their values
// (home < project, deep-merged key by key; an explicitly set project value —
// including its zero value — always wins over an inherited one), applies the
// override chain, and overlays the shipped default registry where the user
// configured no LLMs. An ABSENT layer is the shipped default; a PRESENT
// layer that cannot be parsed is refused, naming the file.
func (s *Sources) Read(ctx context.Context) (*config.Config, []config.Warning, error) {
	fs := s.fs
	injectedFS := fs != nil
	if fs == nil {
		fs = afero.NewOsFs()
	}
	appDir, source := s.target(fs)
	b := config.NewBuilder(fs, injectedFS, appDir, source)
	if s.validatorErr != nil {
		// A schema-compile failure means every config in this process loads
		// with ZERO validation and every override is reclassified from
		// "known, set silently" to "unknown, warn". The embedded schema is a
		// build artifact, so this is a build defect: fatal-class, not a log line.
		b.Warn(config.WarnKindValidate,
			"config schema failed to compile — config validation and override-key checking are DISABLED for this process: %v", s.validatorErr)
	}
	projectConfigPath, homeConfigPath := resolveConfigLayerPaths(appDir, source)
	if err := s.loadLayeredConfig(ctx, b, homeConfigPath, projectConfigPath, fs, source); err != nil {
		return nil, nil, err
	}
	b.OverlayDefaultRegistry(s.defaultConfig)
	cfg := b.Build()
	return cfg, cfg.GetWarnings(), nil
}

// target is the bootstrap stage: WHICH .ctxloom directory this read layers
// over. A pinned appDir that IS the user home is home acting alone, not an
// arbitrary project — the write side (Save's layer-scope filter) keys on
// that distinction, so the read side must agree.
func (s *Sources) target(fs afero.Fs) (string, config.ConfigSource) {
	if s.appDir == "" {
		return findAppDir(fs)
	}
	if homeAppDir, err := paths.HomeConfigDir(); err == nil && filepath.Clean(s.appDir) == filepath.Clean(homeAppDir) {
		return s.appDir, config.SourceHome
	}
	return s.appDir, config.SourceProject
}

// Readers are the bundle sources of cfg's generation, in precedence order —
// a later reader wins a name collision, so pinned remote content shadows a
// stale extracted copy on disk and a companion's own ref, which nothing else
// can claim, comes last. The builtin reader's presence is what makes a
// builtin bundle resolvable BY REF; its position is name precedence only.
func (s *Sources) Readers(_ context.Context, cfg *config.Config) ([]bundles.Reader, error) {
	root := cfg.TrustRoot()
	readers := []bundles.Reader{
		bundles.NewProjectReader(cfg.FS(), cfg.BundleReaderDirs(), bundles.WithTrustRoot(root)),
		bundles.NewBuiltinReader(bundles.WithTrustRoot(root)),
	}
	for _, source := range s.readerSources {
		readers = append(readers, source(cfg)...)
	}
	return append(readers, s.extraReaders...), nil
}

// TrustPorts is the executable gate of cfg's generation.
func (s *Sources) TrustPorts(_ context.Context, cfg *config.Config) (bundles.Authorizer, error) {
	if s.trustGate == nil {
		return bundles.AdmitAll(), nil
	}
	gate := s.trustGate(cfg)
	if gate == nil {
		return nil, fmt.Errorf("configload: the trust gate produced no authorizer")
	}
	return gate, nil
}
