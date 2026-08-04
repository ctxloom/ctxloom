package bundles

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// LoadedContent is a fully resolved fragment or prompt with its bundle
// metadata, ready to assemble into context.
type LoadedContent struct {
	Name         string   // Full name (bundle/item)
	Bundle       string   // Owning bundle's loader name (canonical ref for remote bundles)
	Item         string   // Bare fragment/prompt name within the bundle
	Version      string   // Bundle version
	Tags         []string // Combined tags
	Content      string   // The actual content
	Installation string   // Setup/installation instructions for tooling
	IsDistilled  bool     // Whether distilled version was used
	DistilledBy  string   // Model that created distillation
	// Form is the LAYOUT form these exact bytes were selected in ("raw" |
	// "distilled"). IsDistilled is this same fact as a bool; both come from the
	// form actually served, never re-derived (a re-derivation drops terms like
	// no_distill and describes bytes that were never served).
	Form ContentForm
	// TrustRef is the ref the trust gate keys this item by:
	// "<source>#fragments/<name>" or "<source>#prompts/<name>", where source is
	// the bundle's HONEST source ref (canonical for a cloned bundle so its text
	// gates like an executable, the local name for a project bundle so its text
	// auto-trusts). A read FACT the reader establishes, never a decision.
	TrustRef string
	// Signer is the owning bundle's VERIFIED publisher identity, or "" when the
	// bundle is unsigned or was signed by a key this machine does not trust to
	// publish. It is Bundle.Signer() carried through — stamped only by a load
	// path that actually verified a signature — so it is never a claim the
	// content made about itself.
	Signer  string
	Exports map[string]string // Exported variables (from generators)
	LLM     LLMExports        // Per-LLM export settings (slash-command config)
}

// ExportName returns the short, slash-command-facing name for this item:
// the owning bundle's last path segment plus the bare item name. Remote
// bundles are keyed by canonical ref ("<url>@bundles/<path>"); exporting
// that verbatim names the command after the entire URL (and ':' makes the
// filename invalid on Windows). Name remains the full identity — only the
// export-facing name shortens. Content without bundle metadata (builtin
// prompts) falls back to Name.
func (c *LoadedContent) ExportName() string {
	if c.Bundle == "" || c.Item == "" {
		return c.Name
	}
	return exportBaseName(c.Bundle) + "/" + c.Item
}

