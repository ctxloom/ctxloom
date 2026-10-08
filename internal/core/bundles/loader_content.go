package bundles

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// LoadedContent is a fragment or command that has been through the PROCESS
// stage: one form SELECTED and those exact bytes ADMITTED by Pipeline. It
// is what gets delivered, and Pipeline is the only thing that produces one — a
// read produces an ItemRead, which carries no selected body at all.
type LoadedContent struct {
	Name         string   // Full name (bundle/item)
	Bundle       string   // Owning bundle's loader name (canonical ref for remote bundles)
	Item         string   // Bare fragment/prompt name within the bundle
	Version      string   // Bundle version
	Tags         []string // Combined tags
	Content      string   // The actual content
	Description  string   // A command's authored help text ("" for a fragment)
	Installation string   // Setup/installation instructions for tooling
	IsDistilled  bool     // Whether distilled version was used
	DistilledBy  string   // Model that created distillation
	// Form is the LAYOUT form these exact bytes were selected in ("raw" |
	// "distilled"). IsDistilled is this same fact as a bool; both come from the
	// form actually served, never re-derived (a re-derivation drops terms like
	// no_distill and describes bytes that were never served).
	Form ContentForm
	// ItemRef is the canonical ref of the item delivered, carried through
	// so a delivered item names its own provenance. See ItemRead,
	// which is where it originates.
	ItemRef string
	Exports EngineBlocks // per engine name, opaque; that engine decodes its block
	// Curated marks an item a profile named explicitly; an engine exports
	// it even where its block opts out, because naming it is the ask.
	Curated bool
	// Premise is the fragment's authored applicability condition, carried
	// through delivery so the assembler can tell an unconditional fragment
	// from one the acting agent must select. "" means unconditional — see
	// BundleFragment.Premise.
	Premise string
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
	Name         string       // Full name (bundle/item)
	Bundle       string       // Owning bundle's loader name (canonical ref for remote bundles)
	Item         string       // Bare fragment/command name within the bundle
	Version      string       // Bundle version
	Tags         []string     // Combined tags
	Description  string       // A command's authored help text ("" for a fragment)
	Installation string       // Setup/installation instructions for tooling
	DistilledBy  string       // Model that created the distillation, if any
	Exports      EngineBlocks // per engine name, opaque; that engine decodes its block
	// Premise is the read fact behind conditional delivery: the fragment's
	// authored applicability condition, "" for an unconditional fragment.
	// Commands never carry one. See BundleFragment.Premise.
	Premise string

	// Resolve is the item's own process-stage resolution (BundleFragment /
	// BundleCommand.Resolve), carried from the read UNCALLED: the read has no
	// form preference and picks nothing. The process stage calls it once and
	// gets served bytes and form from that single call — never
	// re-deriving "the bytes of this item" from separate fields.
	Resolve func(preferDistilled bool) ItemSurface

	// ItemRef is the ref this item is addressed by: the canonical
	// bundle-reference grammar's item selector (ItemRefFor,
	// ident.BundleRef.WithItem), "ctxloom+<class>:...#fragments/<name>" or
	// "...#prompts/<name>", minted from the bundle's HONEST typed source ref
	// (BundleRead.SourceRef — canonical for a cloned bundle, the local name
	// for a project bundle). A read FACT the reader establishes, never a decision.
	ItemRef string
	// Read is the owning bundle's read — the facts its reader established.
	//
	// Exported, and safe to be: BundleRead's axes are unexported and settable
	// only by a reader, so an ItemRead built from a struct literal outside this
	// package carries an UNCLAIMED read. The field hands out facts; it cannot
	// mint them.
	Read BundleRead
}

// ExportName returns the short, slash-command-facing name for this item:
// the owning bundle's last path segment plus the bare item name. Remote
// bundles are keyed by an identity carrying the whole repository URL; exporting
// that verbatim names the command after the entire URL (and ':' makes the
// filename invalid on Windows). Name remains the full identity — only the
// export-facing name shortens. Content without bundle metadata (builtin
// prompts) falls back to Name.
func (c *LoadedContent) ExportName() string {
	if c.Bundle == "" || c.Item == "" {
		return c.Name
	}
	return ExportBaseName(c.Bundle) + "/" + c.Item
}

