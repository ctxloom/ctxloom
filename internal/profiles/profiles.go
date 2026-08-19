package profiles

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// FragmentRef is a directory-profile fragment reference with optional priority —
// the profiles-package mirror of config.FragmentRef (the profiles package cannot
// import config, which imports profiles). It accepts the same YAML as the inline
// form: a bare string ("go-style") or a {name, priority} mapping.
// operations.resolveProfile converts it to config.FragmentRef for the shared
// assembly pipeline, where any "@<commit>" pin carried in Name is split
// transiently (normalizeFragmentRef) — so identity/lockfile stay version-agnostic,
// consistent with the inline path.
type FragmentRef struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority,omitempty"`
}

// UnmarshalYAML supports both the bare-string and {name, priority} forms,
// matching config.FragmentRef.
func (f *FragmentRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		f.Name = node.Value
		f.Priority = 0
		return nil
	}
	type plain FragmentRef
	return node.Decode((*plain)(f))
}

// MarshalYAML emits a bare string when priority is 0 (the common case), else the
// {name, priority} mapping — so a round-tripped profile stays compact.
func (f FragmentRef) MarshalYAML() (any, error) {
	if f.Priority == 0 {
		return f.Name, nil
	}
	type plain FragmentRef
	return plain(f), nil
}

// SeededProfilePathPrefix is the sentinel prefix carried in Profile.Path by
// seeded remote profiles (config.loadRemoteProfileSeed builds
// "<remote>:<canonical>@<sha>"). It is not a filesystem path: write paths use
// IsSeededPath to refuse to treat it as one — the profile-side mirror of
// bundles.seededPathPrefix.
const SeededProfilePathPrefix = "<remote>:"

// IsSeededPath reports whether path is the synthetic sentinel of a seeded
// remote profile rather than a real filesystem path.
func IsSeededPath(path string) bool {
	return strings.HasPrefix(path, SeededProfilePathPrefix)
}

// ResolveShortRefs rewrites this profile's bundle and parent references from
// short same-repo form to canonical form, resolved against sourceURL (the repo
// the profile was read from). Self-contained refs pass through unchanged. This
// is applied when seeding a remote profile so every downstream reader sees
// canonical refs — the profile's authored short refs only have meaning relative
// to its own source repo.
func (p *Profile) ResolveShortRefs(sourceURL, sourceHash string) {
	for i, b := range p.Bundles {
		p.Bundles[i] = remote.ResolveRefString(b, sourceURL, sourceHash, remote.ItemTypeBundle)
	}
	// A parent is either a bare local-profile name (passes through) or a
	// <bundle>#profiles/<name> bundle-shipped ref that canonicalizes against the
	// source repo exactly like Bundles/Commands (the "#profiles/<name>" selector
	// rides through ItemTypeBundle's item passthrough). Top-level @profiles/
	// parent distribution was retired, so ItemTypeProfile is no longer used here.
	for i, par := range p.Parents {
		p.Parents[i] = remote.ResolveRefString(par, sourceURL, sourceHash, remote.ItemTypeBundle)
	}
	// Curated command refs ("<bundle>#commands/<name>") carry a bundle
	// reference, so a remote profile's short command refs canonicalize against
	// its source repo exactly like Bundles (the "#commands/<name>" selector and
	// any trailing "@<commit>" pin ride through ResolveRefString's item
	// passthrough).
	for i, pr := range p.Commands {
		p.Commands[i] = remote.ResolveRefString(pr, sourceURL, sourceHash, remote.ItemTypeBundle)
	}
	// Curated skill refs ("<bundle>#skills/<name>") carry a bundle reference
	// exactly like Commands, so a remote profile's short skill refs canonicalize
	// against its source repo the same way.
	for i, sr := range p.Skills {
		p.Skills[i] = remote.ResolveRefString(sr, sourceURL, sourceHash, remote.ItemTypeBundle)
	}
	// Fragment refs and bundle_items cherry-picks both name bundle content, so a
	// remote profile's short forms canonicalize against its source repo exactly
	// like Bundles. Inline hooks:/mcp: are directly-declared executables with no
	// bundle reference to rewrite — they pass through unchanged.
	for i, fr := range p.Fragments {
		p.Fragments[i].Name = remote.ResolveRefString(fr.Name, sourceURL, sourceHash, remote.ItemTypeBundle)
	}
	for i, bi := range p.BundleItems {
		p.BundleItems[i] = remote.ResolveRefString(bi, sourceURL, sourceHash, remote.ItemTypeBundle)
	}
}

