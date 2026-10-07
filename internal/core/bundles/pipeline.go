package bundles

import (
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// The PROCESS stage of the delivery pipeline
// (docs/design/engine-delivery-seam.design.md, "ALL processing lives in the
// middle").
//
// The read stage — Loader — REPORTS. It resolves an ask to every candidate it
// holds and drops nothing on policy grounds. Pipeline is the stage that
// processes: it picks the layout form to serve, withholds an item linked to an
// MCP server the run was not granted, tallies what it withheld, and hands the
// survivor on. One Loader serves management, listing and exposure alike;
// every delivery-shaped Get* lives here.

// Pipeline pairs a read stage (a *Loader) with the process stage's two
// policies: which linked groups are deliverable, and which layout form to
// serve.
type Pipeline struct {
	loader *Loader

	// links decides whether a LINKED item's group is deliverable to this run
	// (see LinkGrant). A surface that does not assemble a run passes
	// LinksUnchecked, and nil is an omission that withholds every linked item.
	links LinkGrant

	// preferDistilled is the caller's raw-vs-distilled choice, held HERE
	// because form selection is processing: the read stage carries every form
	// the store holds (ItemRead.Resolve) and this stage picks one. It only ever
	// PREFERS — an item with no distilled form, or one whose author forbade
	// distillation, still serves raw.
	preferDistilled bool

	withheldMu sync.Mutex
	withheld   map[string]struct{}
}

// NewPipeline builds the process stage over loader. A surface that does not
// assemble a run passes LinksUnchecked — a deliberate statement, spelled as a
// value; a nil links grant withholds every linked item.
func NewPipeline(loader *Loader, links LinkGrant, preferDistilled bool) *Pipeline {
	return &Pipeline{loader: loader, links: links, preferDistilled: preferDistilled}
}

// Loader returns the read stage this pipeline processes. Callers that need
// pure reads — listing bundles, resolving an ask to its canonical name,
// enumerating a profile's refs — use it directly; none of those touch item
// content, so none of them needs to gate.
func (p *Pipeline) Loader() *Loader {
	if p == nil {
		return nil
	}
	return p.loader
}

// PreferDistilled reports the form preference this pipeline serves.
func (p *Pipeline) PreferDistilled() bool { return p != nil && p.preferDistilled }

// Withheld returns the item refs this pipeline withheld over its
// lifetime, deduplicated and sorted. Empty when nothing was
// withheld. Callers surface the COUNT (or the refs) so the user knows content
// was hidden; returning refs and never bodies keeps the disclosure
// content-free.
func (p *Pipeline) Withheld() []string {
	p.withheldMu.Lock()
	defer p.withheldMu.Unlock()
	if len(p.withheld) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.withheld))
	for ref := range p.withheld {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// addressable reports whether ref parses in the canonical bundle-reference
// grammar. An item nothing can address is a load error: it is named, tallied,
// and not delivered.
func (p *Pipeline) addressable(ref string) bool {
	if _, err := ident.ParseBundleRef(ref); err != nil {
		p.loader.cat.rep.Warnf("withheld %s: its ref could not be parsed: %v", ref, err)
		p.recordWithheld(ref)
		return false
	}
	return true
}

// recordWithheld tallies a ref this pipeline did not deliver (deduplicated,
// lazily allocated). Refs only, never bodies: the disclosure stays content-free.
func (p *Pipeline) recordWithheld(ref string) {
	p.withheldMu.Lock()
	if p.withheld == nil {
		p.withheld = make(map[string]struct{})
	}
	p.withheld[ref] = struct{}{}
	p.withheldMu.Unlock()
}

// linkWithholds asks LinkWithholds with this pipeline's grant.
func (p *Pipeline) linkWithholds(read BundleRead, tags []string) (linkID, server string, withheld bool) {
	return LinkWithholds(p.links, read, tags)
}

// withholdLinked tallies and surfaces a link withhold.
func (p *Pipeline) withholdLinked(ref, linkID, server string) {
	p.recordWithheld(ref)
	WarnLinkWithheld(p.loader.cat.rep, ref, linkID, server)
}

// deliver is the process stage in one function: RESOLVE a form from everything
// the read reported, then withhold it if it is unaddressable or linked to an
// ungranted server. Returns nil when it is withheld.
func (p *Pipeline) deliver(r *ItemRead) *LoadedContent {
	if r == nil {
		return nil
	}
	s := r.Resolve(p.preferDistilled)
	if !p.addressable(r.TrustRef) {
		return nil
	}
	if id, server, withheld := p.linkWithholds(r.Read, r.Tags); withheld {
		p.withholdLinked(r.TrustRef, id, server)
		return nil
	}
	return &LoadedContent{
		Name:         r.Name,
		Bundle:       r.Bundle,
		Item:         r.Item,
		Version:      r.Version,
		Tags:         r.Tags,
		Content:      string(s.Body),
		Description:  r.Description,
		Installation: r.Installation,
		// From the form Resolve actually chose, never re-derived: a
		// re-derivation drops terms (no_distill) and describes bytes that were
		// never served.
		IsDistilled: s.Form == FormDistilled,
		DistilledBy: r.DistilledBy,
		Exports:     r.Exports,
		Form:        s.Form,
		TrustRef:    r.TrustRef,
		Premise:     r.Premise,
	}
}

// admitSkill reports whether a resolved skill package may be delivered: it
// exists and its ref is addressable.
func (p *Pipeline) admitSkill(ls *LoadedSkill) bool {
	return ls != nil && p.addressable(ls.TrustRef)
}

// deliverSkill is the process stage for a skill package: ADMIT the package,
// then SELECT the body an engine receives. Returns nil when the package may
// not be delivered.
//
// Selection only PREFERS, like BundleFragment.Resolve: a package with no
// distilled body serves raw. What it never does is deliver both bodies or an
// engine-side file for the unselected one; the materialization it applies
// (content.SkillMaterialization) is the same rule the content store's
// Skill.Materialize applies, so the two cannot disagree about which file is a
// body. A materialization failure withholds the package loudly rather than
// delivering an empty or two-bodied one.
func (p *Pipeline) deliverSkill(ls *LoadedSkill) *LoadedSkill {
	if !p.admitSkill(ls) {
		return nil
	}
	if id, server, withheld := p.linkWithholds(ls.Read, ls.Tags); withheld {
		p.withholdLinked(ls.TrustRef, id, server)
		return nil
	}
	paths := make([]string, len(ls.Files))
	for i, f := range ls.Files {
		paths[i] = f.RelPath
	}
	form := ident.FormRaw
	if p.preferDistilled && slices.Contains(content.SkillForms(paths), ident.FormDistilled) {
		form = ident.FormDistilled
	}
	layout, err := content.SkillMaterialization(paths, form)
	if err != nil {
		p.loader.cat.rep.Warnf("skill %q withheld: %v", ls.Name, err)
		p.recordWithheld(ls.TrustRef)
		return nil
	}
	files := make([]LoadedSkillFile, 0, len(layout))
	for _, f := range ls.Files {
		target, ok := layout[f.RelPath]
		if !ok {
			continue
		}
		f.RelPath = target
		files = append(files, f)
	}
	out := *ls
	out.Files = files
	return &out
}

// firstAdmissible returns the first candidate the pipeline delivers, tallying
// every earlier withhold on the way. A withheld match does not end the scan —
// a deliverable copy of the same bare name in another bundle still wins. found reports whether the read produced any candidate at all,
// which is what separates "withheld" from "not found".
func (p *Pipeline) firstAdmissible(reads []*ItemRead) (lc *LoadedContent, found bool) {
	for _, cand := range reads {
		if out := p.deliver(cand); out != nil {
			return out, true
		}
	}
	return nil, len(reads) > 0
}

// GetFragment resolves a fragment ask to the content that may be delivered.
// Name can be "fragment-name" (searches all bundles) or
// "bundle#fragments/name". A withheld item reports ErrFragmentWithheld, which
// is deliberately distinct from ErrFragmentNotFound: "you may not see this"
// and "this does not exist" are different facts and the user acts on them
// differently.
func (p *Pipeline) GetFragment(name string) (*LoadedContent, error) {
	reads, err := p.loader.ReadFragment(name)
	if err != nil {
		return nil, err
	}
	lc, found := p.firstAdmissible(reads)
	if lc != nil {
		return lc, nil
	}
	if found {
		return nil, fmt.Errorf("%w: %s", errs.ErrFragmentWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrFragmentNotFound, name)
}

// GetCommand is GetFragment's command counterpart. Name can be
// "command-name" (searches all bundles) or "bundle#commands/name".
func (p *Pipeline) GetCommand(name string) (*LoadedContent, error) {
	reads, err := p.loader.ReadCommand(name)
	if err != nil {
		return nil, err
	}
	lc, found := p.firstAdmissible(reads)
	if lc != nil {
		return lc, nil
	}
	if found {
		return nil, fmt.Errorf("%w: %s", errs.ErrCommandWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrCommandNotFound, name)
}

// CommandsFromBundleRef returns every command the bundle at bundleRef ships
// that may be delivered, in the reader's deterministic (name-sorted) order.
// A withheld command is omitted, so it is never exported as a slash command.
func (p *Pipeline) CommandsFromBundleRef(bundleRef string) []*LoadedContent {
	return deliverEach(p.loader.ReadBundleCommands(bundleRef), p.deliver)
}

// deliverEach runs one item's delivery step over a bundle's reads and keeps
// what it lets through. A nil read set is returned as nil, never an empty
// slice: the reader could not resolve the bundle at all (and already warned),
// and "this bundle would not load" must stay distinguishable from "this
// bundle ships nothing of this kind".
func deliverEach[R, D any](reads []*R, deliver func(*R) *D) []*D {
	if reads == nil {
		return nil
	}
	out := make([]*D, 0, len(reads))
	for _, r := range reads {
		if d := deliver(r); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// GetFragmentAtVersion resolves a fragment from a specific commit-version of
// its bundle. A fetch/parse failure surfaces as a resolve error and withholds
// only that version.
func (p *Pipeline) GetFragmentAtVersion(ref, commit string) (*LoadedContent, error) {
	reads, err := p.loader.ReadFragmentAtVersion(ref, commit)
	if err != nil {
		return nil, err
	}
	lc, found := p.firstAdmissible(reads)
	if lc != nil {
		return lc, nil
	}
	if found {
		return nil, fmt.Errorf("%w: %s", errs.ErrFragmentWithheld, ref)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrFragmentNotFound, ref)
}

// GetPromptAtVersion is GetFragmentAtVersion's prompt counterpart.
func (p *Pipeline) GetPromptAtVersion(ref, commit string) (*LoadedContent, error) {
	reads, err := p.loader.ReadCommandAtVersion(ref, commit)
	if err != nil {
		return nil, err
	}
	lc, found := p.firstAdmissible(reads)
	if lc != nil {
		return lc, nil
	}
	if found {
		return nil, fmt.Errorf("%w: %s", errs.ErrCommandWithheld, ref)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrCommandNotFound, ref)
}

// ResolveFragmentVersions resolves the fragment named by ref at each requested
// commit (use "" for the lockfile-pinned default), resolving each version
// independently and collapsing versions whose delivered bytes are identical to
// a single item. It is the multi-version coexistence primitive: several
// commit-versions of one ref carried in one assembly.
//
//   - A version that is withheld, or that failed to fetch/parse, is DROPPED —
//     the surviving versions still resolve.
//   - Dedup runs AFTER withholding, on the exact bytes delivered, keeping
//     the FIRST requested commit's resolution.
//
// Results preserve request order. Withheld versions are tallied through
// Withheld so the caller can surface "N withheld" without leaking content.
func (p *Pipeline) ResolveFragmentVersions(ref string, commits []string) []*LoadedContent {
	reads := p.loader.ReadFragmentVersions(ref, commits)
	seen := collections.NewSet[string]()
	var out []*LoadedContent
	for _, r := range reads {
		lc := p.deliver(r)
		if lc == nil {
			continue
		}
		key := hashContent([]byte(lc.Content))
		if seen.Has(key) {
			continue
		}
		seen.Add(key)
		out = append(out, lc)
	}
	return out
}

// GetSkill resolves an Agent Skill package by name — "skill-name" (searches
// all bundles) or "bundle#skills/name" — to the package that may be
// delivered.
func (p *Pipeline) GetSkill(name string) (*LoadedSkill, error) {
	reads, err := p.loader.ReadSkill(name)
	if err != nil {
		return nil, err
	}
	for _, ls := range reads {
		if out := p.deliverSkill(ls); out != nil {
			return out, nil
		}
	}
	if len(reads) > 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrSkillWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrSkillNotFound, name)
}

// ListAllSkills lists every Agent Skill package that may be delivered, across
// every bundle. An exposure surface's listing must not advertise a package it
// would then withhold, so the same admission runs here too.
func (p *Pipeline) ListAllSkills() ([]SkillInfo, error) {
	skills, err := p.loader.ReadAllSkills()
	if err != nil {
		return nil, err
	}
	out := make([]SkillInfo, 0, len(skills))
	for _, ls := range skills {
		if p.admitSkill(ls) {
			out = append(out, skillInfoFor(ls))
		}
	}
	return out, nil
}

// SkillsFromBundleRef returns every skill the bundle at bundleRef ships that
// may be delivered, in the reader's deterministic (name-sorted) order.
func (p *Pipeline) SkillsFromBundleRef(bundleRef string) []*LoadedSkill {
	return deliverEach(p.loader.ReadBundleSkills(bundleRef), p.deliverSkill)
}

// AdmittedInit is one companion's INIT loadout as this pipeline delivers it:
// the companion's ref and its typed setup-time fields.
type AdmittedInit struct {
	// Ref is the companion's bundle ref (ctxloom:companion@<bin>), the source
	// a consumer attributes each field to.
	Ref  string
	Init InitLoadout
}

// InitLoadouts is the process stage for the INIT half of every companion
// loadout the catalog read: the typed setup-time fields, in ref order. The
// companion binary that produced them was registered (companion add).
// Companions that declare no INIT section are skipped, not reported.
func (p *Pipeline) InitLoadouts() []AdmittedInit {
	var out []AdmittedInit
	for _, read := range p.loader.Reads() {
		if read.Provenance != ProvenanceCompanion || read.Init.IsZero() {
			continue
		}
		out = append(out, AdmittedInit{Ref: string(read.Key()), Init: read.Init})
	}
	return out
}