// ExportBaseName shortens a bundle loader name to its last path segment,
// stripping the canonical ref's "<url>@<type>/" prefix when present.
func ExportBaseName(bundleName string) string {
	base := bundleName
	if i := strings.LastIndex(base, "@"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
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
	// `description:` key) — "" when the item has none. Populated by
	// ListAllCommands so a listing surface (the ACP agent
	// role's available_commands_update, B4) can advertise a real
	// human-readable description instead of fabricating one.
	Description string
	// Premise is the fragment's authored applicability condition, "" for an
	// unconditional fragment. Populated by ListAllFragments; it is what a
	// premise index is built from. See BundleFragment.Premise.
	Premise string
}

// itemTags is an item's listed tags: its bundle's, then its own, each once in
// first-seen order. An item commonly restates its bundle's tags.
func itemTags(bundleTags, own []string) []string {
	var tags []string
	seen := collections.NewSet[string]()
	for _, tag := range slices.Concat(bundleTags, own) {
		if !seen.Has(tag) {
			seen.Add(tag)
			tags = append(tags, tag)
		}
	}
	return tags
}

// ListAllFragments returns info about all fragments across all bundles.
func (c Catalog) ListAllFragments() []ContentInfo {
	var infos []ContentInfo
	seen := collections.NewSet[string]()

	for _, read := range c.Reads() {
		bundleInfo, bundle := read, read.Bundle

		// By name: a listing is what tag selection assembles context from,
		// so its order is the context's order and must not be the map's.
		for _, name := range slices.Sorted(maps.Keys(bundle.Fragments)) {
			frag := bundle.Fragments[name]
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
				Tags:     itemTags(bundle.Tags, frag.Tags),
				Bundle:   bundleInfo.DisplayName(),
				ItemType: "fragment",
				Premise:  frag.Premise,
			})
		}
	}

	return infos
}

// ListAllCommands returns info about all commands across all bundles. Unlike
// its ListAllFragments twin, it populates ContentInfo.Description from the
// command's own authored `description:` (BundleCommand.Description) —
// fragments carry no such field at all (BundleFragment has none), so a
// surface listing commands can advertise something real instead of a
// fabricated placeholder. This is a genuine, permanent shape difference
// between the two item kinds, not drift to reconcile.
// reprise:accept-drift
func (c Catalog) ListAllCommands() []ContentInfo {
	seen := collections.NewSet[string]()
	var infos []ContentInfo
	for _, read := range c.Reads() {
		bundleInfo, bundle := read, read.Bundle

		for _, name := range slices.Sorted(maps.Keys(bundle.Commands)) {
			prompt := bundle.Commands[name]
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
				Tags:        itemTags(bundle.Tags, prompt.Tags),
				Bundle:      bundleInfo.DisplayName(),
				ItemType:    "command",
				Description: prompt.Description,
			})
		}
	}

	return infos
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
// It carries NO form preference. What comes back can resolve every form the
// store has for the item (ItemRead.Resolve); the process stage picks.
func (c Catalog) ReadFragment(name string) ([]*ItemRead, error) {
	ask, err := ParseItemAsk(name)
	if err != nil {
		return nil, err
	}
	if !ask.Scoped {
		return c.searchFragment(name)
	}
	if ask.Kind != ident.KindFragment {
		return nil, fmt.Errorf("%w: %q selects a %s, not a %s", errs.ErrBadItemRef, name, ask.Kind, ident.KindFragment)
	}
	return c.fragmentFromBundle(ask.Bundle, ask.Item)
}

// ItemAsk is a parsed content ask: the bundle half, plus the selector when one
// was written. Kind comes from ident.ParseSelector and nowhere else, so
// "#prompts/" and "#commands/" resolve identically through EVERY reader —
// the single parser every Read* path below routes through, replacing the two
// that used to disagree (bundles.splitItemRef matched only the literal
// "commands"; bundles.selectorOf already used ParseSelector). See task
// stubborn-wow: before this, "bundle#prompts/name" resolved through anything
// built on ParseSelector but failed through ReadCommand with "invalid command
// reference" — the same alias, two different verdicts.
type ItemAsk struct {
	Bundle string         // verbatim: a canonical URI or a bare name (only meaningful when Scoped)
	Kind   ident.ItemKind // zero when Scoped is false
	Item   string
	Scoped bool
}

// ParseItemAsk parses ask's "#<kind>/<name>" selector, if it has one, through
// ident.ParseSelector's own kind vocabulary (fragments | commands | prompts |
// mcp | hooks | skills) — the SAME parser ident.ParseBundleRef's own selector
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
	kind, item, err := ident.ParseSelector(sel)
	if err != nil {
		return ItemAsk{}, fmt.Errorf("invalid item reference %q: %w", ask, err)
	}
	return ItemAsk{Bundle: base, Kind: kind, Item: item, Scoped: true}, nil
}