// Profile represents a named collection of fragments, bundles, and configuration.
// Profiles are stored as YAML files in .ctxloom/profiles/<name>.yaml
//
// # Content Reference Syntax
//
// Profiles use a standardized path syntax to reference content:
//
//	bundle-name                      # Entire bundle (all fragments, commands, MCP)
//	bundle-name:fragments/name       # Specific fragment from bundle
//	bundle-name:commands/name       # Specific command from bundle
//	bundle-name:mcp                  # MCP server from bundle
//	remote/bundle-name:fragments/x   # Fragment from remote bundle
//
// Legacy syntax (for backwards compatibility):
//
//	fragment-name                    # Standalone fragment file
type Profile struct {
	Name string `yaml:"-"` // Derived from filename
	Path string `yaml:"-"` // Full path to the file
	// Signer is the VERIFIED publisher identity of the bundle this profile
	// was shipped inside (bundles.Bundle.Signer(), copied at seed time by
	// config.loadBundleProfileSeed) — empty for a genuinely local/
	// project-authored profile, or for a bundle-shipped profile whose bundle
	// is unsigned/untrusted. Read-only derived data (yaml:"-"), the
	// profile-side mirror of Name/Path: a profile file can never set its own
	// Signer, exactly like a bundle file can never set Bundle.signer.
	Signer      string   `yaml:"-"`
	Description string   `yaml:"description,omitempty"`
	Parents     []string `yaml:"parents,omitempty"`     // Parent profiles to inherit from
	Tags        []string `yaml:"tags,omitempty"`        // Descriptive tags (listing/discovery only; NOT content-selecting)
	SelectTags  []string `yaml:"select_tags,omitempty"` // Fragment tags to select content by

	// LLM is the config label (or backend type) this profile prefers to launch.
	// Used by `ctxloom run` unless overridden by -l/--llm. Empty falls back to
	// the configured primary role.
	LLM string `yaml:"llm,omitempty"`

	// Bundles are content references using standardized path syntax
	// Examples: "go-development", "go-development#fragments/testing", "github/security#mcp"
	// Full URLs: "https://github.com/user/repo@bundles/name"
	Bundles []string `yaml:"bundles,omitempty"`

	// Commands curates the slash-command exports for this directory profile,
	// the mirror of config.Profile.Commands for inline profiles (D2).
	// When a resolved active profile declares a NON-EMPTY list, ONLY these
	// commands are exported (each optionally version-pinned with a trailing
	// "@<commit>"), suppressing the global flag-based auto-export for that
	// profile; an empty list keeps today's global auto-export (opt-in). Each
	// entry is a command ref ("<bundle>#commands/<name>") whose version-agnostic
	// identity is the stored string — like bundles, any "@<commit>" is parsed
	// transiently at assembly and the lockfile stays untouched.
	Commands []string `yaml:"commands,omitempty"`

	// Skills curates the Agent Skill exports for this directory profile, the
	// mirror of config.Profile.Skills for inline profiles (B6a). When a
	// resolved active profile declares a NON-EMPTY list, ONLY these skills are
	// exported per-engine (force-enabled), suppressing the global bundle-wide
	// auto-export for that profile; an empty list keeps today's global
	// auto-export. Each entry is a skill ref ("<bundle>#skills/<name>") — no
	// version pin (a skill has no historical-content resolution).
	Skills []string `yaml:"skills,omitempty"`

	// Fragments are direct fragment references with optional priority — the
	// mirror of config.Profile.Fragments for inline profiles. Each entry may
	// carry a trailing "@<commit>" pin (split transiently at assembly), so the
	// stored ref stays the version-agnostic identity and the lockfile is
	// untouched, exactly like the inline path.
	Fragments []FragmentRef `yaml:"fragments,omitempty"`

	// BundleItems cherry-pick individual bundle items (e.g.
	// "remote/bundle:fragments/name", optionally "@<commit>"-pinned) — the mirror
	// of config.Profile.BundleItems. They are expanded via the same
	// ExpandBundleRefs path as Bundles in operations.resolveProfile.
	BundleItems []string `yaml:"bundle_items,omitempty"`

	// Hooks are lifecycle hooks declared by this directory profile, the mirror of
	// config.Profile.Hooks. Unlike a trusted-local config.yaml inline profile, a
	// directory profile may be remote-sourced (a seeded remote profile), so its
	// directly-declared executable hooks pass the per-item executable trust gate
	// (the SAME gate bundle hooks pass) before reaching backend settings.
	Hooks wire.HooksConfig `yaml:"hooks,omitempty"`

	// MCP are MCP servers declared by this directory profile, the mirror of
	// config.Profile.MCP. Like Hooks, these directly-declared executables pass the
	// executable trust gate before reaching backend settings.
	MCP wire.MCPConfig `yaml:"mcp,omitempty"`

	Variables map[string]string `yaml:"variables,omitempty"`

	// Exclusions - items to filter out after inheritance resolution
	ExcludeFragments []string `yaml:"exclude_fragments,omitempty"`
	ExcludeMCP       []string `yaml:"exclude_mcp,omitempty"`

	// DenyTools is the directory-profile mirror of config.Profile.DenyTools:
	// per-engine tool identifiers this profile denies at launch (e.g. "Task"
	// for Claude Code's built-in sub-agent tool). Accumulates through parent
	// inheritance like ExcludeMCP (a child cannot un-deny what a parent
	// denied) and is never gated — it only narrows what a launch may do,
	// never runs anything.
	DenyTools []string `yaml:"deny_tools,omitempty"`
}

// Loader handles loading profiles from .ctxloom/profiles directories.
type Loader struct {
	dirs []string
	fs   afero.Fs
	// remoteResolver maps a profile load name to the short remote name it was
	// installed from, used to canonicalize bundle refs on load (see upgrade.go).
	// Nil means no resolution — the loader behaves as a plain reader.
	remoteResolver func(profileName string) string
	// remoteURLResolver maps a remote alias to its canonical repo URL. Paired with
	// remoteResolver, it lets the loader rewrite a profile's bare/alias bundle refs
	// to their canonical URL form on load. Nil means no canonicalization.
	remoteURLResolver func(alias string) string
	// pending accumulates in-memory schema upgrades applied during Load that an
	// interactive caller may persist with consent (see PendingUpgrades).
	// pendingPaths dedupes by file path: a legacy file loaded several times in
	// one run (e.g. a shared parent profile) must yield one pending upgrade,
	// not one consent prompt per load.
	pending      []*upgrade.Pending
	pendingPaths map[string]bool
	// seeded holds remote profiles read from the git clone cache at their locked
	// SHA, indexed by canonical ref. Remote profiles are pure references (never
	// materialized to disk), so they enter the loader only via this seed — the
	// profile-side mirror of bundles.WithSeededBundles. Seeded entries are
	// returned ahead of any fs lookup.
	seeded map[string]*Profile
}

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithFS sets a custom filesystem implementation (for testing).
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// WithRemoteResolver sets the function that maps a profile load name to the
// short remote name it was installed from. Paired with WithRemoteURLResolver, the
// loader uses it to canonicalize bare/alias bundle refs in legacy profiles on
// load. Without it, profiles load verbatim.
func WithRemoteResolver(resolve func(profileName string) string) LoaderOption {
	return func(l *Loader) {
		l.remoteResolver = resolve
	}
}

// WithRemoteURLResolver sets the function that maps a remote alias to its
// canonical repo URL. The loader uses it (with WithRemoteResolver) to rewrite a
// legacy profile's bare/alias bundle refs to canonical URL form on load. Without
// it, no canonicalization is attempted.
func WithRemoteURLResolver(resolve func(alias string) string) LoaderOption {
	return func(l *Loader) {
		l.remoteURLResolver = resolve
	}
}

