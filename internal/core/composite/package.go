package composite

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

var (
	// ErrUngatedAssembly is Assemble's refusal of an Ungated trust: a listing
	// surface's trust can never reach delivery.
	ErrUngatedAssembly = errors.New("composite: a package cannot be assembled over an ungated trust")
	// ErrItemWithheld is Assemble's refusal when an item the profile set
	// requires was withheld and Options.DropWithheld did not accept the loss.
	ErrItemWithheld = errors.New("composite: an item the profile set requires was withheld")
)

// Selection is what a profile set asks for, resolved and canonicalised: the
// assembly ORDER of fragments (deduplicated, bookended by priority), the
// curated command and skill asks, the bundle set the uncurated exports draw
// from, the profile-declared hooks and deny list, the variables, and the
// engine the profiles prefer. Pure: Select reads the catalog for what a tag
// or a whole-bundle ask expands to and nothing else.
type Selection struct {
	// Profiles are the resolved profile names, in the order asked.
	Profiles []string
	// Fragments is the assembly order: every fragment ask the profiles push,
	// the explicit asks and the tag matches folded in, one entry per item,
	// highest priority first and second-highest last.
	Fragments []FragmentAsk
	// Explicit names the asks the caller made BY NAME (resolved to their
	// qualified identity); an explicit ask loads even when it carries a
	// premise, because naming it is the selection.
	Explicit []string
	// Tags were the caller's tag asks; MissingTags names them all when the
	// whole tag selection matched nothing (a tag query is a union, so a
	// zero-fragment result cannot be attributed to one tag).
	Tags        []string
	MissingTags []string
	// Commands and Skills are the CURATED asks. Empty means uncurated: every
	// command or skill the Bundles ship exports, each still subject to its
	// own per-engine enablement.
	Commands []ItemAsk
	Skills   []ItemAsk
	// Bundles are the profiles' bundle refs, in order, first occurrence kept.
	Bundles []string
	// Hooks are the hooks the profiles declare directly, each with the
	// profile's own source ref so the gate keys it honestly.
	Hooks []ProfileHooks
	// Exclusions are the MCP server names the profiles veto (the union).
	Exclusions map[string]struct{}
	// Preference is the binding's delivery preference as WRITTEN (approach
	// name per kind); delivery validates it against the engine's Definition.
	// composite carries it, never interprets it. Set by the caller: it is the
	// agent binding's, not a profile's.
	Preference map[string]string
	// Variables are the merged profile variables, later profile wins.
	Variables map[string]string
	// DenyTools is the union of the profiles' deny lists, first-seen order.
	DenyTools []string
	// LLM is the engine label the profiles declared, first non-empty.
	LLM string
	// Declared maps each profile to the fragment names it pushed, so a
	// caller can say which PROFILE a withheld item cost.
	Declared map[string][]string
}

// FragmentAsk is one fragment in the assembly order: its qualified name, an
// optional pinned content version, and the priority that placed it.
type FragmentAsk struct {
	Name     string
	Version  string
	Priority int
}

// ItemAsk is one curated command or skill ask, as written (a "@<commit>"
// pin rides in the ref).
type ItemAsk struct{ Ref string }

// ProfileHooks are one profile's directly-declared hooks and the source ref
// the executable gate keys them by ("" for a project-authored profile).
type ProfileHooks struct {
	Profile   string
	SourceRef string
	Signer    string
	Hooks     wire.HooksConfig
}

// SelectRequest is the caller's explicit arm beside the profile set: named
// fragments and tag matches.
type SelectRequest struct {
	Fragments []string
	Tags      []string
	// Versions materialises a pinned historical version of a bundle for a
	// whole-bundle ask carrying "@<commit>", so its fragment set can be
	// enumerated; nil leaves such an ask unexpanded.
	Versions bundles.BundleVersionResolver
}

// Package is the composed loadout SOURCE: every admitted item, the assembled
// context, the premised fragments held back for the catalog, and the
// attestation. Immutable; only Assemble constructs one.
type Package struct {
	Context    Context
	Fragments  []Item[Fragment]
	Premised   []Item[Fragment]
	Commands   []Item[Command]
	Skills     []Item[Skill]
	Hooks      wire.HooksConfig
	MCP        map[string]wire.MCPServer
	Links      []LinkGroup
	DenyTools  []string
	Statusline bool
	// Selection is the selection this package was assembled from, for the
	// consumers that report it (the profile set, the engine the profiles
	// prefer).
	Selection Selection
	// Loaded names every fragment that loaded, in load order, a collapsed
	// duplicate included: its content IS in the context through the copy
	// that survived, so a report that omitted it would call a delivered
	// fragment missing.
	Loaded []string
	// Findings are the content-free facts a surface voices: an ask that did
	// not load, an undefined variable, a duplicate dropped.
	Findings []Finding

	attestation Attestation
}