// itemRead builds the ItemRead every text kind shares: the item's identity
// and the read facts the process stage decides on. ItemRef is minted from
// the bundle's honest TYPED source ref (BundleRead.SourceRef) — canonical
// for a cloned bundle, the local name for a project bundle — through the canonical
// bundle-reference grammar (ItemRefFor), not hand-concatenated from
// Bundle.contentSourceRef's string. That is the SAME keying hook and MCP
// extraction uses. What differs between the kinds — a fragment's premise, a command's
// blocks — the caller sets on the result.
func itemRead(read BundleRead, kind ident.ItemKind, name string, body ItemBody, resolve func(bool) ItemSurface) (*ItemRead, error) {
	bundle := read.Bundle
	itemRef, err := ItemRefFor(read.SourceRef(), kind, name)
	if err != nil {
		return nil, fmt.Errorf("%s %q in bundle %q: %w", kind, name, bundle.Name, err)
	}
	return &ItemRead{
		Name:         fmt.Sprintf("%s/%s", bundle.Name, name),
		Bundle:       bundle.Name,
		Item:         name,
		Version:      bundle.Version,
		Tags:         itemTags(bundle.Tags, body.Tags),
		Installation: body.Installation,
		DistilledBy:  body.DistilledBy,
		Resolve:      resolve,
		ItemRef:      itemRef,
		Read:         read,
	}, nil
}