// WithSeededProfiles pre-populates the loader with parsed remote profiles
// indexed by canonical ref. Seeded entries win over fs hits with the same name,
// mirroring bundles.WithSeededBundles: remote profiles served from the git clone
// cache (see remote.ProfileReader) are referenced, never materialized to disk,
// so they reach the loader only through this seed. Each call merges its map in.
func WithSeededProfiles(seeded map[string]*Profile) LoaderOption {
	return func(l *Loader) {
		if l.seeded == nil {
			l.seeded = make(map[string]*Profile, len(seeded))
		}
		maps.Copy(l.seeded, seeded)
	}
}

// lookupSeeded returns the seeded profile for name, if any. Seeded profiles
// are keyed by their version-less canonical ref (the lockfile key shape), so a
// URL ref carrying a content version ("...@<sha>") is normalized to that form
// when the exact lookup misses — the profile-side mirror of
// bundles.Loader.lookupSeeded.
func (l *Loader) lookupSeeded(name string) (*Profile, bool) {
	if l.seeded == nil {
		return nil, false
	}
	if p, ok := l.seeded[name]; ok {
		return p, true
	}
	// A bundle-profile ref canonicalizes selector-preserving: CanonicalKey
	// would drop the "#profiles/<name>" selector and collapse the ref to its
	// bundle, never matching a seed key.
	if key, ok := remote.CanonicalProfileKey(name); ok && key != name {
		if p, ok := l.seeded[key]; ok {
			return p, true
		}
		// "<alias>/<bundle>#profiles/<name>": the pure-string canonicalizer
		// reads the alias as a local path segment; resolve it against the
		// remote registry so the alias spelling reaches the same seed entry
		// as the canonical URL.
		if aliasKey, ok := l.aliasSeededKey(name); ok {
			p, ok := l.seeded[aliasKey]
			return p, ok
		}
		return nil, false
	}
	if key, ok := remote.CanonicalKey(name); ok && key != name {
		p, ok := l.seeded[key]
		return p, ok
	}
	return nil, false
}

// canonicalProfileName returns the version-less canonical identity of a
// profile reference, for recursion and visited-map dedup. A bundle-profile
// ref resolves alias-first (so "<alias>/<bundle>#profiles/<p>" and its
// canonical URL spelling share one identity), then through the
// selector-preserving CanonicalProfileKey — CanonicalKey would drop the
// "#profiles/<name>" selector and collapse the ref to its bundle, which is
// never a profile name. Non-selector refs and plain local names pass through
// CanonicalKey / unchanged.
func (l *Loader) canonicalProfileName(ref string) string {
	if _, _, ok := remote.SplitBundleProfileRef(ref); ok {
		if key, ok := l.aliasSeededKey(ref); ok {
			return key
		}
		if key, ok := remote.CanonicalProfileKey(ref); ok {
			return key
		}
		return ref
	}
	if key, ok := remote.CanonicalKey(ref); ok {
		return key
	}
	return ref
}

// aliasSeededKey resolves an "<alias>/<bundle>#profiles/<name>" reference to
// its canonical seed key via the remote registry. The bare-name grammar is
// untouched: a parent or -p WITHOUT a "#profiles/" selector never reaches
// here, so a local profile in a subdirectory (e.g. "personal/go-developer")
// keeps winning for selector-less names. ok is false when the loader has no
// registry resolver, the ref carries no selector, the bundle part is already
// canonical or ctxloom:local, or the alias names no configured remote.
func (l *Loader) aliasSeededKey(name string) (string, bool) {
	if l.remoteURLResolver == nil {
		return "", false
	}
	bundle, _, ok := remote.SplitBundleProfileRef(name)
	if !ok || remote.IsCanonicalRef(bundle) || strings.HasPrefix(bundle, remote.LocalSource) {
		return "", false
	}
	alias, rest, found := strings.Cut(bundle, "/")
	if !found || alias == "" || rest == "" {
		return "", false
	}
	url := l.remoteURLResolver(alias)
	if url == "" {
		return "", false
	}
	candidate := url + "@" + remote.ItemTypeBundle.DirName() + "/" + rest + name[strings.Index(name, remote.ProfileSelector):]
	return remote.CanonicalProfileKey(candidate)
}

// PendingUpgrades returns the in-memory schema upgrades Load applied to older
// profile files this loader has not persisted. An interactive caller prompts the
// user and calls CommitUpgrade to make a rewrite permanent (see cmd/run.go).
// Empty when every loaded profile was already current.
func (l *Loader) PendingUpgrades() []*upgrade.Pending {
	return l.pending
}

// CommitUpgrade persists a pending profile upgrade, writing the upgraded bytes
// verbatim so comments and key order preserved by the node rewrite survive, and
// drops it from the pending set. Callers prompt the user before invoking this
// (see cmd/run.go); ctxloom never rewrites a profile without consent.
func (l *Loader) CommitUpgrade(p *upgrade.Pending) error {
	// Nothing to write is not a successful write. The caller has just asked
	// the user to consent to a rewrite, so a nil return here means that
	// rewrite landed; an empty payload would land as a zero-byte profile.
	if p == nil {
		return fmt.Errorf("no pending profile upgrade to commit")
	}
	if p.Path == "" {
		return fmt.Errorf("pending profile upgrade has no path")
	}
	if len(p.Data) == 0 {
		return fmt.Errorf("pending upgrade for profile %s carries no content; refusing to truncate it", p.Path)
	}
	if err := afero.WriteFile(l.fs, p.Path, p.Data, 0o644); err != nil {
		return fmt.Errorf("write upgraded profile %s: %w", p.Path, err)
	}
	for i, pending := range l.pending {
		if pending == p {
			l.pending = append(l.pending[:i], l.pending[i+1:]...)
			break
		}
	}
	return nil
}

