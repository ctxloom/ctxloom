package profiles

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/refuri"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
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
		// No empty-scalar check here: Profile.UnmarshalYAML's raw-sequence walk
		// already refuses one, and it has to exist anyway for the null items
		// this method is never called for. A second check would be unkillable.
		f.Name = node.Value
		f.Priority = 0
		return nil
	}
	// The struct form is NOT covered by that walk, which only inspects scalar
	// items, so `- {name: ""}` is refused here or nowhere.
	type plain FragmentRef
	if err := node.Decode((*plain)(f)); err != nil {
		return err
	}
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("empty fragment reference at line %d: a fragments list entry must name a fragment", node.Line)
	}
	return nil
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
	// Parents canonicalize against the source repo exactly like
	// Bundles/Commands: a <bundle>#profiles/<name> parent keeps its selector
	// through ItemTypeBundle's item passthrough. Top-level @profiles/ parent
	// distribution was retired, so ItemTypeProfile is no longer used here.
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

// ErrCrossRepoReference is the refusal for a remote profile naming content
// outside the repository it was shipped in. Only a local profile composes
// several repositories: registering a remote admits that repository's
// content, never whatever its profiles name.
var ErrCrossRepoReference = errors.New("a remote profile may refer only to bundles in its own repository")

// CheckOwnRepo refuses a remote profile (SourceURL set) that names, through
// any field carrying a bundle reference, content outside its own repository —
// the consumer's local content included. It reads refs as ResolveShortRefs
// leaves them, so a short same-repo ref is already canonical; a ref that does
// not parse cannot be attributed to the repository and is refused too. A
// local profile (SourceURL "") may name any repository.
func (p *Profile) CheckOwnRepo() error {
	if p.SourceURL == "" {
		return nil
	}
	refs := slices.Concat(p.Bundles, p.Parents, p.Commands, p.Skills, p.BundleItems)
	for _, fr := range p.Fragments {
		refs = append(refs, fr.Name)
	}
	for _, ref := range refs {
		if !inRepository(ref, p.SourceURL) {
			return fmt.Errorf("profile %s: %w: %s", p.Name, ErrCrossRepoReference, ref)
		}
	}
	return nil
}

// inRepository reports whether ref names content of the repository repoURL.
func inRepository(ref, repoURL string) bool {
	base, _ := remote.SplitItemPath(ref)
	parsed, err := remote.ParseReference(base)
	if err != nil || parsed.IsLocal {
		return false
	}
	return remote.SameRepository(parsed.URL, repoURL)
}