// exportBaseName shortens a bundle loader name to its last path segment,
// stripping the canonical ref's "<url>@<type>/" prefix when present.
func exportBaseName(bundleName string) string {
	base := bundleName
	if i := strings.LastIndex(base, "@"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}

// ClaudeCodeConfig holds configuration for exporting prompts as Claude Code slash commands.
type ClaudeCodeConfig struct {
	Enabled      *bool    `yaml:"enabled"`       // nil = true (opt-out model)
	Description  string   `yaml:"description"`   // For /help display
	ArgumentHint string   `yaml:"argument_hint"` // Autocomplete hint
	AllowedTools []string `yaml:"allowed_tools"` // Tool restrictions
	Model        string   `yaml:"model"`         // Override model
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c ClaudeCodeConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// AntigravityConfig holds configuration for exporting commands as Antigravity
// CLI (agy) skill files.
type AntigravityConfig struct {
	Enabled     *bool  `yaml:"enabled"`     // nil = true (opt-out model)
	Description string `yaml:"description"` // For skill listings
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c AntigravityConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// CodexConfig holds configuration for exporting prompts as Codex CLI custom prompts.
type CodexConfig struct {
	Enabled      *bool  `yaml:"enabled"`       // nil = true (opt-out model)
	Description  string `yaml:"description"`   // For the /prompts listing
	ArgumentHint string `yaml:"argument_hint"` // Autocomplete hint
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c CodexConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// KiroConfig holds configuration for exporting commands as Kiro CLI skills
// (agentskills.io SKILL.md files, invocable as /<name> slash commands).
type KiroConfig struct {
	Enabled     *bool  `yaml:"enabled"`     // nil = true (opt-out model)
	Description string `yaml:"description"` // For skill discovery + /help
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c KiroConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// OpencodeConfig holds configuration for exporting prompts as opencode CLI
// custom commands (.opencode/command/<name>.md files, invocable as /<name>
// slash commands).
type OpencodeConfig struct {
	Enabled     *bool  `yaml:"enabled"`     // nil = true (opt-out model)
	Description string `yaml:"description"` // Shown in opencode's command palette / resolved config
}

// IsEnabled returns true unless explicitly disabled (opt-out model).
func (c OpencodeConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// LLMExports holds per-LLM export settings for a fragment/prompt — e.g. how it
// surfaces as a slash command in each backend — keyed by backend name.
type LLMExports struct {
	ClaudeCode  ClaudeCodeConfig  `yaml:"claude-code"`
	Antigravity AntigravityConfig `yaml:"antigravity"`
	Codex       CodexConfig       `yaml:"codex"`
	Kiro        KiroConfig        `yaml:"kiro"`
	Opencode    OpencodeConfig    `yaml:"opencode"`
}

// ContentInfo provides metadata about a fragment or prompt for listing.
type ContentInfo struct {
	Name     string
	FileName string
	Path     string
	Source   string // "bundle:name" or legacy path
	Tags     []string
	Bundle   string // Bundle name this came from
	ItemType string // "fragment" or "command"
	// Description is the item's own authored description (BundleCommand's
	// `description:` key) — "" when the item has none (fragments never carry
	// one). Populated by ListAllCommands so a listing surface (the ACP agent
	// role's available_commands_update, B4) can advertise a real
	// human-readable description instead of fabricating one.
	Description string
}

// ListAllFragments returns info about all fragments across all bundles.
func (l *Loader) ListAllFragments() ([]ContentInfo, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	var infos []ContentInfo
	seen := collections.NewSet[string]()

	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		for name, frag := range bundle.Fragments {
			// Use bundleInfo.Name (full path) instead of bundle.Name (just filename)
			key := bundleInfo.Name + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:     name,
				FileName: name + ".yaml",
				Path:     bundleInfo.Path,
				Source:   bundleInfo.Name,
				Tags:     slices.Concat(bundle.Tags, frag.Tags),
				Bundle:   bundleInfo.Name,
				ItemType: "fragment",
			})
		}
	}

	return infos, nil
}

// ListAllCommands returns info about all commands across all bundles. Unlike
// its ListAllFragments twin, it populates ContentInfo.Description from the
// command's own authored `description:` (BundleCommand.Description) —
// fragments carry no such field at all (BundleFragment has none), and a
// command's description is exactly what an ACP editor's available_commands_
// update needs (B4, gap G5 — see internal/operations.buildSessionCommands)
// to advertise something real instead of a fabricated placeholder. This is a
// genuine, permanent shape difference between the two item kinds, not drift
// to reconcile.
// reprise:accept-drift
func (l *Loader) ListAllCommands() ([]ContentInfo, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}

	seen := collections.NewSet[string]()
	var infos []ContentInfo
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}

		for name, prompt := range bundle.Commands {
			// Use bundleInfo.Name (normalized full path) instead of bundle.Name (just filename)
			key := bundleInfo.Name + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:        name,
				FileName:    name + ".yaml",
				Path:        bundleInfo.Path,
				Source:      bundleInfo.Name,
				Tags:        slices.Concat(bundle.Tags, prompt.Tags),
				Bundle:      bundleInfo.Name,
				ItemType:    "command",
				Description: prompt.Description,
			})
		}
	}

	return infos, nil
}