// NewLoader creates a new profile loader.
func NewLoader(dirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		dirs: dirs,
		fs:   afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// List returns all available profiles (searches subdirectories recursively).
func (l *Loader) List() ([]*Profile, error) {
	var profiles []*Profile
	seen := make(map[string]bool)

	// Seeded remote profiles are emitted first — they carry a non-fs Path (the
	// canonical ref) so write paths never touch them. This is NOT a precedence
	// guard against the fs loop below: a seed key always contains "#profiles/"
	// (built by remote.BundleProfileRef), while the fs loop keys seen by a
	// .yaml-stripped relative path that never contains that substring, so
	// `seen[profileName]` there can never match a seeded entry — both a
	// same-named seeded and on-disk profile are emitted, unlike Load's
	// (lookupSeeded) precedence.
	for _, p := range l.seeded {
		profiles = append(profiles, p)
	}

	for _, dir := range l.dirs {
		exists, err := afero.DirExists(l.fs, dir)
		if err != nil {
			// A directory that cannot be interrogated is not an empty one.
			// Degrading silently here reports "you have no profiles" for a
			// machine whose profiles are all present but unreachable.
			clidiag.Warn("ctxloom", "skipping profiles directory %s: %v", dir, err)
			continue
		}
		if !exists {
			continue
		}

		err = afero.Walk(l.fs, dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Same reasoning one level down: skip the entry, but say which
				// one and why, or the profiles under it vanish undiagnosably.
				clidiag.Warn("ctxloom", "skipping unreadable profiles path %s: %v", path, err)
				return nil
			}
			if info.IsDir() {
				return nil
			}
			name := info.Name()
			if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
				return nil
			}

			// Use relative path from dir as profile name (e.g., "github/go-developer")
			relPath, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				// Walk derives every path from dir, so this cannot fire today;
				// the guard exists because the alternative is a profile named
				// "" — which sorts first and addresses nothing.
				clidiag.Warn("ctxloom", "skipping profile %s: cannot derive a name relative to %s: %v", path, dir, relErr)
				return nil
			}
			profileName := strings.TrimSuffix(strings.TrimSuffix(relPath, ".yaml"), ".yml")
			// Normalize path separators to forward slashes for consistency
			profileName = filepath.ToSlash(profileName)

			if seen[profileName] {
				return nil // First directory wins
			}
			seen[profileName] = true

			profile, err := l.loadFile(path, l.remoteFor(profileName))
			if err != nil {
				// Degrade, but say so: a corrupt profile silently vanishing
				// from list output is undiagnosable.
				clidiag.Warn("ctxloom", "skipping profile %s: %v", path, err)
				return nil
			}
			profile.Name = profileName
			profiles = append(profiles, profile)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan profiles directory %s: %w", dir, err)
		}
	}

	// Sort by name
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})

	return profiles, nil
}

// Load loads a profile by name (supports subdirectory paths like
// "github/profile-name"). URL refs are normalized at this boundary so every
// caller benefits: a seeded remote profile is found under its version-less
// canonical key (lockfiles and the seed map both use that shape), and a URL
// ref with no seed entry falls back to its local materialized name (e.g.
// "github.com/owner/repo/name") for the fs lookup.
//
// # Ownership
//
// A returned profile is READ-ONLY to the caller. The two sources differ in
// what that costs to violate, so it is stated rather than left to be
// discovered: a filesystem profile is parsed afresh on every call, while a
// SEEDED (bundle-shipped) profile is the one shared instance every reader in
// this run receives — it is a reference with no local file, so there is
// nothing to re-read it from. Mutating one would corrupt the seed for every
// later reader and then evaporate on the next pull, which is why every write
// path (Save, Delete, and operations' edit/import flows) refuses a seeded
// profile BEFORE mutating anything rather than at the moment of writing.
func (l *Loader) Load(name string) (*Profile, error) {
	// Seeded remote profiles win over any fs lookup.
	if p, ok := l.lookupSeeded(name); ok {
		return p, nil
	}

	// A bundle-profile ref ("...#profiles/<name>") or scheme-qualified remote
	// ref that missed the seed can never resolve from disk: bundle profiles
	// exist only via the lockfile-built seed map. Say so — the bare "not
	// found" otherwise reads as "the profile doesn't exist upstream". ('#' is
	// reserved in local profile names, so the selector is unambiguous.)
	if strings.Contains(name, remote.ProfileSelector) || remote.IsCanonicalRef(name) {
		return nil, fmt.Errorf("%w: %s (bundle profile has no lockfile entry — run 'ctxloom deps pull')", errs.ErrProfileNotFound, name)
	}

	// Names reach here from MCP tools and CLI args, so the local form must be
	// confined to the profiles directories before it is joined into a path —
	// the same guard Save applies (and the profile-side mirror of
	// bundles.Loader.Find validating in the read path).
	// The name is used as-is: a bundle-shipped (seeded) profile is resolved by
	// lookupSeeded before this point, and top-level "<url>@profiles/"
	// distribution was retired, so there is no URL form left to convert.
	localName := name
	if err := validateProfileName(localName); err != nil {
		return nil, err
	}

	path, ok := l.findFile(localName)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errs.ErrProfileNotFound, name)
	}
	profile, err := l.loadFile(path, l.remoteFor(name))
	if err != nil {
		return nil, err
	}
	profile.Name = name
	return profile, nil
}

// findFile resolves a validated local profile name to the file backing it,
// searching the configured dirs in order and both extensions. Shared by Load
// and Exists so the two can never disagree about which names have a file —
// "is a profile stored here" is one question with one answer, independent of
// whether the profile happens to parse.
func (l *Loader) findFile(localName string) (string, bool) {
	// Convert forward slashes to OS-specific separator for file lookup
	osName := filepath.FromSlash(localName)
	for _, dir := range l.dirs {
		for _, ext := range []string{".yaml", ".yml"} {
			path := filepath.Join(dir, osName+ext)
			if _, err := l.fs.Stat(path); err == nil {
				return path, true
			}
		}
	}
	return "", false
}