// Profile is a named collection of fragments, bundles, and configuration: a
// bundle's profile item, stored as <bundle>/profiles/<name>.yaml. A project's
// own profiles are the items of its project bundle (paths.ProjectBundleName).
type Profile struct {
	Name string `yaml:"-"` // Derived from filename
	Path string `yaml:"-"` // Full path to the file
	// SourceURL is the repository this profile was shipped in, stamped at
	// seed time by config.loadBundleProfileSeed; "" for a local/project
	// profile. A profile with one may name only that repository's content
	// (CheckOwnRepo). Read-only derived data (yaml:"-"), like Name/Path: a
	// profile file can never claim its own source.
	SourceURL   string   `yaml:"-"`
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
	// config.Profile.Hooks. A directory profile may be remote-sourced (a seeded
	// remote profile), so its directly-declared hooks are addressed under the
	// profile's own source before reaching backend settings.
	Hooks wire.HooksConfig `yaml:"hooks,omitempty"`

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

// UnmarshalYAML decodes a profile and then backstops the fragments list against
// empty entries. FragmentRef.UnmarshalYAML rejects an empty or whitespace
// scalar, but yaml.v3 never invokes a value's Unmarshaler for a NULL node -- a
// bare "- " list item decodes straight to the zero FragmentRef and is then
// DROPPED from the sequence entirely -- so an empty-named fragment never
// reaches p.Fragments to be caught there. Walk the source nodes so the typo
// fails loudly instead of vanishing.
func (p *Profile) UnmarshalYAML(node *yaml.Node) error {
	type plain Profile
	if err := node.Decode((*plain)(p)); err != nil {
		return err
	}
	return RefuseEmptyFragmentEntries(node)
}

// RefuseEmptyFragmentEntries reports an error for any empty entry in a profile
// node's fragments sequence.
//
// It is a free function, and exported, because the inline profile type in
// package config needs the identical check and cannot be reached from here:
// config imports profiles, never the other way round.
func RefuseEmptyFragmentEntries(node *yaml.Node) error {
	frags := yamlx.MapValue(node, "fragments")
	if frags == nil || frags.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range frags.Content {
		if item.Kind == yaml.ScalarNode && strings.TrimSpace(item.Value) == "" {
			return fmt.Errorf("empty fragment reference in profile fragments list (line %d): a fragments entry must name a fragment", item.Line)
		}
	}
	return nil
}

// Loader resolves profiles. Every profile it knows is a bundle's profile item,
// handed to it through WithSeededProfiles: a local bundle's profiles (the
// project bundle, paths.ProjectBundleName, among them) and the profiles of
// every installed remote bundle. A selector-less name is the project bundle's
// profile of that name (see profileRef).
type Loader struct {
	// dirs are the local bundles roots, in precedence order: where a NEW
	// profile item is written (Save). Reading never consults them — the seed
	// is the only read path.
	dirs []string
	fs   afero.Fs
	// remoteURLResolver maps a remote alias to its canonical repo URL, so a
	// LOCAL bundle's "<alias>/<bundle>" refs canonicalize to the aliased
	// remote (see canonicalizeLocalAliases). Nil means no canonicalization.
	remoteURLResolver func(alias string) string
	// localBundleExists reports whether a bundle name resolves to a LOCAL
	// bundle: an "<alias>/<bundle>" ref spelling one stays local (decision
	// E), and Save writes only into a local bundle that exists. Nil skips both
	// checks.
	localBundleExists func(name string) bool
	// seeded holds every profile this loader resolves, indexed by canonical
	// "<bundle>#profiles/<name>" ref. WithSeededProfiles fills it and Save
	// adds to it; nothing else is read.
	seeded map[string]*Profile

	// rep receives what a load reports about one profile without failing the
	// operation (a hollow profile, a parent that does not resolve). The caller
	// renders it.
	rep report.Reporter
}

// LoaderOption is a functional option for configuring a Loader.
type LoaderOption func(*Loader)

// WithReporter names the Sink the Loader reports per-profile findings to;
// without one they are discarded.
func WithReporter(sink report.Sink) LoaderOption {
	return func(l *Loader) { l.rep = report.To(sink) }
}

// WithFS sets the filesystem Save and Delete write through.
func WithFS(fs afero.Fs) LoaderOption {
	return func(l *Loader) {
		l.fs = fs
	}
}

// WithRemoteURLResolver sets the function that maps a remote alias to its
// canonical repo URL. The loader uses it to rewrite a LOCAL bundle profile's
// "<alias>/<bundle>" refs to canonical URL form. Without it, no
// canonicalization is attempted.
func WithRemoteURLResolver(resolve func(alias string) string) LoaderOption {
	return func(l *Loader) {
		l.remoteURLResolver = resolve
	}
}

// WithLocalBundleResolver sets the oracle reporting whether a bundle name
// resolves to a LOCAL bundle. Alias canonicalization leaves an
// "<alias>/<bundle>" ref local when it does (local-file-wins), and Save
// refuses to write into a local bundle it denies.
func WithLocalBundleResolver(exists func(name string) bool) LoaderOption {
	return func(l *Loader) {
		l.localBundleExists = exists
	}
}

// WithSeededProfiles pre-populates the loader with parsed bundle profiles
// indexed by canonical "<bundle>#profiles/<name>" ref. Each call merges its
// map in.
func WithSeededProfiles(seeded map[string]*Profile) LoaderOption {
	return func(l *Loader) {
		if l.seeded == nil {
			l.seeded = make(map[string]*Profile, len(seeded))
		}
		maps.Copy(l.seeded, seeded)
	}
}

// projectProfileRef is the ref a selector-less profile name resolves to: the
// profile of that name in the project bundle.
func projectProfileRef(name string) string {
	return remote.LocalBundleRef(paths.ProjectBundleName) + refuri.ProfileSelector + name
}

// profileRef maps the name a caller asked for onto the ref the seed is keyed
// by. A selector-less name — no "#profiles/" selector and no source — is the
// project bundle's profile of that name, so it must be a valid profile name
// (validateProfileName); every other spelling passes through for lookupSeeded
// to canonicalize.
func profileRef(name string) (string, error) {
	if strings.Contains(name, "#") || remote.IsFetchAddressRef(name) {
		return name, nil
	}
	if err := validateProfileName(name); err != nil {
		return "", err
	}
	return projectProfileRef(name), nil
}

// localBundleOf returns the LOCAL bundle a "<bundle>#profiles/<name>" ref
// addresses and the profile's bare name; ok is false for a ref into any other
// source, or one carrying no profile selector.
func localBundleOf(ref string) (bundle, profile string, ok bool) {
	b, name, ok := remote.SplitBundleProfileRef(ref)
	if !ok {
		return "", "", false
	}
	canon, err := remote.CanonicalBundleRef(b)
	if err != nil {
		return "", "", false
	}
	parsed, err := remote.ParseReference(canon)
	if err != nil || !parsed.IsLocal {
		return "", "", false
	}
	return parsed.Path, name, true
}

// explicitlyLocal reports whether ref spells its bundle in an explicit LOCAL
// grammar (ctxloom:local@bundles/<name>, or its canonical form) — not a short
// "<bundle>" or "<alias>/<bundle>" spelling, which may name a remote.
func explicitlyLocal(ref string) bool {
	bundle, _, ok := remote.SplitBundleProfileRef(ref)
	if !ok {
		return false
	}
	parsed, err := remote.ParseReference(bundle)
	return err == nil && parsed.IsLocal
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
// profile reference, for recursion and visited-map dedup. A selector-less name
// is first mapped to its project-bundle ref (profileRef), so "dev" and the
// ref it resolves to share one identity. A bundle-profile ref resolves
// alias-first (so "<alias>/<bundle>#profiles/<p>" and its canonical URL
// spelling share one identity), then through the selector-preserving
// CanonicalProfileKey — CanonicalKey would drop the "#profiles/<name>"
// selector and collapse the ref to its bundle, which is never a profile name.
func (l *Loader) canonicalProfileName(ref string) string {
	if mapped, err := profileRef(ref); err == nil {
		ref = mapped
	}
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
// its canonical seed key via the remote registry. ok is false when the loader
// has no registry resolver, the ref carries no selector, the bundle part is
// already canonical or ctxloom:local, or the alias names no configured remote.
func (l *Loader) aliasSeededKey(name string) (string, bool) {
	if l.remoteURLResolver == nil {
		return "", false
	}
	bundle, _, ok := remote.SplitBundleProfileRef(name)
	if !ok || remote.IsFetchAddressRef(bundle) || strings.HasPrefix(bundle, remote.LocalSource) {
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
	candidate := url + "@" + remote.ItemTypeBundle.DirName() + "/" + rest + name[strings.Index(name, refuri.ProfileSelector):]
	return remote.CanonicalProfileKey(candidate)
}

// NewLoader creates a profile loader. dirs are the local bundles roots a new
// profile item is written under (Save), in precedence order.
func NewLoader(dirs []string, opts ...LoaderOption) *Loader {
	l := &Loader{
		dirs: dirs,
		fs:   afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(l)
	}
	l.canonicalizeLocalAliases()
	return l
}

// canonicalizeLocalAliases rewrites each LOCAL bundle profile's
// "<alias>/<bundle>" bundle refs to the aliased remote's canonical URI, in
// memory. Only a local bundle's profiles mean anything by an alias: the alias
// table is this machine's, so a publisher's profile could never have been
// written against it. It runs once every option is applied, so the order the
// seed and the resolvers were given in does not matter.
func (l *Loader) canonicalizeLocalAliases() {
	if l.remoteURLResolver == nil {
		return
	}
	u := bundleRefCanonicalizeUpgrade{aliasToURL: l.remoteURLResolver, localBundleExists: l.localBundleExists}
	for key, p := range l.seeded {
		if _, _, ok := localBundleOf(key); ok {
			u.applyTo(p)
		}
	}
}

// List returns every profile the loader resolves, sorted by name.
func (l *Loader) List() []*Profile {
	profiles := make([]*Profile, 0, len(l.seeded))
	for _, p := range l.seeded {
		profiles = append(profiles, p)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})
	return profiles
}

// The fix a missed project-profile name carries. A bare name means the
// project's own profile, but the name a user types is often one they saw in a
// listing for a profile another bundle ships — which is addressed by its ref.
const (
	didYouMeanProfileFix = "did you mean %s? Another bundle's profile is named by its full ref"
	listProfilesFix      = "`ctxloom profile list` names every profile you can use"
)

// localMissFix names the one installed profile whose name matches the missed
// project-profile ref, or the listing when there is no single such profile.
func (l *Loader) localMissFix(ref string) string {
	leaf := ref
	if i := strings.LastIndex(ref, refuri.ProfileSelector); i >= 0 {
		leaf = ref[i+len(refuri.ProfileSelector):]
	}
	var match string
	for key := range l.seeded {
		if !strings.HasSuffix(key, refuri.ProfileSelector+leaf) {
			continue
		}
		if match != "" {
			return listProfilesFix
		}
		match = key
	}
	if match == "" {
		return listProfilesFix
	}
	return fmt.Sprintf(didYouMeanProfileFix, match)
}

// Load loads a profile by name: a selector-less name is the project bundle's
// profile of that name, and any "<bundle>#profiles/<name>" ref — local or
// remote, short, aliased or version-pinned — resolves through the seed under
// its canonical key.
//
// # Ownership
//
// A returned profile is READ-ONLY to the caller: it is the one shared instance
// every reader in this run receives. Every write path (Save, Delete, and
// operations' edit/import flows) works on the profile's file, never on this
// instance in place.
func (l *Loader) Load(name string) (*Profile, error) {
	ref, err := profileRef(name)
	if err != nil {
		return nil, err
	}
	p, ok := l.lookupSeeded(ref)
	if !ok {
		// A selector-less name, or an explicitly local ref, is a profile of
		// this project's own: nothing to pull. Any other bundle-profile
		// spelling may name an installed bundle not yet pulled, so its miss
		// says how to install it.
		if ref != name || explicitlyLocal(ref) {
			return nil, report.Errorf(l.localMissFix(ref), "%w: %s", errs.ErrProfileNotFound, name)
		}
		return nil, fmt.Errorf("%w: %s (bundle profile has no lockfile entry — run 'ctxloom deps pull')", errs.ErrProfileNotFound, name)
	}
	// A remote profile fails to LOAD, not to seed: every reader — run, sync,
	// lock, upgrade, a child resolving it as a parent — reaches it here, and
	// sees the same refusal naming the profile and the offending ref.
	if err := p.CheckOwnRepo(); err != nil {
		return nil, err
	}
	// A profile that selects nothing loads, so List and the pickers can still
	// enumerate a half-authored one, but nothing may launch on it while
	// pretending it composed something: the fail-loudly gate says so. Only
	// for a LOCAL profile — the one this project can fix.
	if _, _, local := localBundleOf(p.Name); local && !p.HasContent() {
		l.rep.FailOncef(report.KindConfig, "give the profile something to select (parents, bundles, fragments, select_tags) or delete it",
			"profile %s selects nothing: no parents, bundles, fragments, bundle_items, commands, skills, select_tags, hooks, variables or llm — a session launched on it composes no context", p.Path)
	}
	return p, nil
}

// Exists reports whether a profile is stored under name. It deliberately does
// not require the profile to select anything: callers use Exists to decide
// whether a name is free (operations.CreateProfile) or whether a declared
// parent is present (requireProfilesExist).
func (l *Loader) Exists(name string) bool {
	ref, err := profileRef(name)
	if err != nil {
		return false
	}
	_, ok := l.lookupSeeded(ref)
	return ok
}

// Decode is the ONE decoder for a profile document, whichever bundle it is
// read from: the document is checked against the profile schema AS WRITTEN —
// decoding is what loses a typo'd key or coerces a wrong type — and only then
// is it decoded. A document that does not
// match the schema is an error, not a warning: a bundle carrying one does not
// load.
//
// An empty document decodes to the empty profile; whether one may be written
// or launched is the writers' and the fail-loudly gate's decision.
func Decode(data []byte) (*Profile, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if err := validateProfileDocument(&doc, data); err != nil {
		return nil, err
	}
	var profile Profile
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
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
		p.Hooks.HasAny() ||
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

// Save writes a profile item into a LOCAL bundle tree: back to its own file
// when it was loaded from one, else as a new item of the local bundle its name
// addresses — a selector-less name is the project bundle's.
//
// A remote bundle's profile is refused: its Path is the "<remote>:" sentinel,
// and writing one anywhere locally would silently fork it from its source —
// the edit would evaporate on the next pull.
func (l *Loader) Save(profile *Profile) error {
	if IsSeededPath(profile.Path) {
		return fmt.Errorf("profile %q is a remote profile and read-only; edit it at its source and run 'ctxloom deps pull'", profile.Name)
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
		l.rep.FailOncef(report.KindConfig, "give the profile something to select (parents, bundles, fragments, select_tags)",
			"profile %q selects nothing: it carries only labels, so a session launched on it composes no context", profile.Name)
	}

	path := profile.Path
	if path == "" {
		var err error
		if path, err = l.newItemPath(profile.Name); err != nil {
			return err
		}
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
	// The seed is this loader's view of every profile; the item just written
	// joins it, so the same loader resolves what it saved.
	saved := *profile
	saved.Name = l.canonicalProfileName(profile.Name)
	if l.seeded == nil {
		l.seeded = make(map[string]*Profile)
	}
	l.seeded[saved.Name] = &saved
	return nil
}

// newItemPath is where a new profile item named name is written: the
// paths.ProfilesDir item directory of the local bundle the name addresses,
// under the first local bundles root holding that bundle.
func (l *Loader) newItemPath(name string) (string, error) {
	ref, err := profileRef(name)
	if err != nil {
		return "", err
	}
	bundle, item, ok := localBundleOf(ref)
	if !ok {
		return "", fmt.Errorf("profile %q is not in a local bundle: only a local bundle's profiles are written here", name)
	}
	if err := validateProfileName(item); err != nil {
		return "", err
	}
	dir, ok := l.bundleDir(bundle)
	if !ok {
		return "", fmt.Errorf("%w: no local bundle %q to write profile %q into", errs.ErrBundleNotFound, bundle, item)
	}
	path := filepath.Join(dir, paths.ProfilesDir, item+".yaml")
	// A file already here did not load — it is not in the seed, or this would
	// not be a NEW profile. It is still the user's file, and a new profile
	// written over it would silently replace it.
	if present, err := afero.Exists(l.fs, path); err != nil || present {
		return "", fmt.Errorf("profile %q: %s is present but did not load; fix or remove it: %w", item, path, os.ErrExist)
	}
	return path, nil
}

// bundleDir is the directory of the local bundle called bundle under the
// first local bundles root holding it; ok is false when no root does, or the
// local-bundle oracle denies it.
func (l *Loader) bundleDir(bundle string) (string, bool) {
	if l.localBundleExists != nil && !l.localBundleExists(bundle) {
		return "", false
	}
	for _, root := range l.dirs {
		dir := filepath.Join(root, filepath.FromSlash(bundle))
		if exists, err := afero.DirExists(l.fs, dir); err == nil && exists {
			return dir, true
		}
	}
	return "", false
}

// validateProfileName refuses a name that is not a single path segment. A
// profile item's name is its filename inside a bundle's profiles directory,
// exactly like every other bundle item, so it can carry no directory part —
// which is also what keeps a name from escaping that directory when joined
// into a path. '#' is reserved for bundle-item selectors.
func validateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if strings.Contains(name, "#") {
		return fmt.Errorf("invalid profile name %q: '#' is reserved for bundle refs (<bundle>#profiles/<name>)", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid profile name %q: a profile name is a single path segment; rename it, e.g. %q", name, strings.NewReplacer("/", "-", `\`, "-").Replace(name))
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid profile name %q: must stay within the profiles directory", name)
	}
	return nil
}

// Delete removes a local profile item's file. A remote bundle's profile is a
// read-only reference with no local file to remove.
func (l *Loader) Delete(name string) error {
	profile, err := l.Load(name)
	if err != nil {
		return err
	}
	if IsSeededPath(profile.Path) {
		return fmt.Errorf("profile %q is a remote profile and read-only; remove its lockfile entry instead (see 'ctxloom remote')", profile.Name)
	}
	if err := l.fs.Remove(profile.Path); err != nil {
		return err
	}
	delete(l.seeded, profile.Name)
	return nil
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
		Name:      name,
		Variables: make(map[string]string),
	}
	// SourceRef is THIS profile's own provenance — never inherited
	// from (or overwritten by) a parent's Merge below: a profile's
	// directly-declared hooks/mcp must be addressed by ITS OWN origin, not a
	// parent's. profile.Name is already
	// the canonical identity here — the "<bundle>#profiles/<name>" seed key
	// (config.loadBundleProfileSeed) — so deriving from it needs no
	// re-canonicalization.
	if bundle, _, ok := remote.SplitBundleProfileRef(profile.Name); ok {
		// Fails the whole resolution rather than degrading. SourceRef is what
		// this profile's directly-declared hooks and MCP servers are addressed
		// by; a source that cannot be canonicalized has no key, and the local
		// fallback would address it under a name the ref never named.
		sourceRef, err := remote.CanonicalBundleRef(bundle)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		resolved.SourceRef = sourceRef
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
				l.rep.FailOncef(report.KindRef, "ctxloom deps pull",
					"profile %q: parent %s not installed; skipping (run `ctxloom deps pull` to install)",
					name, parent)
			default:
				// Corrupt parent (invalid YAML, IO/permission error): skip this
				// branch rather than aborting the whole resolution — degraded
				// mode still reaches the LLM; strict mode aborts on the finding.
				l.rep.FailOncef(report.KindRef, "fix or remove the parent profile file",
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
	// Inline hooks fold like the inline profileBuilder: they accumulate
	// (event-keyed union). Self is applied after parents, so a child's hooks
	// override the parents'.
	wire.MergeHooksConfig(&resolved.Hooks, &profile.Hooks)
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
	// append into the receiver's own slice; MergeHooksConfig likewise
	// only appends into dest), so a cached ResolvedProfile is never
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
	// Name is the name this profile resolved under: the ask, so a report can
	// say which profile pushed what. Never inherited from a parent.
	Name        string
	Bundles     []string         // All bundle references
	Tags        []string         // Descriptive (listing/discovery); does NOT select content
	SelectTags  []string         // Fragment tags to select content by
	Commands    []string         // Curated slash-command refs (opt-in; empty = global auto-export)
	Skills      []string         // Curated skill refs (opt-in; empty = global auto-export)
	Fragments   []FragmentRef    // Direct fragment references (with optional priority/version pin in Name)
	BundleItems []string         // Cherry-picked bundle items (e.g. "remote/bundle:fragments/x")
	Hooks       wire.HooksConfig // Directly-declared lifecycle hooks (executable; gated downstream)
	Variables   map[string]string
	LLM         string // Preferred config label/backend (empty = inherit primary)

	// SourceRef is this profile's OWN canonical origin ref, for addressing its
	// directly-declared hooks (managedhooks.addressableProfileHooks) by
	// SOURCE rather than display name. It is
	// the canonical ref of the bundle the profile is an item of (WITHOUT the
	// "#profiles/<name>" selector — carrying that selector into the gate
	// ref is exactly what once produced a double-'#'): a local bundle's for
	// a project's own profile, a remote bundle's for a shipped one. Populated by
	// resolveProfileRecursive from THIS profile's own load name; Merge below
	// deliberately never touches it, so a parent's SourceRef can never leak
	// onto a child's directly-declared execs.
	SourceRef string

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
	wire.MergeHooksConfig(&r.Hooks, &other.Hooks)
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