// fragmentRead is itemRead for a fragment, carrying its premise.
func fragmentRead(read BundleRead, fragName string, frag BundleFragment) (*ItemRead, error) {
	r, err := itemRead(read, ident.KindFragment, fragName, frag.ItemBody, frag.Resolve)
	if err != nil {
		return nil, err
	}
	r.Premise = frag.Premise
	return r, nil
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
	item, err := fragmentRead(read, fragName, frag)
	if err != nil {
		return nil, err
	}
	return []*ItemRead{item}, nil
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
		canonical, err := remote.CanonicalFragmentRef(name)
		if err != nil {
			// The ask is returned AS AUTHORED, which is the same
			// fault-tolerance the no-match branch below applies and is not the
			// same thing as the local fallback this replaced: an unaddressable
			// source stays unaddressable, so the load step withholds it and
			// says so, instead of resolving to first-party content the ask
			// never named.
			return name
		}
		return canonical
	}
	var matches []string
	for _, read := range c.Reads() {
		if _, ok := read.Bundle.Fragments[name]; ok {
			// A read whose own display name will not canonicalize is skipped
			// rather than failing the ask: the fragment may still be served by
			// another bundle, and refusing the whole search would make one
			// unaddressable bundle hide every copy of the fragment.
			match, err := remote.CanonicalBundleRef(read.DisplayName())
			if err != nil {
				continue
			}
			matches = append(matches, match)
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
// scanning past one it withholds — a usable copy in another bundle still wins
// — a decision the reader is in no position to make.
func (c Catalog) searchFragment(name string) ([]*ItemRead, error) {
	var out []*ItemRead
	for _, read := range c.Reads() {
		if frag, ok := read.Bundle.Fragments[name]; ok {
			item, err := fragmentRead(read, name, frag)
			if err != nil {
				// One unaddressable bundle costs its own copy of this
				// fragment, never the search: a copy in another bundle is
				// addressable and is what the caller asked for.
				c.rep.Warnf("skipping an unaddressable copy of fragment %q: %v", name, err)
				continue
			}
			out = append(out, item)
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
	if ask.Kind != ident.KindPrompt {
		return nil, fmt.Errorf("%w: %q selects a %s, not a %s", errs.ErrBadItemRef, name, ask.Kind, ident.KindPrompt)
	}
	return c.commandFromBundle(ask.Bundle, ask.Item)
}

// commandRead is itemRead for a command, carrying its per-engine blocks.
// ItemRef keeps the "prompts" kind segment (ident.KindPrompt, whose Dir()
// is "prompts") even though the load selector is "#commands/", so the
// item-kind rename does not invalidate existing trust grants.
func commandRead(read BundleRead, promptName string, prompt BundleCommand) (*ItemRead, error) {
	r, err := itemRead(read, ident.KindPrompt, promptName, prompt.ItemBody, prompt.Resolve)
	if err != nil {
		return nil, err
	}
	r.Description = prompt.Description
	r.Exports = prompt.Exports
	return r, nil
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
		case ident.KindPrompt:
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
		case ident.KindFragment, ident.KindMCP, ident.KindHook, ident.KindSkill:
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
		item, err := commandRead(read, name, bundle.Commands[name])
		if err != nil {
			// One unaddressable command costs itself, never the bundle's
			// other commands.
			c.rep.Warnf("bundle %q: skipping an unaddressable command: %v", bundleRef, err)
			continue
		}
		out = append(out, item)
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
	item, err := commandRead(read, promptName, prompt)
	if err != nil {
		return nil, err
	}
	return []*ItemRead{item}, nil
}

// searchCommand scans every bundle for a command with the given name and
// reports EVERY match, in List order — searchFragment's command twin, for the
// same reason: only the process stage can say which of them is admissible.
func (c Catalog) searchCommand(name string) ([]*ItemRead, error) {
	var out []*ItemRead
	for _, read := range c.Reads() {
		if prompt, ok := read.Bundle.Commands[name]; ok {
			item, err := commandRead(read, name, prompt)
			if err != nil {
				// See searchFragment: one unaddressable bundle costs its own
				// copy of this command, never the search.
				c.rep.Warnf("skipping an unaddressable copy of command %q: %v", name, err)
				continue
			}
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrCommandNotFound, name)
	}
	return out, nil
}

// ByTags returns fragments matching any of the given tags.
func (c Catalog) ByTags(tags []string) []ContentInfo {
	all := c.ListAllFragments()

	tagSet := collections.NewSetFrom(tags...)

	var matched []ContentInfo
	for _, info := range all {
		if slices.ContainsFunc(info.Tags, tagSet.Has) {
			matched = append(matched, info)
		}
	}

	return matched
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
// documentation in internal/core/profiles for the supported reference syntax.
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

// expandedFragmentName mints an ExpandedRef.Name from a canonical bundle ref
// and a "<kind>/<name>" selector. The name is the one ident.ParseSelector
// returns — normalised — never the selector text it was handed: a profile's
// selector and a bundle-authored fragment name are both outside input, and
// this Name is what every downstream surface prints and keys on.
func expandedFragmentName(canonical, sel string) (string, error) {
	kind, name, err := ident.ParseSelector(sel)
	if err != nil {
		return "", err
	}
	return canonical + "#" + kind.Dir() + "/" + name, nil
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
		return l.expandTargetedRef(ref, sep)
	}
	return l.expandWholeBundle(ref)
}

// expandTargetedRef expands a ref that selects one item, whose selector starts
// at sep; anything but a fragment selector expands to nothing.
func (l *Loader) expandTargetedRef(ref string, sep int) []ExpandedRef {
	bundleName := ref[:sep]
	rest := ref[sep+1:]
	if !strings.HasPrefix(rest, "fragments/") {
		// Targeted at commands, mcp, or unknown — not a fragment ref.
		return nil
	}
	// The bundle part may pin a content version ("bundle@<commit>"); keep it
	// (the read path resolves the cherry-pick at that commit) while the
	// emitted Name stays the version-agnostic canonical identity.
	canonical, version, err := splitBundleVersion(bundleName)
	if err != nil {
		l.Catalog().warnUnresolvedBundle(bundleName, err)
		return nil
	}
	name, err := expandedFragmentName(canonical, rest)
	if err != nil {
		l.Catalog().warnUnresolvedBundle(ref, err)
		return nil
	}
	return []ExpandedRef{{Name: name, Version: version}}
}

// expandWholeBundle expands a ref naming a whole bundle into every fragment it
// holds.
func (l *Loader) expandWholeBundle(ref string) []ExpandedRef {
	// Whole-bundle ref: enumerate every fragment in the bundle. A pinned
	// "@<commit>" enumerates that historical version (its fragment set may
	// differ from the default) and stamps every item with the commit so each
	// resolves at that version.
	canonical, version, err := splitBundleVersion(ref)
	if err != nil {
		l.Catalog().warnUnresolvedBundle(ref, err)
		return nil
	}
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
		name, err := expandedFragmentName(canonical, ident.KindFragment.Dir()+"/"+fragName)
		if err != nil {
			l.Catalog().warnUnresolvedBundle(ref, err)
			continue
		}
		out = append(out, ExpandedRef{Name: name, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