// Exists reports whether a profile is STORED under name — a seeded
// bundle-shipped profile, or a file in one of the profile directories. It
// deliberately does not require the profile to parse: callers use Exists to
// decide whether a name is free (operations.CreateProfile) or whether a
// declared parent is present (requireProfilesExist), and answering "no" for a
// profile that is there but malformed makes the first of those overwrite the
// user's file. A name that can never have a file behind it — traversal,
// a bundle-profile selector, a remote URL with no seed entry — is false.
func (l *Loader) Exists(name string) bool {
	if _, ok := l.lookupSeeded(name); ok {
		return true
	}
	if strings.Contains(name, remote.ProfileSelector) || remote.IsCanonicalRef(name) {
		return false
	}
	if err := validateProfileName(name); err != nil {
		return false
	}
	_, ok := l.findFile(name)
	return ok
}

// remoteFor returns the short remote name a profile was installed from, or ""
// when no resolver is configured or the profile is local.
func (l *Loader) remoteFor(name string) string {
	if l.remoteResolver == nil {
		return ""
	}
	return l.remoteResolver(name)
}

func (l *Loader) loadFile(path, remoteAlias string) (*Profile, error) {
	data, err := afero.ReadFile(l.fs, path)
	if err != nil {
		return nil, err
	}

	// Resolution context for canonicalizing legacy bundle refs (see upgrade.go):
	// ownURL is the profile's own remote repo URL (for bare sibling refs), and
	// l.remoteURLResolver maps any alias to its repo URL (for "<alias>/<bundle>"
	// refs). Both are nil/empty for a local project profile, which then loads
	// verbatim.
	var ownURL string
	if l.remoteURLResolver != nil && remoteAlias != "" {
		ownURL = l.remoteURLResolver(remoteAlias)
	}

	// Upgrade older on-disk schema (e.g. bare/alias bundle refs, retired
	// @profiles/ parents) to the current canonical form in memory before
	// unmarshaling. Applied upgrades are recorded as pending so an interactive
	// caller may persist the rewrite with consent (see upgrade.go). The seeded
	// map is the discovery surface for retired-parent rewrites — it is populated
	// at construction (WithSeededProfiles), before any Load reaches here.
	if upgraded, applied := profileUpgrades(ownURL, l.remoteURLResolver, l.seeded).Run(data); len(applied) > 0 {
		data = upgraded
		if !l.pendingPaths[path] {
			if l.pendingPaths == nil {
				l.pendingPaths = make(map[string]bool)
			}
			l.pendingPaths[path] = true
			l.pending = append(l.pending, &upgrade.Pending{Path: path, Data: upgraded, Applied: applied})
		}
	}

	var profile Profile
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	// A zero-byte, `{}`, or fully-commented-out profile parses cleanly into a
	// profile that selects NOTHING, and used to load with err=nil and record
	// zero strictness findings — a session launches on it, gets no context at
	// all, and every surface reports success. It is reported
	// through the fail-loudly gate rather than as a hard load error: `List`
	// and the pickers must still be able to ENUMERATE a hollow profile (a
	// half-authored one is a normal intermediate state), but nothing may
	// launch on one while pretending it composed something.
	if !profile.HasContent() {
		strictness.FailOnce(strictness.ClassConfig, "give the profile something to select (parents, bundles, fragments, select_tags) or delete it",
			"profile %s selects nothing: no parents, bundles, fragments, bundle_items, commands, skills, select_tags, hooks, mcp, variables or llm — a session launched on it composes no context", path)
	}
	profile.Path = path
	return &profile, nil
}

// HasContent reports whether a profile actually selects or declares anything.
// Description/Tags alone do not count: they are labels, not content, and a
// profile carrying only labels composes exactly nothing.
func (p *Profile) HasContent() bool {
	return len(p.Parents) > 0 ||
		len(p.SelectTags) > 0 ||
		len(p.Bundles) > 0 ||
		len(p.Commands) > 0 ||
		len(p.Skills) > 0 ||
		len(p.Fragments) > 0 ||
		len(p.BundleItems) > 0 ||
		len(p.ExcludeFragments) > 0 ||
		len(p.ExcludeMCP) > 0 ||
		len(p.DenyTools) > 0 ||
		len(p.Variables) > 0 ||
		len(p.Hooks.Unified.PreTool)+len(p.Hooks.Unified.PostTool)+
			len(p.Hooks.Unified.SessionStart)+len(p.Hooks.Unified.SessionEnd)+
			len(p.Hooks.Unified.PreShell)+len(p.Hooks.Unified.PostFileEdit) > 0 ||
		len(p.Hooks.Plugins) > 0 ||
		len(p.MCP.Servers) > 0 ||
		p.LLM != ""
}

// IsEmptyDocument reports whether a profile carries NOTHING — no selection and
// not even a label. This is the shape that serializes to "{}\n": it can only
// ever compose nothing, and there is no half-authored reading of it. It is the
// refusal line every profile WRITE shares (Save, and operations' import /
// edit-write-back / export), kept here so the three cannot drift apart.
//
// A profile carrying only labels is deliberately NOT this: it is a normal
// half-authored state, so it saves and the fail-loudly gate says what it will
// (not) do.
func (p *Profile) IsEmptyDocument() bool {
	return !p.HasContent() && p.Description == "" && len(p.Tags) == 0
}

// Save saves a profile to disk.
//
// Seeded remote profiles are rejected: their Path is the "<remote>:" sentinel
// (not a filesystem path), and writing one anywhere locally would silently
// fork it from its source — the edit would evaporate on the next pull.
func (l *Loader) Save(profile *Profile) error {
	if len(l.dirs) == 0 {
		return fmt.Errorf("no profiles directory configured")
	}
	if IsSeededPath(profile.Path) {
		return fmt.Errorf("profile %q is a remote profile and read-only; edit it at its source and run 'ctxloom deps pull'", profile.Name)
	}
	if err := validateProfileName(profile.Name); err != nil {
		return err
	}
	// A profile with NOTHING in it — no selection and not even a description
	// or tags — serializes to "{}\n", and writing that reported success while
	// creating a file that can only ever compose nothing. Refuse
	// it. A profile that carries labels but selects nothing is a normal
	// half-authored state: it saves, and the fail-loudly gate says what it
	// will (not) do, exactly as Load does for the same shape.
	if !profile.HasContent() {
		if profile.IsEmptyDocument() {
			return fmt.Errorf("profile %q is empty: refusing to write a profile with no content at all (add parents, bundles, fragments, select_tags or an llm)", profile.Name)
		}
		strictness.FailOnce(strictness.ClassConfig, "give the profile something to select (parents, bundles, fragments, select_tags)",
			"profile %q selects nothing: it carries only labels, so a session launched on it composes no context", profile.Name)
	}

	// A profile loaded from disk saves back to its own file (so a .yml
	// profile round-trips instead of duplicating as .yaml); a new profile
	// writes under the first directory. Subdir-qualified names — the form
	// List produces — get their intermediate directories created.
	path := profile.Path
	if path == "" {
		path = filepath.Join(l.dirs[0], filepath.FromSlash(profile.Name)+".yaml")
	}
	if err := l.fs.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create profiles directory: %w", err)
	}

	// Create a copy without Name and Path for serialization
	toSave := *profile
	toSave.Name = ""
	toSave.Path = ""

	data, err := yaml.Marshal(&toSave)
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	if err := afero.WriteFile(l.fs, path, data, 0644); err != nil {
		return fmt.Errorf("failed to write profile: %w", err)
	}

	profile.Path = path
	return nil
}

