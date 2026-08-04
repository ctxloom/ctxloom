package bundles

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The PROCESS stage of the delivery pipeline
// (docs/design/engine-delivery-seam.design.md, "ALL processing lives in the
// middle").
//
// The read stage — Loader — REPORTS. It resolves an ask to every candidate it
// holds, attaches the trust FACTS it established while reading (the gate ref
// the item is addressed by, and the bundle's verified publisher identity), and
// drops nothing on policy grounds. It has no gate and no opinion about which
// items are admissible.
//
// Pipeline is the stage that decides. It applies the trust gate to the bytes
// the read reported, tallies what it withheld, and hands the survivor on. Two
// consequences are the point of the split:
//
//   - "Is this exposure gated" is a property of the PIPELINE, not of the
//     reader. One Loader serves management, listing and exposure alike;
//     exposure surfaces wrap it in a filtered Pipeline, management surfaces wrap
//     it in an unfiltered one.
//   - The filter can no longer be forgotten by OMISSION. A caller holding a
//     Loader cannot reach an exposed body at all — Loader's read methods are
//     named Read* and return candidate sets, and every delivery-shaped Get*
//     lives here. Not gating now requires writing the nil filter out loud.
//
// FAIL-CLOSED is preserved verbatim and is the Filter's own contract: a resolve
// or store error inside it withholds, and a withheld verdict here means the
// item is not delivered. This stage adds no arm that can turn an error into an
// exposure — an item is admitted only when the filter positively says so.

// Pipeline pairs a read stage (a *Loader) with the process stage's two
// policies: which Filter decides admissibility, and which layout form to
// serve. A nil filter means no gating — the management/listing shape, which
// still resolves pending content so a human can review, accept or stamp it.
type Pipeline struct {
	loader *Loader
	filter Filter

	// preferDistilled is the caller's raw-vs-distilled choice, held HERE
	// because form selection is processing: the read stage reports every form
	// the store holds (ItemRead.Forms) and this stage picks one. It only ever
	// PREFERS — an item with no distilled form, or one whose author forbade
	// distillation, still serves raw.
	preferDistilled bool

	withheldMu sync.Mutex
	withheld   map[string]struct{}
}

