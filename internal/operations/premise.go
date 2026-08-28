package operations

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// PremiseIndexEntry is one row of the premise index: the fragment NAME a
// selection callback quotes back, and the premise that decides whether it
// applies.
//
// The name is the identifier and there is deliberately no second one. It is
// exactly what AssembleContextRequest.Fragments already takes and what
// FragmentsLoaded/MissingFragments already report, so a selection round-trips
// through the existing surface without minting a parallel identity for the
// same fragment.
type PremiseIndexEntry struct {
	// Name is what a selection quotes back, and it must be the QUALIFIED
	// reference (bundle#fragments/name), never a bare name.
	//
	// A bare ask is resolved by Catalog.ResolveFragmentAsk, which on several
	// matches picks the first in List order -- sorted by bundle name -- and
	// only warns. That is a live hazard, not a hypothetical: `general` is
	// defined in SEVENTEEN code-review bundles in the default corpus, so a bare
	// ask for the structural design lens silently resolves to the
	// accessibility-i18n one. The agent gets a fragment it did not choose and
	// nothing downstream can tell.
	Name    string `json:"name"`
	Premise string `json:"premise"`
}

// premiseFilter is the SINGLE decision about conditional fragments, and it is
// an object rather than an inline test so it stays single.
//
// This package holds more than one assembler of a fragment set, and they are
// required to agree about what a context contains — a divergence ships an
// agent guidance that a second code path believes it already has. Any
// assembler that grows a premise rule must call THIS, never re-test
// Premise != "" itself: a second copy of the rule is a second policy, and the
// two disagree the first time either one changes.
type premiseFilter struct {
	// explicit is the set of fragment names the caller asked for BY NAME. An
	// explicit ask always loads: it is the selection callback itself, and a
	// premise that could veto it would make the loop unable to close.
	explicit map[string]bool
	index    []PremiseIndexEntry
	seen     map[string]bool
}

// newPremiseFilter builds the filter for one assembly. explicit is the
// already-resolved list of by-name fragment asks (AssembleContextRequest
// .Fragments after ResolveFragmentAsk), which bypass the filter entirely.
//
// THIS PARAMETER IS THE STOCHASTIC BOUNDARY, and that is why it is a bare list
// of names rather than a richer type. In production the selection is made by a
// model; in a test it is a literal slice. Everything on both sides of this call
// is deterministic, so no test needs a model to exercise assembly, withholding
// or the index.
//
// Two properties hold only while the parameter stays exactly this shape:
//
//   - A NAME IS CHECKABLE. The values are drawn from a closed vocabulary — the
//     catalog — so a name a model invents does not resolve and is reported
//     (MissingFragments) rather than silently absorbed. Free text could carry
//     anything; a name can only ever be right or absent.
//   - SUBSTITUTABILITY. A []string can be written by hand, so the deterministic
//     tests are not approximating the production path, they ARE it.
//
// So do not widen this to carry ordering, confidence, a rationale, or content
// from the model. Anything the assembly needs beyond the set of names must be
// DERIVED from those names here, where it can be tested — the moment a model's
// judgment reaches past this parameter, nondeterminism is behind the seam and
// every test downstream of it becomes a sampling exercise.
func newPremiseFilter(explicit []string) *premiseFilter {
	set := make(map[string]bool, len(explicit))
	for _, name := range explicit {
		set[name] = true
	}
	return &premiseFilter{explicit: set, seen: make(map[string]bool)}
}

// withhold reports whether this fragment is held back from unconditional
// assembly, recording an index row when it is.
//
// A fragment with NO premise is always loaded — absence asserts that it
// applies unconditionally (BundleFragment.Premise). That is what makes the
// mechanism additive: a corpus authoring no premises withholds nothing, builds
// an empty index, and assembles the exact bytes it did before.
func (f *premiseFilter) withhold(name, premise string) bool {
	if premise == "" || f.explicit[name] {
		return false
	}
	if !f.seen[name] {
		f.seen[name] = true
		f.index = append(f.index, PremiseIndexEntry{Name: name, Premise: premise})
	}
	return true
}

// entries returns the index rows for everything withheld, in the order the
// fragments would have been assembled. nil when nothing was withheld.
func (f *premiseFilter) entries() []PremiseIndexEntry {
	return f.index
}