// validateProfileName rejects names that would escape the profiles directory
// when joined into a path. The check is the cleaned-relative form (not a
// naive ".." prefix test), so legitimate names like "..hidden" pass while
// "../x" and absolute paths are rejected. This is the profile-side mirror of
// bundles.ValidateBundleName.
func validateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	// '#' is reserved for bundle-item selectors, so a "<bundle>#profiles/<n>"
	// ref is structurally never a local profile file — the seed intercept in
	// Load is a guarantee, not a coincidence.
	if strings.Contains(name, "#") {
		return fmt.Errorf("invalid profile name %q: '#' is reserved for bundle refs (<bundle>#profiles/<name>)", name)
	}
	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid profile name %q: must stay within the profiles directory", name)
	}
	return nil
}

// Delete removes a profile file. Load validates the name (traversal names are
// rejected before any path join) and resolves the path inside the profiles
// dirs; seeded remote profiles are read-only references with no local file to
// remove.
func (l *Loader) Delete(name string) error {
	profile, err := l.Load(name)
	if err != nil {
		return err
	}
	if IsSeededPath(profile.Path) {
		return fmt.Errorf("profile %q is a remote profile and read-only; remove its lockfile entry instead (see 'ctxloom remote')", profile.Name)
	}
	return l.fs.Remove(profile.Path)
}

// GetProfileDirs returns the existing profile directories under the given
// ctxloom paths, resolved against fs. A nil fs means the real OS filesystem
// (the production default).
//
// fs is a parameter rather than an os.Stat because discovery must follow the
// SAME filesystem the Loader reads: statting the real disk unconditionally made
// every injected fs a lie — a profile written to a MemMapFs was never found —
// which is why callers throughout the tree had to fall back to real tempdirs.
func GetProfileDirs(fs afero.Fs, scmPaths []string) []string {
	if fs == nil {
		fs = afero.NewOsFs()
	}
	var dirs []string
	for _, scmPath := range scmPaths {
		profileDir := paths.ProfilesPath(scmPath)
		if isDir, err := afero.DirExists(fs, profileDir); err == nil && isDir {
			dirs = append(dirs, profileDir)
		}
	}
	return dirs
}

// maxProfileDepth prevents stack overflow from deeply nested or malformed configurations.
// This matches the limit used in config.ResolveProfile for consistency.
const maxProfileDepth = 64

// ResolveProfile resolves a profile including its parents, returning all referenced items.
// Returns bundles, tags, and variables.
// Uses the same algorithm as config.ResolveProfile for consistency:
// - Clones visited set for each parent to handle diamond inheritance correctly
// - Enforces depth limit to prevent stack overflow
func (l *Loader) ResolveProfile(name string, visited map[string]bool) (*ResolvedProfile, error) {
	// memo is the per-call result cache that turns diamond-shaped
	// parent inheritance from exponential (each shared ancestor re-Load()ed,
	// re-parsed, and re-resolved once per path to it) into linear (each
	// distinct profile resolved once, however many branches reach it). It is
	// SEPARATE from visited: visited stays per-branch for cycle detection
	// (cloned at each parent, per resolveProfileRecursive's existing
	// doc); memo is shared across the whole call and keyed by the exact
	// name string each recursive call receives, matching visited's own
	// keying so the two stay consistent.
	// The caller's spelling is canonicalized exactly as every recursive step
	// canonicalizes a parent, so the top frame shares one identity with the
	// frames below it: visited and memo are keyed the same way at every depth.
	// A raw spelling here would be a SECOND identity for a profile the
	// recursion already knows by its canonical key, and the memo would miss.
	return l.resolveProfileRecursive(l.canonicalProfileName(name), visited, 0, make(map[string]*ResolvedProfile))
}