// ReadFragment reports every fragment this reader holds under name, with the
// trust facts attached and NOTHING dropped on policy grounds. Name can be
// "fragment-name" (searches all bundles, so several bundles may each answer)
// or "bundle#fragments/name" (at most one). The process stage decides which of
// them — if any — may be delivered; see Pipeline.GetFragment.
//
// A name that resolves to no item at all is an error (ErrFragmentNotFound):
// "I do not have this" is a read fact. "You may not see this" is not, and this
// method never says it.
//
// preferDistilled is the CALLER'S form choice, supplied per call rather than
// baked into the reader. It only ever PREFERS — a fragment with no distilled
// form, or one that forbids distillation, still reports raw.
func (l *Loader) ReadFragment(name string, preferDistilled bool) ([]*LoadedContent, error) {
	bundleName, fragName, isRef, err := splitItemRef(name, "fragments")
	if err != nil {
		return nil, err
	}
	if isRef {
		return l.fragmentFromBundle(bundleName, fragName, preferDistilled)
	}
	return l.searchFragment(name, preferDistilled)
}

// splitItemRef parses a "bundle#kind/name" reference. isRef reports whether a
// "#" was present at all; when it was, kind must equal want or an error is
// returned. For a plain name (no "#"), isRef is false and the caller searches.
func splitItemRef(name, want string) (bundleName, itemName string, isRef bool, err error) {
	bundleName, rest, found := strings.Cut(name, "#")
	if !found {
		return "", "", false, nil
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] != want {
		return "", "", true, fmt.Errorf("invalid %s reference: %s", strings.TrimSuffix(want, "s"), name)
	}
	return bundleName, parts[1], true, nil
}

// fragmentContent builds a LoadedContent for a fragment: the selected bytes
// plus the trust facts the process stage decides on. TrustRef is keyed on the
// bundle's honest source ref (Bundle.contentSourceRef) — canonical for a cloned
// bundle so its text gates like an executable, the local name for a project
// bundle so its text auto-trusts — which is the SAME keying the exec gate uses.
// Content is the EXACT bytes a delivery would carry (pre-mustache), so the gate
// decides on what the agent would actually see; profile-variable substitution
// happens after and cannot smuggle content past it.
func (l *Loader) fragmentContent(bundle *Bundle, fragName string, frag BundleFragment, preferDistilled bool) *LoadedContent {
	payload, form := frag.ContentPayload(preferDistilled)
	content := string(payload)
	return &LoadedContent{
		TrustRef:     bundle.contentSourceRef() + "#fragments/" + fragName,
		Signer:       bundle.Signer(),
		Form:         form,
		Name:         fmt.Sprintf("%s/%s", bundle.Name, fragName),
		Bundle:       bundle.Name,
		Item:         fragName,
		Version:      bundle.Version,
		Tags:         slices.Concat(bundle.Tags, frag.Tags),
		Content:      content,
		Installation: frag.Installation,
		// From the form resolveEffective actually chose, never re-derived:
		// a re-derivation drops terms (no_distill) and describes bytes that
		// were never served.
		IsDistilled: form == FormDistilled,
		DistilledBy: frag.DistilledBy,
	}
}

// fragmentFromBundle loads a specific bundle and reports the named fragment —
// the single-candidate case of ReadFragment.
func (l *Loader) fragmentFromBundle(bundleName, fragName string, preferDistilled bool) ([]*LoadedContent, error) {
	bundle, err := l.Load(bundleName)
	if err != nil {
		return nil, err
	}
	frag, ok := bundle.Fragments[fragName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrFragmentNotFound, fragName, bundleName)
	}
	return []*LoadedContent{l.fragmentContent(bundle, fragName, frag, preferDistilled)}, nil
}

