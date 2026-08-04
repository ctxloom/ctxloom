package bundles

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
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
//     exposure surfaces wrap it in a gated Pipeline, management surfaces wrap
//     it in an ungated one.
//   - The gate can no longer be forgotten by OMISSION. A caller holding a
//     Loader cannot reach an exposed body at all — Loader's read methods are
//     named Read* and return candidate sets, and every delivery-shaped Get*
//     lives here. Not gating now requires writing the nil gate out loud.
//
// FAIL-CLOSED is preserved verbatim and is the gate's own contract: a resolve
// or store error inside the gate returns false, and false here means withheld.
// This stage adds no arm that can turn an error into an exposure — an item is
// admitted only when the gate positively says so.

// ContentGate is the per-item trust gate for resolved fragment/prompt content.
// It receives:
//
//   - ref     — the item's full ref, "<bundle>#<kind>/<name>"
//   - payload — the EXACT bytes about to be exposed (pre-mustache)
//   - form    — the LAYOUT form: "raw" | "distilled"
//   - signer  — the bundle's VERIFIED publisher identity, or "" for unsigned
//
// form is the layout axis ONLY. A countersignature binds a COMPOSITE
// attestation form that also names the item's role ("fragment/raw",
// "exec/hook", …), and that value is derived below the gate from the ref's kind
// — never passed in here. A gate call site therefore cannot name its own role,
// which is what stops a text item's approval from ever satisfying an
// executable's gate over identical bytes: the caller does not get a say.
//
// and reports whether the item may be exposed (true) or must be withheld
// (false).
//
// It takes BYTES, not a content hash, and that is the point: a hash can only be
// compared against a recorded hash, and a recorded hash is a file anything can
// write. Bytes can be VERIFIED against a signature. Handing the gate a
// precomputed hash would make the fast path "is this hash in the store?" — which
// is exactly the forgeable-file weakness the signature design exists to remove
// (spec §9.3, trap #2). The hash still exists, as an index; it just stopped
// being the authority.
//
// It is the PROCESS stage's decision function (see Pipeline), never the read
// stage's: a Loader reports what it holds and attaches the trust facts, and a
// Pipeline decides with this. A nil gate means no enforcement — the
// management/listing shape, which still resolves pending items so a human can
// review, accept or stamp them. Fail-closed semantics are the gate's own
// responsibility: a resolve/store error must return false (withhold), never
// default-allow.
type ContentGate func(ref string, payload []byte, form, signer string) bool

// Pipeline pairs a read stage (a *Loader) with the process stage's two
// policies: which trust gate decides admissibility, and which layout form to
// serve. A nil gate means no gating — the management/listing shape, which
// still resolves pending content so a human can review, accept or stamp it.
type Pipeline struct {
	loader *Loader
	gate   ContentGate

	// preferDistilled is the caller's raw-vs-distilled choice, held HERE
	// because form selection is processing: the read stage reports every form
	// the store holds and this stage picks one. It only ever PREFERS — an item
	// with no distilled form, or one whose author forbade distillation, still
	// serves raw.
	preferDistilled bool

	withheldMu sync.Mutex
	withheld   map[string]struct{}
}

// NewPipeline builds the process stage over loader. gate may be nil (no
// gating). Passing nil is a deliberate statement that this surface does not
// gate, never an omission: there is no other way to reach an exposed body.
func NewPipeline(loader *Loader, gate ContentGate, preferDistilled bool) *Pipeline {
	return &Pipeline{loader: loader, gate: gate, preferDistilled: preferDistilled}
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

// Gate returns the trust gate this pipeline decides with (nil when it does not
// gate). Lets a caller that must gate OTHER items through the IDENTICAL
// decision — builtin bundle fragments, which never resolve through a Loader at
// all — share this gate rather than building a redundant one.
func (p *Pipeline) Gate() ContentGate {
	if p == nil {
		return nil
	}
	return p.gate
}

// PreferDistilled reports the form preference this pipeline serves.
func (p *Pipeline) PreferDistilled() bool { return p != nil && p.preferDistilled }

// Withheld returns the item refs this pipeline's gate withheld over its
// lifetime, deduplicated and sorted. Empty when no gate is set or nothing was
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
// they may not. A nil gate admits everything. The gate is handed the EXACT
// bytes about to be exposed (pre-mustache) rather than a hash: a hash can only
// be compared against a recorded hash, and a recorded hash is a file anything
// can write, while bytes can be VERIFIED against a signature (spec §9.3, trap
// #2).
func (p *Pipeline) admit(ref string, payload []byte, form ContentForm, signer string) bool {
	if p.gate == nil {
		return true
	}
	if p.gate(ref, payload, string(form), signer) {
		return true
	}
	p.withheldMu.Lock()
	if p.withheld == nil {
		p.withheld = make(map[string]struct{})
	}
	p.withheld[ref] = struct{}{}
	p.withheldMu.Unlock()
	return false
}

// admitContent is admit for a resolved fragment/command, keyed on the read
// facts the reader attached: the item's own gate ref, the exact bytes it
// selected, the form it selected them in, and the bundle's verified signer.
func (p *Pipeline) admitContent(lc *LoadedContent) bool {
	if lc == nil {
		return false
	}
	return p.admit(lc.TrustRef, []byte(lc.Content), lc.Form, lc.Signer)
}

// admitSkill is admit for a resolved skill package. A skill's preimage is its
// manifest (BundleSkill.EffectiveManifest), not a single body blob, and the
// reader has already verified the on-disk tree against that same manifest — so
// what the gate decides on is exactly what was verified.
func (p *Pipeline) admitSkill(ls *LoadedSkill) bool {
	if ls == nil {
		return false
	}
	return p.admit(ls.TrustRef, ls.TrustPayload, FormRaw, ls.Signer)
}

// firstAdmissible returns the first candidate the gate admits, gating every
// earlier one on the way (so each rejection is tallied). A withheld match does
// not end the scan — a trusted copy of the same bare name in another bundle
// still wins. found reports whether the read produced any candidate at all,
// which is what separates "withheld" from "not found".
func (p *Pipeline) firstAdmissible(reads []*LoadedContent) (lc *LoadedContent, found bool) {
	for _, cand := range reads {
		if p.admitContent(cand) {
			return cand, true
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
	reads, err := p.loader.ReadFragment(name, p.preferDistilled)
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
	reads, err := p.loader.ReadCommand(name, p.preferDistilled)
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
	reads := p.loader.ReadBundleCommands(bundleRef, p.preferDistilled)
	if reads == nil {
		// The reader could not resolve the bundle at all and already warned.
		// nil, not an empty slice: "this bundle would not load" must stay
		// distinguishable from "this bundle ships no commands".
		return nil
	}
	out := make([]*LoadedContent, 0, len(reads))
	for _, lc := range reads {
		if p.admitContent(lc) {
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
	reads, err := p.loader.ReadFragmentAtVersion(ref, commit, p.preferDistilled)
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
	reads, err := p.loader.ReadCommandAtVersion(ref, commit, p.preferDistilled)
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
	reads := p.loader.ReadFragmentVersions(ref, commits, p.preferDistilled)
	seen := collections.NewSet[string]()
	var out []*LoadedContent
	for _, lc := range reads {
		if !p.admitContent(lc) {
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