func (l *Loader) resolveProfileRecursive(name string, visited map[string]bool, depth int, memo map[string]*ResolvedProfile) (*ResolvedProfile, error) {
	// Check depth limit (consistent with config.ResolveProfile)
	if depth > maxProfileDepth {
		return nil, fmt.Errorf("%w (%d): possible misconfiguration", errs.ErrProfileDepthExceeded, maxProfileDepth)
	}

	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[name] {
		return nil, fmt.Errorf("%w: %s", errs.ErrCircularInheritance, name)
	}
	visited[name] = true

	// A shared ancestor reached through a second (or third, or
	// Nth) branch is resolved once, not re-Load()ed/re-parsed/re-resolved
	// per path — this is what turns the diamond-inheritance case from
	// Θ(2^n) into Θ(n). Only reachable here for a node NOT already in the
	// current branch's own path (the visited/cycle check above always runs
	// first), so a cache hit can never mask a genuine cycle.
	if cached, ok := memo[name]; ok {
		return cached, nil
	}

	profile, err := l.Load(name)
	if err != nil {
		return nil, err
	}

	resolved := &ResolvedProfile{
		Variables: make(map[string]string),
	}
	// SourceRef/Signer are THIS profile's own provenance — never inherited
	// from (or overwritten by) a parent's Merge below: a profile's
	// directly-declared hooks/mcp must key the executable
	// trust gate by ITS OWN origin, not a parent's. profile.Name is already
	// the canonical identity here — a bare local name for a genuinely local
	// profile, or the "<bundle>#profiles/<name>" seed key
	// (config.loadBundleProfileSeed) for a bundle-shipped one — so deriving
	// from it needs no re-canonicalization.
	if bundle, _, ok := remote.SplitBundleProfileRef(profile.Name); ok {
		resolved.SourceRef = remote.CanonicalBundleRef(bundle)
		resolved.Signer = profile.Signer
	}

	// Resolve parents first (depth-first)
	// Clone visited map for each parent to handle diamond inheritance correctly.
	// This allows shared ancestors to be resolved through different paths.
	//
	// An unresolvable parent is fatal-class in strict mode (fail-loudly): the
	// branch is skipped so the rest of the profile still resolves, the warning
	// streams either way, and the startup choke owner aborts on the collected
	// finding. In degraded mode (--degraded / CTXLOOM_DEGRADED=1) this is pure
	// warn-and-skip so the user still reaches their LLM. Circular references
	// and depth-limit overruns remain hard errors in both modes because
	// continuing would mask a real misconfiguration or risk runaway recursion.
	for _, parent := range profile.Parents {
		// Load normalizes the ref (seeded remote bundles are keyed by their
		// version-less canonical ref; unseeded URL refs fall back to the local
		// materialized name), so the parent ref recurses as-is. Canonicalize
		// the recursion name so the visited map treats a bundle-shipped parent
		// "<bundle>#profiles/p", its "@<sha>"-pinned form, and its
		// "<alias>/<bundle>#profiles/p" spelling as the same profile.
		parentName := l.canonicalProfileName(parent)

		// Clone visited map for this parent branch
		parentVisited := cloneVisited(visited)
		parentResolved, err := l.resolveProfileRecursive(parentName, parentVisited, depth+1, memo)
		if err != nil {
			switch {
			case errors.Is(err, errs.ErrCircularInheritance), errors.Is(err, errs.ErrProfileDepthExceeded):
				// Structural misconfiguration stays fatal: continuing would mask
				// a real cycle or risk runaway recursion.
				return nil, fmt.Errorf("failed to resolve parent %s: %w", parent, err)
			case errors.Is(err, errs.ErrProfileNotFound):
				// FailOnce: resolution re-runs in every subsystem that builds a
				// loader (assembly, hooks, MCP, ...), so an unresolvable parent
				// would otherwise repeat the same line — and finding — a dozen
				// times per startup.
				if _, bare, retired := remote.SplitRetiredProfileRef(parent); retired {
					// The retired top-level @profiles/ grammar can never pull or
					// pin, so "deps pull"/"deps upgrade" would both be wrong
					// advice. The load-time upgrade rewrites this parent
					// automatically once a bundle shipping the profile is
					// installed — so the fix is to point the parent at the
					// bundle-shipped form (or install a bundle that ships it).
					strictness.FailOnce(strictness.ClassRef, "point the parent at \"<url>@bundles/<bundle>#profiles/<name>\", or install a bundle that ships it",
						"profile %q: parent %s uses the retired top-level @profiles/ grammar and no installed bundle ships profile %q; point the parent at \"<url>@bundles/<bundle>#profiles/<name>\", or install a bundle that ships it (the load-time upgrade then rewrites the parent automatically)",
						name, parent, bare)
				} else {
					strictness.FailOnce(strictness.ClassRef, "ctxloom deps pull",
						"profile %q: parent %s not installed; skipping (run `ctxloom deps pull` to install)",
						name, parent)
				}
			default:
				// Corrupt parent (invalid YAML, IO/permission error): skip this
				// branch rather than aborting the whole resolution — degraded
				// mode still reaches the LLM; strict mode aborts on the finding.
				strictness.FailOnce(strictness.ClassRef, "fix or remove the parent profile file",
					"profile %q: parent %s failed to load (%v); skipping",
					name, parent, err)
			}
			continue
		}
		resolved.Merge(parentResolved)
	}

	// Then apply this profile's settings (overrides parents)
	resolved.Bundles = appendUnique(resolved.Bundles, profile.Bundles...)
	resolved.Tags = appendUnique(resolved.Tags, profile.Tags...)
	resolved.SelectTags = appendUnique(resolved.SelectTags, profile.SelectTags...)
	// Curated commands union with parents in declaration order, deduped by
	// their version-agnostic stored ref — the directory-profile mirror of how
	// inline profiles fold Commands in config_resolve.mergeProfileValues.
	resolved.Commands = appendUnique(resolved.Commands, profile.Commands...)
	// Curated skills union with parents in declaration order, deduped by their
	// stored ref — the skill mirror of the Commands fold immediately above.
	resolved.Skills = appendUnique(resolved.Skills, profile.Skills...)
	// Direct fragments union (dedup by version-agnostic Name, child raises
	// priority) and cherry-picked bundle_items union — the directory mirror of
	// profileBuilder.addFragment / addBundleItem. Applied after parents so a
	// child overrides, consistent with the inline fold.
	resolved.Fragments = appendUniqueFragments(resolved.Fragments, profile.Fragments...)
	resolved.BundleItems = appendUnique(resolved.BundleItems, profile.BundleItems...)
	// Inline hooks/mcp fold like the inline profileBuilder: hooks accumulate
	// (event-keyed union) and a server name later in the chain wins. Self is
	// applied after parents, so a child's hooks/mcp override the parents'.
	agent.MergeHooksConfig(&resolved.Hooks, &profile.Hooks)
	wire.MergeMCPConfig(&resolved.MCP, &profile.MCP)
	maps.Copy(resolved.Variables, profile.Variables)
	// A profile's own llm overrides any inherited from parents.
	if profile.LLM != "" {
		resolved.LLM = profile.LLM
	}
	// Exclusions accumulate through the chain — a child cannot un-exclude
	// what a parent excluded. Mirrors mergeProfileValues for inline
	// config-map profiles (config_resolve.go).
	resolved.ExcludeFragments = appendUnique(resolved.ExcludeFragments, profile.ExcludeFragments...)
	resolved.ExcludeMCP = appendUnique(resolved.ExcludeMCP, profile.ExcludeMCP...)
	resolved.DenyTools = appendUnique(resolved.DenyTools, profile.DenyTools...)

	// Cache the finished result for any sibling branch that
	// reaches this same profile. Safe to share the pointer: Merge only ever
	// READS from its "other" argument (appendUnique/appendUniqueFragments
	// append into the receiver's own slice; MergeHooksConfig/MergeMCPConfig
	// likewise only append into dest), so a cached ResolvedProfile is never
	// mutated by whoever merges it in next.
	if memo != nil {
		memo[name] = resolved
	}

	return resolved, nil
}

