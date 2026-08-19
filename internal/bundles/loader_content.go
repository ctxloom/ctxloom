package bundles

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// LoadedContent is a fragment or command that has been through the PROCESS
// stage: one form SELECTED and those exact bytes ADMITTED by the trust gate. It
// is what gets delivered, and Pipeline is the only thing that produces one — a
// read produces an ItemRead, which carries no selected body at all.
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
	// TrustRef / Signer are the read facts this delivery decision was made on,
	// carried through so a delivered item names its own provenance. See
	// ItemRead, which is where they originate.
	TrustRef string
	Signer   string
	Exports  map[string]string // Exported variables (from generators)
	LLM      LLMExports        // Per-LLM export settings (slash-command config)
}

// ItemRead is what a READ reports for one fragment or command: every form the
// store holds, the item's metadata, and the trust FACTS the reader established
// while reading it. It carries no selected body and no verdict — selecting a
// form and deciding admissibility are both PROCESS-stage work
// (docs/design/engine-delivery-seam.design.md, "ALL processing lives in the
// middle"), and Pipeline is what turns one of these into a LoadedContent.
//
// Keeping it a distinct type from LoadedContent is deliberate: a caller holding
// a read result must not be able to reach a body that no gate has cleared and
// no form has been chosen for. There is no such field to reach.
type ItemRead struct {
	Name         string     // Full name (bundle/item)
	Bundle       string     // Owning bundle's loader name (canonical ref for remote bundles)
	Item         string     // Bare fragment/command name within the bundle
	Version      string     // Bundle version
	Tags         []string   // Combined tags
	Installation string     // Setup/installation instructions for tooling
	DistilledBy  string     // Model that created the distillation, if any
	LLM          LLMExports // Per-LLM export settings (slash-command config)

	// Forms is every form of this item the store holds — the read's answer to
	// "what have you got", with nothing picked.
	Forms ContentForms

	// TrustRef is the ref the trust gate keys this item by: the canonical
	// bundle-reference grammar's item selector (ItemRefFor,
	// trust.BundleRef.WithItem), "ctxloom+<class>:...#fragments/<name>" or
	// "...#prompts/<name>", minted from the bundle's HONEST typed source ref
	// (BundleRead.SourceRef — canonical for a cloned bundle so its text gates
	// like an executable, the local name for a project bundle so its text
	// auto-trusts). A read FACT the reader establishes, never a decision.
	TrustRef string
	// Signer is the owning bundle's VERIFIED publisher identity, or "" when the
	// bundle is unsigned or was signed by a key this machine does not trust to
	// publish. It is Bundle.Signer() carried through — stamped only by a load
	// path that actually verified a signature, before any parse — so it is never
	// a claim the content made about itself.
	//
	// Establishing it is READING: the reader keeps its signature awareness. What
	// it no longer does is act on it.
	//
	// It is the COLLAPSED form of two of Read's axes and cannot express the
	// third: signing.VerifyPublisher returns "" for both "unsigned" and "signed
	// by a key we do not trust", and nothing about it can say "invalid". Read is
	// the un-collapsed truth and is what the Authorizer decides on; this stays
	// because a DELIVERED item names its own publisher.
	Signer string

	// Read is the owning bundle's read — the trust FACTS its reader established,
	// on all three axes. It is what Authorizer.Admit decides on (bundles.Exposure).
	//
	// Exported, and safe to be: BundleRead's axes are unexported and settable
	// only by a reader, so an ItemRead built from a struct literal outside this
	// package carries an UNCLAIMED read, which every Authorizer withholds. The field
	// hands out facts; it cannot mint them.
	Read BundleRead
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
	ClaudeCode ClaudeCodeConfig `yaml:"claude-code"`
	Codex      CodexConfig      `yaml:"codex"`
	Kiro       KiroConfig       `yaml:"kiro"`
	Opencode   OpencodeConfig   `yaml:"opencode"`
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
func (c Catalog) ListAllFragments() ([]ContentInfo, error) {
	var infos []ContentInfo
	seen := collections.NewSet[string]()

	for _, read := range c.Reads() {
		bundleInfo, bundle := read, read.Bundle

		for name, frag := range bundle.Fragments {
			// Use bundleInfo.Name (full path) instead of bundle.Name (just filename)
			key := bundleInfo.DisplayName() + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:     name,
				FileName: name + ".yaml",
				Path:     bundle.Path,
				Source:   bundleInfo.DisplayName(),
				Tags:     slices.Concat(bundle.Tags, frag.Tags),
				Bundle:   bundleInfo.DisplayName(),
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
func (c Catalog) ListAllCommands() ([]ContentInfo, error) {
	seen := collections.NewSet[string]()
	var infos []ContentInfo
	for _, read := range c.Reads() {
		bundleInfo, bundle := read, read.Bundle

		for name, prompt := range bundle.Commands {
			// Use bundleInfo.Name (normalized full path) instead of bundle.Name (just filename)
			key := bundleInfo.DisplayName() + "/" + name
			if seen.Has(key) {
				continue
			}
			seen.Add(key)
			infos = append(infos, ContentInfo{
				Name:        name,
				FileName:    name + ".yaml",
				Path:        bundle.Path,
				Source:      bundleInfo.DisplayName(),
				Tags:        slices.Concat(bundle.Tags, prompt.Tags),
				Bundle:      bundleInfo.DisplayName(),
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
// It carries NO form preference. What comes back holds every form the store has
// for the item (ItemRead.Forms); the process stage picks.
func (c Catalog) ReadFragment(name string) ([]*ItemRead, error) {
	ask, err := ParseItemAsk(name)
	if err != nil {
		return nil, err
	}
	if !ask.Scoped {
		return c.searchFragment(name)
	}
	if ask.Kind != trust.KindFragment {
		return nil, fmt.Errorf("%w: %q selects a %s, not a %s", errs.ErrBadItemRef, name, ask.Kind, trust.KindFragment)
	}
	return c.fragmentFromBundle(ask.Bundle, ask.Item)
}

// ItemAsk is a parsed content ask: the bundle half, plus the selector when one
// was written. Kind comes from trust.ParseSelector and nowhere else, so
// "#prompts/" and "#commands/" resolve identically through EVERY reader —
// the single parser every Read* path below routes through, replacing the two
// that used to disagree (bundles.splitItemRef matched only the literal
// "commands"; bundles.selectorOf already used ParseSelector). See task
// stubborn-wow: before this, "bundle#prompts/name" resolved through anything
// built on ParseSelector but failed through ReadCommand with "invalid command
// reference" — the same alias, two different verdicts.
type ItemAsk struct {
	Bundle string         // verbatim: a canonical URI or a bare name (only meaningful when Scoped)
	Kind   trust.ItemKind // zero when Scoped is false
	Item   string
	Scoped bool
}

// ParseItemAsk parses ask's "#<kind>/<name>" selector, if it has one, through
// trust.ParseSelector's own kind vocabulary (fragments | commands | prompts |
// mcp | hooks | skills) — the SAME parser trust.ParseBundleRef's own selector
// half uses, so every reader judges a selector by the kind it names rather
// than by the mere presence of "#" or by matching one literal spelling.
//
// Scoped is false for an ordinary whole-bundle/bare-name ask (no "#"); the
// caller searches or looks up by the ORIGINAL ask string in that case, not by
// Bundle (which is empty). A malformed or unrecognized selector (ParseSelector
// errored — an unknown kind word, or a selector with no "/") is a genuine
// parse error and is returned as one; it is NOT downgraded to Scoped=false,
// because a caller that swallows it and falls through to a name search would
// search using a string that contains a literal "#", which can never match a
// bundle or item name — that is a worse silence than reporting the malformed
// selector plainly.
//
// ParseItemAsk does NOT itself decide whether a well-formed selector's kind
// is the one the caller serves — that a fragment reader was asked for
// "#commands/x" is not a parse error, it is a caller-level kind mismatch (see
// each Read* caller below).
func ParseItemAsk(ask string) (ItemAsk, error) {
	base, sel, found := strings.Cut(ask, "#")
	if !found {
		return ItemAsk{}, nil
	}
	kind, item, err := trust.ParseSelector(sel)
	if err != nil {
		return ItemAsk{}, fmt.Errorf("invalid item reference %q: %w", ask, err)
	}
	return ItemAsk{Bundle: base, Kind: kind, Item: item, Scoped: true}, nil
}

// fragmentRead builds the ItemRead for a fragment: every form the store holds
// plus the trust facts the process stage decides on. TrustRef is minted from
// the bundle's honest TYPED source ref (BundleRead.SourceRef) — canonical for
// a cloned bundle so its text gates like an executable, the local name for a
// project bundle so its text auto-trusts — through the canonical
// bundle-reference grammar (ItemRefFor), not hand-concatenated from
// Bundle.contentSourceRef's string. That is the SAME keying the exec gate
// uses.
func fragmentRead(read BundleRead, fragName string, frag BundleFragment) *ItemRead {
	bundle := read.Bundle
	return &ItemRead{
		Name:         fmt.Sprintf("%s/%s", bundle.Name, fragName),
		Bundle:       bundle.Name,
		Item:         fragName,
		Version:      bundle.Version,
		Tags:         slices.Concat(bundle.Tags, frag.Tags),
		Installation: frag.Installation,
		DistilledBy:  frag.DistilledBy,
		Forms:        frag.Forms(),
		TrustRef:     ItemRefFor(read.SourceRef(), trust.KindFragment, fragName),
		Signer:       bundle.Signer(),
		Read:         read,
	}
}

// fragmentFromBundle loads a specific bundle and reports the named fragment —
// the single-candidate case of ReadFragment.
func (c Catalog) fragmentFromBundle(bundleName, fragName string) ([]*ItemRead, error) {
	read, err := c.Read(bundleName)
	if err != nil {
		return nil, err
	}
	frag, ok := read.Bundle.Fragments[fragName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrFragmentNotFound, fragName, bundleName)
	}
	return []*ItemRead{fragmentRead(read, fragName, frag)}, nil
}

// ResolveFragmentAsk resolves a user-supplied fragment ask to the canonical
// name shape the assembly pipeline carries (see ExpandBundleRefs). A
// qualified ask ("bundle#fragments/name") canonicalizes its bundle part
// directly. A bare name searches all bundles: a unique match qualifies it;
// several matches resolve deterministically to the first in List order (List
// sorts by bundle name) with a warning naming the alternatives; no match
// returns the ask unchanged so the load step reports it (fault-tolerance:
// an explicit ask is never dropped silently).
func (c Catalog) ResolveFragmentAsk(name string) string {
	if strings.Contains(name, "#") {
		return remote.CanonicalFragmentRef(name)
	}
	var matches []string
	for _, read := range c.Reads() {
		if _, ok := read.Bundle.Fragments[name]; ok {
			matches = append(matches, remote.CanonicalBundleRef(read.DisplayName()))
		}
	}
	if len(matches) == 0 {
		return name
	}
	if len(matches) > 1 {
		c.warnAmbiguousFragment(name, matches, matches[0])
	}
	return matches[0] + remote.FragmentSelector + name
}

// searchFragment scans every bundle for a fragment with the given name and
// reports EVERY match, in List order (bundle-name sorted, so the order is
// deterministic). Reporting all of them is what lets the process stage keep
// scanning past one it withholds — a trusted copy in another bundle still wins
// — a decision the reader is in no position to make.
func (c Catalog) searchFragment(name string) ([]*ItemRead, error) {
	var out []*ItemRead
	for _, read := range c.Reads() {
		if frag, ok := read.Bundle.Fragments[name]; ok {
			out = append(out, fragmentRead(read, name, frag))
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
func (c Catalog) ReadCommand(name string) ([]*ItemRead, error) {
	ask, err := ParseItemAsk(name)
	if err != nil {
		return nil, err
	}
	if !ask.Scoped {
		return c.searchCommand(name)
	}
	if ask.Kind != trust.KindPrompt {
		return nil, fmt.Errorf("%w: %q selects a %s, not a %s", errs.ErrBadItemRef, name, ask.Kind, trust.KindPrompt)
	}
	return c.commandFromBundle(ask.Bundle, ask.Item)
}

// commandRead builds the ItemRead for a command. See fragmentRead — the same
// read facts, minted the same way (ItemRefFor over the typed SourceRef).
// TrustRef keeps the "prompts" kind segment (trust.KindPrompt, whose Dir() is
// "prompts") even though the load selector is "#commands/", so the item-kind
// rename does not invalidate existing trust grants.
func commandRead(read BundleRead, promptName string, prompt BundleCommand) *ItemRead {
	bundle := read.Bundle
	return &ItemRead{
		Name:         fmt.Sprintf("%s/%s", bundle.Name, promptName),
		Bundle:       bundle.Name,
		Item:         promptName,
		Version:      bundle.Version,
		Tags:         slices.Concat(bundle.Tags, prompt.Tags),
		Installation: prompt.Installation,
		DistilledBy:  prompt.DistilledBy,
		LLM:          prompt.LLM,
		Forms:        prompt.Forms(),
		TrustRef:     ItemRefFor(read.SourceRef(), trust.KindPrompt, promptName),
		Signer:       bundle.Signer(),
		Read:         read,
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
func (c Catalog) ReadBundleCommands(bundleRef string) []*ItemRead {
	if ask, err := ParseItemAsk(bundleRef); err == nil && ask.Scoped {
		switch ask.Kind {
		case trust.KindPrompt:
			// An explicit "#commands/" (or legacy "#prompts/") cherry-pick
			// NAMES a command: resolve exactly that one, through the same
			// single-item path ReadCommand's "bundle#commands/name" form
			// already uses, rather than silently reporting zero commands for
			// a ref that asked for one by name. A genuine failure (bundle or
			// command missing) is still loud — via commandFromBundle's own
			// error — and never the misleading "bundle not found: <whole
			// ref>", which would name an existing bundle as missing.
			reads, err := c.commandFromBundle(ask.Bundle, ask.Item)
			if err != nil {
				c.warnUnresolvedBundle(bundleRef, err)
				return nil
			}
			return reads
		case trust.KindFragment, trust.KindMCP, trust.KindHook, trust.KindSkill:
			// A fragment/MCP/hook/skill cherry-pick legitimately ships no
			// COMMANDS — considered, not overlooked. Fragments resolve
			// through ExpandBundleRefs/ReadFragment, MCP/hooks through
			// config.loadMCPFromBundleRef/loadHooksFromBundleRef, skills
			// through ReadBundleSkills below; none of those routes through
			// here, so silence names a true fact rather than withholding one.
			return nil
		}
	}
	read, err := c.Read(bundleRef)
	if err != nil {
		// Same silent-export defect as SkillsFromBundleRef, same fix, same
		// warner expandBundleRef already uses: writing zero command
		// files because the bundle would not load must not look like a bundle
		// that ships no commands.
		c.warnUnresolvedBundle(bundleRef, err)
		return nil
	}
	bundle := read.Bundle
	names := make([]string, 0, len(bundle.Commands))
	for name := range bundle.Commands {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*ItemRead, 0, len(names))
	for _, name := range names {
		out = append(out, commandRead(read, name, bundle.Commands[name]))
	}
	return out
}

// commandFromBundle loads a specific bundle and reports the named command —
// the single-candidate case of ReadCommand.
func (c Catalog) commandFromBundle(bundleName, promptName string) ([]*ItemRead, error) {
	read, err := c.Read(bundleName)
	if err != nil {
		return nil, err
	}
	prompt, ok := read.Bundle.Commands[promptName]
	if !ok {
		return nil, fmt.Errorf("%w: %q in bundle %q", errs.ErrCommandNotFound, promptName, bundleName)
	}
	return []*ItemRead{commandRead(read, promptName, prompt)}, nil
}

// searchCommand scans every bundle for a command with the given name and
// reports EVERY match, in List order — searchFragment's command twin, for the
// same reason: only the process stage can say which of them is admissible.
func (c Catalog) searchCommand(name string) ([]*ItemRead, error) {
	var out []*ItemRead
	for _, read := range c.Reads() {
		if prompt, ok := read.Bundle.Commands[name]; ok {
			out = append(out, commandRead(read, name, prompt))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrCommandNotFound, name)
	}
	return out, nil
}

// ByTags returns fragments matching any of the given tags.
func (c Catalog) ByTags(tags []string) ([]ContentInfo, error) {
	all, err := c.ListAllFragments()
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
	read, err := l.bundleAtVersion(ref, "")
	if err != nil {
		// A profile referenced this bundle but it didn't resolve (missing, or a
		// pinned version that failed to fetch). Warn so the gap is diagnosable —
		// silently dropping it produces context that is missing content with no
		// error (fault-tolerance: log, don't crash). Deduped process-wide:
		// startup assembles context more than once.
		l.Catalog().warnUnresolvedBundle(ref, err)
		return nil
	}
	out := make([]ExpandedRef, 0, len(read.Bundle.Fragments))
	for fragName := range read.Bundle.Fragments {
		out = append(out, ExpandedRef{Name: canonical + remote.FragmentSelector + fragName, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