// ResolveFragmentAsk resolves a user-supplied fragment ask to the canonical
// name shape the assembly pipeline carries (see ExpandBundleRefs). A
// qualified ask ("bundle#fragments/name") canonicalizes its bundle part
// directly. A bare name searches all bundles: a unique match qualifies it;
// several matches resolve deterministically to the first in List order (List
// sorts by bundle name) with a warning naming the alternatives; no match
// returns the ask unchanged so the load step reports it (fault-tolerance:
// an explicit ask is never dropped silently).
func (l *Loader) ResolveFragmentAsk(name string) string {
	if strings.Contains(name, "#") {
		return remote.CanonicalFragmentRef(name)
	}
	bundleInfos, err := l.List()
	if err != nil {
		return name
	}
	var matches []string
	for _, bundleInfo := range bundleInfos {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}
		if _, ok := bundle.Fragments[name]; ok {
			matches = append(matches, remote.CanonicalBundleRef(bundleInfo.Name))
		}
	}
	if len(matches) == 0 {
		return name
	}
	if len(matches) > 1 {
		l.warnAmbiguousFragment(name, matches, matches[0])
	}
	return matches[0] + remote.FragmentSelector + name
}

// searchFragment scans every bundle for a fragment with the given name and
// reports EVERY match, in List order (bundle-name sorted, so the order is
// deterministic). Reporting all of them is what lets the process stage keep
// scanning past one it withholds — a trusted copy in another bundle still wins
// — a decision the reader is in no position to make.
func (l *Loader) searchFragment(name string, preferDistilled bool) ([]*LoadedContent, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}
	var out []*LoadedContent
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}
		if frag, ok := bundle.Fragments[name]; ok {
			out = append(out, l.fragmentContent(bundle, name, frag, preferDistilled))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrFragmentNotFound, name)
	}
	return out, nil
}

// ReadCommand is ReadFragment's command counterpart: every command this reader
// holds under name, nothing dropped on policy grounds. Name can be
// "command-name" (searches all bundles) or "bundle#commands/name".
func (l *Loader) ReadCommand(name string, preferDistilled bool) ([]*LoadedContent, error) {
	bundleName, promptName, isRef, err := splitItemRef(name, "commands")
	if err != nil {
		return nil, err
	}
	if isRef {
		return l.commandFromBundle(bundleName, promptName, preferDistilled)
	}
	return l.searchCommand(name, preferDistilled)
}

// commandContent builds a LoadedContent for a command (commands also carry
// Plugins). See fragmentContent — the same read facts, over the same exact
// effective-content bytes. TrustRef keeps the "prompts" kind segment
// (trust.KindPrompt.Dir()) even though the load selector is "#commands/", so
// the item-kind rename does not invalidate existing trust grants.
func (l *Loader) commandContent(bundle *Bundle, promptName string, prompt BundleCommand, preferDistilled bool) *LoadedContent {
	payload, form := prompt.ContentPayload(preferDistilled)
	content := string(payload)
	return &LoadedContent{
		TrustRef:     bundle.contentSourceRef() + "#prompts/" + promptName,
		Signer:       bundle.Signer(),
		Form:         form,
		Name:         fmt.Sprintf("%s/%s", bundle.Name, promptName),
		Bundle:       bundle.Name,
		Item:         promptName,
		Version:      bundle.Version,
		Tags:         slices.Concat(bundle.Tags, prompt.Tags),
		Content:      content,
		Installation: prompt.Installation,
		// See fragmentContent: the served form is the only honest source.
		IsDistilled: form == FormDistilled,
		DistilledBy: prompt.DistilledBy,
		LLM:         prompt.LLM,
	}
}

// ReadBundleCommands reports every command shipped by the bundle at bundleRef
// as fully-resolved LoadedContent, in deterministic (name-sorted) order, or
// nil if the bundle can't be loaded. This is the command analog of the
// per-bundle MCP/hook resolution (loadMCPFromBundleRef): it lets command
// exports be scoped to a specific profile's bundles instead of the global
// ListAllCommands sweep. Deterministic order matters so downstream
// command-file writes are reproducible. Nothing is dropped on policy grounds —
// see Pipeline.CommandsFromBundleRef for the gated delivery.
func (l *Loader) ReadBundleCommands(bundleRef string, preferDistilled bool) []*LoadedContent {
	bundle, err := l.Load(bundleRef)
	if err != nil {
		// Same silent-export defect as SkillsFromBundleRef, same fix, same
		// warner expandBundleRef already uses: writing zero command
		// files because the bundle would not load must not look like a bundle
		// that ships no commands.
		l.warnUnresolvedBundle(bundleRef, err)
		return nil
	}
	names := make([]string, 0, len(bundle.Commands))
	for name := range bundle.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*LoadedContent, 0, len(names))
	for _, name := range names {
		out = append(out, l.commandContent(bundle, name, bundle.Commands[name], preferDistilled))
	}
	return out
}