// cloneVisited creates a copy of the visited map for branch isolation.
func cloneVisited(visited map[string]bool) map[string]bool {
	clone := make(map[string]bool, len(visited))
	maps.Copy(clone, visited)
	return clone
}

// ResolvedProfile contains the fully resolved contents of a profile after parent inheritance.
type ResolvedProfile struct {
	Bundles     []string         // All bundle references
	Tags        []string         // Descriptive (listing/discovery); does NOT select content
	SelectTags  []string         // Fragment tags to select content by
	Commands    []string         // Curated slash-command refs (opt-in; empty = global auto-export)
	Skills      []string         // Curated skill refs (opt-in; empty = global auto-export)
	Fragments   []FragmentRef    // Direct fragment references (with optional priority/version pin in Name)
	BundleItems []string         // Cherry-picked bundle items (e.g. "remote/bundle:fragments/x")
	Hooks       wire.HooksConfig // Directly-declared lifecycle hooks (executable; gated downstream)
	MCP         wire.MCPConfig   // Directly-declared MCP servers (executable; gated downstream)
	Variables   map[string]string
	LLM         string // Preferred config label/backend (empty = inherit primary)

	// SourceRef is this profile's OWN canonical origin ref, for keying the
	// executable trust gate on its directly-declared hooks/mcp
	// (internal/lm/backends/managed.go's gateProfileMCP/gateProfileHooks) by
	// SOURCE rather than display name. It is
	// the origin bundle's canonical ref ("<url>@bundles/<bundle>", WITHOUT
	// the "#profiles/<name>" selector — carrying that selector into the gate
	// ref is exactly what once produced a double-'#') for a
	// bundle-shipped profile, or "" for a genuinely local/project-authored
	// profile — which then keys the gate honestly IsLocal via
	// the bare-token fallback, never auto-allowing a
	// remote-sourced profile's inline executables. Populated by
	// resolveProfileRecursive from THIS profile's own load name; Merge below
	// deliberately never touches it, so a parent's SourceRef can never leak
	// onto a child's directly-declared execs.
	SourceRef string
	// Signer is the verified publisher identity of the bundle this profile
	// was shipped inside (Profile.Signer) — "" for a local profile or an
	// unsigned/untrusted bundle. Threaded to gateProfileExec so a
	// trusted-publisher profile's inline hooks/mcp are trusted-signer-allowed
	// exactly like bundle-declared ones, rather than every one of them
	// falling to manual review the moment that gap is fixed. Like
	// SourceRef, this is the profile's OWN signer and is never inherited
	// from a parent by Merge.
	Signer string

	// Exclusions accumulated through the parent chain (a child cannot
	// un-exclude what a parent excluded), matching the inline config-map
	// profile semantics in config_resolve.go.
	ExcludeFragments []string
	ExcludeMCP       []string

	// DenyTools accumulates through the parent chain like the exclusions
	// above (a child cannot un-deny what a parent denied). See Profile.DenyTools.
	DenyTools []string
}

// Merge adds items from another resolved profile.
func (r *ResolvedProfile) Merge(other *ResolvedProfile) {
	r.Bundles = appendUnique(r.Bundles, other.Bundles...)
	r.Tags = appendUnique(r.Tags, other.Tags...)
	r.SelectTags = appendUnique(r.SelectTags, other.SelectTags...)
	r.Commands = appendUnique(r.Commands, other.Commands...)
	r.Skills = appendUnique(r.Skills, other.Skills...)
	r.Fragments = appendUniqueFragments(r.Fragments, other.Fragments...)
	r.BundleItems = appendUnique(r.BundleItems, other.BundleItems...)
	agent.MergeHooksConfig(&r.Hooks, &other.Hooks)
	wire.MergeMCPConfig(&r.MCP, &other.MCP)
	for k, v := range other.Variables {
		if _, exists := r.Variables[k]; !exists {
			r.Variables[k] = v
		}
	}
	// First non-empty parent wins; the resolving profile overrides afterward.
	if r.LLM == "" {
		r.LLM = other.LLM
	}
	// Exclusions always accumulate (cannot un-exclude).
	r.ExcludeFragments = appendUnique(r.ExcludeFragments, other.ExcludeFragments...)
	r.ExcludeMCP = appendUnique(r.ExcludeMCP, other.ExcludeMCP...)
	r.DenyTools = appendUnique(r.DenyTools, other.DenyTools...)
}

func appendUnique(slice []string, items ...string) []string {
	seen := make(map[string]bool)
	for _, s := range slice {
		seen[s] = true
	}
	for _, item := range items {
		if !seen[item] {
			slice = append(slice, item)
			seen[item] = true
		}
	}
	return slice
}

// appendUniqueFragments unions fragment refs, deduped by their version-agnostic
// Name; a later occurrence only raises priority (a child profile can override a
// parent's priority but never lowers it). This mirrors profileBuilder.addFragment
// for inline profiles so the two resolution paths fold direct fragments
// identically.
func appendUniqueFragments(slice []FragmentRef, items ...FragmentRef) []FragmentRef {
	idx := make(map[string]int, len(slice))
	for i, f := range slice {
		idx[f.Name] = i
	}
	for _, item := range items {
		if i, ok := idx[item.Name]; ok {
			if item.Priority > slice[i].Priority {
				slice[i].Priority = item.Priority
			}
			continue
		}
		idx[item.Name] = len(slice)
		slice = append(slice, item)
	}
	return slice
}