// premiseIndexHeading opens the rendered index. It is addressed to the acting
// agent because the agent is the only party that can evaluate these premises:
// they turn on what is about to be done, which the host assembling the context
// does not know.
const premiseIndexHeading = "# Conditional guidance (not loaded)"

// The index's wording is LOAD-BEARING and was chosen by measurement, not taste.
// Against a fixture of 59 situations mined from real transcripts and labelled
// blind, three properties moved the result and the rest did not:
//
//   - PER-PREMISE judgement. Presenting the index as a menu to pick from makes
//     premises compete for one slot: recall ~0.49, with a single fragment
//     returned for ~93% of moments. Judging each premise on its own lifts recall
//     to ~0.76. This is the largest effect anyone measured, by a wide margin.
//   - BORDERLINE RESOLVES TOWARD INCLUDING. Recall ~0.83, because the two errors
//     are not equal: an over-offered fragment costs context, a withheld one is
//     never learned to exist.
//   - MATCH THE IMMINENT ACTION, not the surrounding context. Given a 25k-token
//     window of real history instead of a one-line intent, recall FELL from 0.93
//     to 0.57 while selections doubled -- the moment gets diluted by the span
//     around it.
//
// Wording that made no measurable difference: telling the model that context is
// costly, telling it that selecting nothing is often right, and broadening the
// premises themselves. Do not trade the three properties above for brevity.
//
// RenderPremiseIndex renders the index the agent selects from. It returns ""
// for an empty index, and every caller relies on that: an empty render is what
// keeps a premise-free corpus byte-identical to what it assembled before.
//
// It lists NAME and PREMISE and nothing else. The index is paid for on every
// session while the bodies it advertises are not, so anything added to a row
// is a cost multiplied by the whole corpus — a body, a tag dump or a
// description here would defeat the mechanism it serves.
func RenderPremiseIndex(entries []PremiseIndexEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(premiseIndexHeading)
	b.WriteString("\n\nThe guidance below is NOT in your context. Each line gives a fragment's\n")
	b.WriteString("NAME and the PREMISE under which it applies.\n\n")
	b.WriteString("Match against WHAT YOU ARE ABOUT TO DO -- the next action, not the whole\n")
	b.WriteString("conversation behind you. Take each premise ON ITS OWN and ask whether it\n")
	b.WriteString("describes that action; you are not picking the single best match, and\n")
	b.WriteString("several premises often apply at once.\n\n")
	b.WriteString("When it is a BORDERLINE call, include it. An unnecessary fragment costs a\n")
	b.WriteString("little context; one you withhold is never learned to exist and cannot be\n")
	b.WriteString("asked for.\n\n")
	b.WriteString("Select on the premise, not on the name. Then call the ctxloom\n")
	b.WriteString("`assemble_context` tool with the names you chose (the `bundles` argument)\n")
	b.WriteString("and follow what it returns.\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\n- %s: %s", e.Name, e.Premise)
	}
	return b.String()
}

// PremiseIndex reports every fragment in the catalog that carries a premise,
// as the index an agent selects from. Fragments with no premise are absent by
// construction: they are always loaded and there is nothing to decide about
// them.
//
// This is the CORPUS-wide producer. AssembleContextResult.PremiseIndex is the
// assembly-scoped one — what a particular assembly actually withheld — and the
// two answer different questions: this one says what exists, that one says
// what you did not get.
//
// Sorted by name so the same corpus always renders the same index; a row order
// that moved between calls would defeat prompt caching for every consumer.
func PremiseIndex(cat bundles.Catalog) ([]PremiseIndexEntry, error) {
	infos, err := cat.ListAllFragments()
	if err != nil {
		return nil, fmt.Errorf("failed to list fragments: %w", err)
	}
	var entries []PremiseIndexEntry
	for _, info := range infos {
		if info.Premise == "" {
			continue
		}
		// Qualify against the owning bundle. The assembly-scoped producer
		// already carries qualified refs (its rows come from the pipeline's
		// canonical ref); this one built bare names from ContentInfo, so the
		// two disagreed on the one thing a selection quotes back.
		entries = append(entries, PremiseIndexEntry{
			Name:    info.Bundle + "#fragments/" + info.Name,
			Premise: info.Premise,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}