// commandFromBundle loads a specific bundle and reports the named command —
// the single-candidate case of ReadCommand.
func (l *Loader) commandFromBundle(bundleName, promptName string, preferDistilled bool) ([]*LoadedContent, error) {
	bundle, err := l.Load(bundleName)
	if err != nil {
		return nil, err
	}
	prompt, ok := bundle.Commands[promptName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrCommandNotFound, promptName, bundleName)
	}
	return []*LoadedContent{l.commandContent(bundle, promptName, prompt, preferDistilled)}, nil
}

// searchCommand scans every bundle for a command with the given name and
// reports EVERY match, in List order — searchFragment's command twin, for the
// same reason: only the process stage can say which of them is admissible.
func (l *Loader) searchCommand(name string, preferDistilled bool) ([]*LoadedContent, error) {
	bundles, err := l.List()
	if err != nil {
		return nil, err
	}
	var out []*LoadedContent
	for _, bundleInfo := range bundles {
		bundle, err := l.LoadFile(bundleInfo.Path)
		if err != nil {
			continue
		}
		if prompt, ok := bundle.Commands[name]; ok {
			out = append(out, l.commandContent(bundle, name, prompt, preferDistilled))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrCommandNotFound, name)
	}
	return out, nil
}

// ListByTags returns fragments matching any of the given tags.
func (l *Loader) ListByTags(tags []string) ([]ContentInfo, error) {
	all, err := l.ListAllFragments()
	if err != nil {
		return nil, err
	}

	tagSet := collections.NewSetFrom(tags...)

	var matched []ContentInfo
	for _, info := range all {
		if slices.ContainsFunc(info.Tags, tagSet.Has) {
			matched = append(matched, info)
		}
	}

	return matched, nil
}

// ExpandedRef is one fragment produced by expanding a profile bundle reference.
// Name is the version-AGNOSTIC canonical fragment identity
// ("<canonical-bundle>#fragments/<name>") used for dedup/exclusion/ordering;
// Version is the optional "@<commit>" content version the originating ref pinned
// (empty = the lockfile-pinned default). The version is honored only at the
// read/resolution path (GetFragmentAtVersion), so the identity stays
// version-agnostic — two spellings of the same item dedup regardless of version.
type ExpandedRef struct {
	Name    string
	Version string
}

// ExpandBundleRefs expands profile bundle references into canonical fragment
// refs usable with GetFragment / GetFragmentAtVersion. See the Profile.Bundles
// documentation in internal/profiles for the supported reference syntax.
//
// Supported reference forms (each may carry a trailing "@<commit>" on the
// bundle part to pin that item to a historical version):
//
//	"bundle"                        // every fragment in the bundle
//	"bundle#fragments/name"         // a single fragment (canonical syntax)
//	"bundle:fragments/name"         // a single fragment (profile syntax alias)
//	"bundle@<commit>"               // every fragment at that commit
//	"bundle@<commit>:fragments/name"// a single fragment at that commit
//
// Refs that target commands or MCP servers (e.g. "bundle:commands/x",
// "bundle:mcp") are skipped, because they do not resolve to fragments.
// Bundles that cannot be loaded — including a pinned version that fails to
// fetch — are skipped, mirroring the tolerant behavior of LoadMultiple/
// GetFragment so a missing bundle does not abort the whole assembly.
//
// The returned refs are deduplicated and stable: whole-bundle expansions are
// sorted alphabetically by fragment name so the resulting context hash is
// reproducible. Bundle identities are canonicalized (remote.CanonicalBundleRef)
// — remote refs to their version-less canonical URL, plain local names to
// ctxloom:local form — so names from different reference spellings of the same
// bundle compare and dedupe exactly. Dedup is version-agnostic: when the same
// item is produced more than once an explicit "@<commit>" wins over a
// default-version entry (one version per item).
func (l *Loader) ExpandBundleRefs(refs []string) []ExpandedRef {
	index := make(map[string]int)
	var out []ExpandedRef
	for _, ref := range refs {
		for _, er := range l.expandBundleRef(ref) {
			if i, ok := index[er.Name]; ok {
				// Same item already present: an explicit @commit upgrades a
				// default-version entry; never carry two versions of one item.
				if out[i].Version == "" && er.Version != "" {
					out[i].Version = er.Version
				}
				continue
			}
			index[er.Name] = len(out)
			out = append(out, er)
		}
	}
	return out
}