// Context is the assembled context text and its hash.
type Context struct {
	Text string
	Hash string
}

// Fragment is one fragment as assembled: its qualified name, its body after
// variable substitution, and its premise ("" ⇒ unconditional).
type Fragment struct {
	Name    string
	Body    string
	Premise string
}

// Command is one slash-command item. Exports is per engine name, opaque:
// that engine's Exports decodes its own block against its ExportSchema.
type Command struct {
	Name        string
	Bundle      string // the owning bundle's loader name ("" for an injected command)
	Item        string
	Tags        []string // the effective tags (bundle tags merged onto the item's)
	Description string
	Body        string
	Exports     map[string][]byte
	// Curated marks a command a profile named explicitly; an engine exports
	// it even where the bundle's block opts out.
	Curated bool
}

// Skill is one Agent Skill package.
type Skill struct {
	Name        string
	Bundle      string
	Item        string
	Tags        []string
	Description string
	Files       []engine.SkillFile
	Exports     map[string][]byte
	Curated     bool
}

// LinkGroup is one link group the package delivers whole.
type LinkGroup struct {
	Server  string
	Members []trust.Ref
}

// Item pairs an admitted value with the read facts it was admitted on.
type Item[T any] struct {
	Value    T
	Ref      string
	Form     bundles.ContentForm
	Decision trust.Decision
	Signer   string
}

// Finding is one content-free fact about the assembly a surface voices.
type Finding struct {
	Kind    FindingKind
	Ref     string
	Version string
	Message string
}

// FindingKind names what a Finding is about.
type FindingKind string

const (
	// FindingLoadFailed: a fragment ask did not load (not found, unaddressable,
	// a pinned version that failed).
	FindingLoadFailed FindingKind = "load-failed"
	// FindingSubstitution: a fragment's template had an undefined variable or
	// did not parse.
	FindingSubstitution FindingKind = "substitution"
	// FindingDuplicate: the same item reached the context twice under two
	// refs; the second copy was dropped.
	FindingDuplicate FindingKind = "duplicate"
	// FindingCuratedSkipped: a curated command or skill ask did not resolve.
	FindingCuratedSkipped FindingKind = "curated-skipped"
)

// Attestation is the record of how a Package was decided: one row per
// delivered item plus the withheld tally.
type Attestation struct {
	Items    []ItemAttestation
	Withheld []string
}

// ItemAttestation is one delivered item's decision row.
type ItemAttestation struct {
	Ref      string
	Decision trust.Decision
	Hash     string
}

// Attestation returns the record that decided this package.
func (p Package) Attestation() Attestation { return p.attestation }

// Index is what the runner's search_library and the ctxloom:// resources
// enumerate: refs, kinds and descriptions of everything in the CATALOG the
// package was assembled from — not bytes, and not only the selection.
type Index struct{ Entries []IndexEntry }

// IndexEntry is one catalog item.
type IndexEntry struct {
	Ref         string
	Kind        trust.ItemKind
	Description string
	Premise     string
}

// Options are the assembly's knobs and the inputs the caller resolved
// outside the catalog.
type Options struct {
	// PreferDistilled picks the distilled form where a bundle offers one.
	PreferDistilled bool
	// Versions materialises a pinned historical version of a bundle for an
	// ask carrying "@<commit>" — a fetch beyond the resolved set, so it is an
	// adapter's and rides in here; nil refuses every pinned ask.
	Versions bundles.BundleVersionResolver
	// Pipeline is the injected-stage seam: a process stage built elsewhere
	// (a test's, over its own gate and link grant) that Assemble reads
	// through instead of building one from cat and tr. tr still decides
	// the ungated refusal.
	Pipeline *bundles.Pipeline
	// DropWithheld accepts a withheld required item instead of refusing.
	DropWithheld bool
	// Static writes premised fragments into the context instead of holding
	// them for the catalog: the consumer has no ctxloom behind it to pull one.
	Static bool
	// Builtin are unconditional fragment injections, already read (the
	// builtin bundles' fragments, companion loadouts).
	Builtin []Fragment
	// Commands are unconditional command injections, already read (ctxloom's
	// embedded commands).
	Commands []Command
	// Hooks, MCP, DenyTools and Statusline are the surfaces the caller
	// resolved: config-level hooks, ctxloom's own hooks for this run, the
	// bundle and builtin servers with the profiles' vetoes applied. Assemble
	// carries them; the link grant is derived from MCP.
	Hooks      wire.HooksConfig
	MCP        map[string]wire.MCPServer
	DenyTools  []string
	Statusline bool
}