// NewPipeline builds the process stage over loader. filter may be nil (no
// gating). Passing nil is a deliberate statement that this surface does not
// gate, never an omission: there is no other way to reach an exposed body.
func NewPipeline(loader *Loader, filter Filter, preferDistilled bool) *Pipeline {
	return &Pipeline{loader: loader, filter: filter, preferDistilled: preferDistilled}
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

// Filter returns the filter this pipeline decides with (nil when it does not
// gate). Lets a caller that must decide about OTHER items through the IDENTICAL
// decision — builtin bundle fragments, which never resolve through a Loader at
// all — share this filter rather than building a redundant one.
func (p *Pipeline) Filter() Filter {
	if p == nil {
		return nil
	}
	return p.filter
}

// PreferDistilled reports the form preference this pipeline serves.
func (p *Pipeline) PreferDistilled() bool { return p != nil && p.preferDistilled }

// Withheld returns the item refs this pipeline's filter withheld over its
// lifetime, deduplicated and sorted. Empty when no filter is set or nothing was
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

// admit reports whether these bytes may be delivered, recording the ref when
// they may not and SURFACING a verdict that admits with a warning. A nil filter
// admits everything.
//
// The filter is handed the EXACT bytes about to be exposed (pre-mustache)
// rather than a hash: a hash can only be compared against a recorded hash, and a
// recorded hash is a file anything can write, while bytes can be VERIFIED
// against a signature (spec §9.3, trap #2).
//
// It also does the ONE thing a Filter deliberately cannot: emit. The filter is a
// pure function and returns its warning in Verdict.Detail; this is the delivery
// path's obligation to print it. A Detail nobody prints means an author never
// learns their signature went stale, which is the whole point of the
// admit-with-warning row.
func (p *Pipeline) admit(read BundleRead, ref string, payload []byte, form ContentForm) bool {
	if p.filter == nil {
		return true
	}
	tRef, _, _, err := trust.ParseItemRef(ref)
	if err != nil {
		// An item we cannot ADDRESS is one the decision function was never able
		// to key on. Withhold rather than expose content nothing evaluated —
		// the same fail-closed answer the filter itself gives for an
		// unaddressable ref, decided here because there is no Ref to hand it.
		clidiag.Warn("ctxloom", "withheld %s: %s", ref, ReasonUnaddressable.Explain(err.Error()))
		p.recordWithheld(ref)
		return false
	}
	v := p.filter.Admit(Exposure{Read: read, Ref: tRef, Bytes: payload, Form: form})
	ReportVerdict(ref, v)
	if v.Admit {
		return true
	}
	p.recordWithheld(ref)
	return false
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

// deliver is the process stage in one function: SELECT a form from everything
// the read reported, then decide whether those exact bytes may be exposed.
// Returns nil when they may not.
//
// The two steps are here, in this order, and nowhere else. Selecting first is
// what makes the gate's decision cover the bytes actually served — a grant
// binds the PAIR (bytes, form), so a raw-form approval must never validate a
// distilled exposure.
func (p *Pipeline) deliver(r *ItemRead) *LoadedContent {
	if r == nil {
		return nil
	}
	payload, form := r.Forms.Select(p.preferDistilled)
	if !p.admit(r.Read, r.TrustRef, payload, form) {
		return nil
	}
	return &LoadedContent{
		Name:         r.Name,
		Bundle:       r.Bundle,
		Item:         r.Item,
		Version:      r.Version,
		Tags:         r.Tags,
		Content:      string(payload),
		Installation: r.Installation,
		// From the form Select actually chose, never re-derived: a
		// re-derivation drops terms (no_distill) and describes bytes that were
		// never served.
		IsDistilled: form == FormDistilled,
		DistilledBy: r.DistilledBy,
		LLM:         r.LLM,
		Form:        form,
		TrustRef:    r.TrustRef,
		Signer:      r.Signer,
	}
}

// admitSkill is admit for a resolved skill package. A skill's preimage is its
// manifest (BundleSkill.EffectiveManifest), not a single body blob, and the
// reader has already verified the on-disk tree against that same manifest — so
// what the gate decides on is exactly what was verified.
func (p *Pipeline) admitSkill(ls *LoadedSkill) bool {
	if ls == nil {
		return false
	}
	return p.admit(ls.Read, ls.TrustRef, ls.TrustPayload, FormRaw)
}

// firstAdmissible returns the first candidate the gate admits, gating every
// earlier one on the way (so each rejection is tallied). A withheld match does
// not end the scan — a trusted copy of the same bare name in another bundle
// still wins. found reports whether the read produced any candidate at all,
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
	reads := p.loader.ReadBundleCommands(bundleRef)
	if reads == nil {
		// The reader could not resolve the bundle at all and already warned.
		// nil, not an empty slice: "this bundle would not load" must stay
		// distinguishable from "this bundle ships no commands".
		return nil
	}
	out := make([]*LoadedContent, 0, len(reads))
	for _, r := range reads {
		if lc := p.deliver(r); lc != nil {
			out = append(out, lc)
		}
	}
	return out
}

// GetFragmentAtVersion resolves a fragment from a specific commit-version of
// its bundle, gated by THAT version's own bytes — so each historical version
// is trusted or withheld independently. A fetch/parse failure surfaces as a
// resolve error; both it and a gate denial withhold only that version.
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
// commit (use "" for the lockfile-pinned default), gating each version
// independently and collapsing versions whose delivered bytes are identical to
// a single item. It is the multi-version coexistence primitive: several
// commit-versions of one ref carried in one assembly, each its own decision.
//
//   - A version the gate withholds, or that failed to fetch/parse, is DROPPED
//     (fail-closed) — the surviving versions still resolve, so a blacklisted
//     v2 vanishes while a trusted v1 remains.
//   - Dedup runs AFTER gating, on the exact bytes the gate decided on, keeping
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
		if p.admitSkill(ls) {
			return ls, nil
		}
	}
	if len(reads) > 0 {
		return nil, fmt.Errorf("%w: %s", errs.ErrSkillWithheld, name)
	}
	return nil, fmt.Errorf("%w: %s", errs.ErrSkillNotFound, name)
}

// ListAllSkills lists every Agent Skill package that may be delivered, across
// every bundle. An exposure surface's listing must not advertise a package it
// would then withhold, so the gate runs here too.
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
	reads := p.loader.ReadBundleSkills(bundleRef)
	if reads == nil {
		// See CommandsFromBundleRef: an unresolvable bundle is nil, never an
		// empty export set.
		return nil
	}
	out := make([]*LoadedSkill, 0, len(reads))
	for _, ls := range reads {
		if p.admitSkill(ls) {
			out = append(out, ls)
		}
	}
	return out
}