// expandBundleRef returns the canonical fragment refs for a single ref.
// See ExpandBundleRefs for the supported syntax.
func (l *Loader) expandBundleRef(ref string) []ExpandedRef {
	if ref == "" {
		return nil
	}

	// Targeted ref: bundle#<anything>/... or bundle:{fragments|commands|mcp}/...
	// The ':' alias set is exactly the marker list below — ":prompts/" is not
	// in it and is not a shim for ":commands/". An unrecognised ':' selector
	// is not rejected here; it falls through to the whole-bundle branch, which
	// then fails to find a bundle by that whole name.
	// Locate the item selector WITHOUT tripping on a source's scheme colon.
	// '#' is the canonical separator and is unambiguous — a ref never contains
	// '#' except to introduce a selector (canonical URLs included). The ':'
	// alias counts only when it introduces a known section, so a URL scheme's
	// ':' (always followed by "//") is never mistaken for a selector. (The
	// previous IndexAny(":#") split a "https://…" ref on the scheme colon,
	// dropping every URL-form cherry-pick.)
	sep := strings.Index(ref, "#")
	if sep == -1 {
		for _, marker := range []string{":fragments/", ":commands/", ":mcp"} {
			if i := strings.Index(ref, marker); i != -1 {
				sep = i
				break
			}
		}
	}
	if sep != -1 {
		bundleName := ref[:sep]
		rest := ref[sep+1:]
		if !strings.HasPrefix(rest, "fragments/") {
			// Targeted at commands, mcp, or unknown — not a fragment ref.
			return nil
		}
		// The bundle part may pin a content version ("bundle@<commit>"); keep it
		// (the read path resolves the cherry-pick at that commit) while the
		// emitted Name stays the version-agnostic canonical identity.
		canonical, version := splitBundleVersion(bundleName)
		return []ExpandedRef{{Name: canonical + "#" + rest, Version: version}}
	}

	// Whole-bundle ref: enumerate every fragment in the bundle. A pinned
	// "@<commit>" enumerates that historical version (its fragment set may
	// differ from the default) and stamps every item with the commit so each
	// resolves at that version.
	canonical, version := splitBundleVersion(ref)
	// bundleAtVersion with no explicit commit re-derives the version from the
	// ref itself: the lockfile-pinned default when nothing is pinned, or the
	// exact historical version via the wired version resolver for "@<commit>".
	b, err := l.bundleAtVersion(ref, "")
	if err != nil {
		// A profile referenced this bundle but it didn't resolve (missing, or a
		// pinned version that failed to fetch). Warn so the gap is diagnosable —
		// silently dropping it produces context that is missing content with no
		// error (fault-tolerance: log, don't crash). Deduped process-wide:
		// startup assembles context more than once.
		l.warnUnresolvedBundle(ref, err)
		return nil
	}
	out := make([]ExpandedRef, 0, len(b.Fragments))
	for fragName := range b.Fragments {
		out = append(out, ExpandedRef{Name: canonical + remote.FragmentSelector + fragName, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
