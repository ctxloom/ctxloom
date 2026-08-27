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
	b.WriteString("NAME and the premise under which it applies. When you are about to do\n")
	b.WriteString("something a premise describes, call the ctxloom `assemble_context` tool\n")
	b.WriteString("with those names (the `bundles` argument) and follow what it returns.\n")
	b.WriteString("Select on the premise, not on the name.\n")
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
		entries = append(entries, PremiseIndexEntry{Name: info.Name, Premise: info.Premise})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}
