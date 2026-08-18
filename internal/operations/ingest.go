package operations

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// contextSectionSeparator joins the sections of an assembled context. It is the
// same separator internal/shared/agent's contextSectionSep uses, so a context
// assembled here and one written by WriteContextFile split on the same
// boundaries (agent.ChunkContext relies on that).
const contextSectionSeparator = "\n\n---\n\n"

// contextIngest IS the ingest layer for assembled context: every fragment that
// reaches a session's context passes through add, whichever route brought it —
// a profile's selection, an explicit `-f` ask, a tag match, an always-on
// builtin-bundle injection, or a companion loadout. It is the one place that
// decides whether an arriving fragment is new content or a second copy of
// content already ingested, so ingest is IDEMPOTENT by construction rather than
// by each route remembering to check the others.
//
// THE RULE, in one sentence: two arriving fragments are the same content — and
// the second is dropped — when they name the SAME bundle-relative item (bundle
// path + item kind + item name, source-agnostic) AND their delivered bytes are
// identical ignoring surrounding whitespace; anything else is two fragments and
// both are assembled.
//
// Both halves of that conjunction are load-bearing, and each one is the answer
// to a case the other gets wrong:
//
//   - ITEM ALONE is too coarse. Two genuinely different bundles can sit at the
//     same repo-relative path in different repositories
//     ("<repoA>@bundles/std#fragments/rules" and "<repoB>@bundles/std#..."),
//     and collapsing those would silently drop one publisher's content in
//     favour of another's.
//   - CONTENT ALONE is too coarse, and is the identity that was already in use
//     downstream (internal/shared/agent's assembleDedupedContext, now keyed the
//     same way as here): two DIFFERENT fragments that happen to say the same
//     thing are two deliberate authored items, and delivering one of them is
//     data loss, not deduplication.
//
// Together they collapse exactly the cases that are one piece of content
// arriving twice:
//
//   - the same fragment selected by two profiles in an inheritance chain
//     (already collapsed upstream by dedupeFragmentRefs when the two asks spell
//     the same ref; caught here when they do not),
//   - a fragment that is INJECTED unconditionally as a builtin
//     ("builtin:isolation#fragments/isolation-axes") and ALSO selected by ref
//     through the bundle loader ("ctxloom:local@bundles/isolation#fragments/
//     isolation-axes"). The two spellings carry different SOURCES and so are
//     different strings and different trust keys, but trust.Ref.Key() is
//     source-agnostic, so both reduce to "isolation#fragments/isolation-axes"
//     and the identical bytes finish the match.
//
// ORDER: the FIRST occurrence is kept and a later duplicate is dropped, never
// the other way round. Callers ingest loader-resolved content before injected
// content, so the occurrence that survives is the one the user's profile or
// request actually selected — the more specific ask, and the one that carries
// variable substitution and any pinned content version. Keeping the LAST
// occurrence would instead move that content to the end of the assembled
// context, past everything selected after it, which changes what the model
// sees and defeats sortFragmentsByPriority's bookend placement. Nothing here
// sorts or reorders: parts are appended in arrival order and a dropped
// duplicate is simply not appended, so the surviving sequence is exactly the
// subsequence of first occurrences.
//
// TRUST: dedup runs strictly AFTER the content gate, on fragments the gate has
// already ALLOWED — a withheld fragment never reaches add at all (see
// loadFragmentRef/ResolveBuiltinBundleFragments, both of which drop a denied
// item before ingest). Two allowed occurrences therefore have no weaker and no
// stronger side to launder into each other, and collapsing them cannot expose a
// byte the gate did not already pass. This is also why the check must live here
// and not in dedupeFragmentRefs, which runs BEFORE the gate: collapsing two ref
// spellings there would pick one item's trust outcome to stand for the other's.
type contextIngest struct {
	// parts holds every ingested fragment in arrival order.
	parts []ingestedFragment
	// seen maps an identity to the ref of the occurrence that was kept, so a
	// drop can name both spellings.
	seen map[ingestIdentity]string
}

// ingestedFragment is one fragment as it entered the assembled context.
//
// Ref and Name are deliberately separate. Ref is the trust-grade item
// reference ("<source>#fragments/<name>") that identity is derived from; Name
// is whatever the calling surface reports the fragment as, which is not the
// same string on every route (regenerateContext labels loader-resolved
// fragments "<bundle>/<fragment>" and injected ones by their full ref) and
// which is written into delivered artifacts. Deriving one from the other would
// change bytes a caller already publishes.
type ingestedFragment struct {
	Ref     string
	Name    string
	Content string
}

// ingestIdentity is what "the same content" MEANS at ingest — see
// contextIngest's rule. item is trust.Ref.Key(), the source-agnostic
// "<bundle>#<kind>/<name>"; content is the delivered bytes with surrounding
// whitespace removed, because the routes trim at different moments (the loader
// route trims before variable substitution, the injection route trims the raw
// bytes) and edge whitespace is framing, not content.
type ingestIdentity struct {
	item    string
	content string
}

// newContextIngest returns an empty ingest accumulator.
func newContextIngest() *contextIngest {
	return &contextIngest{seen: make(map[ingestIdentity]string)}
}

// ingestWarn is the sink for add's cross-route duplicate diagnostic.
//
// It is a variable, not a direct clidiag.WarnOnce call, purely so a test can
// observe the diagnostic deterministically: WarnOnce dedups on the formatted
// line for the whole PROCESS, and the line here is built from the two refs
// alone, so whichever test collapses a given pair FIRST consumes the key and
// every later one sees silence regardless of what it asserts.
//
// WarnOnce is still the production behaviour, and deliberately: AssembleContext
// runs once per conversation turn, so a per-call warning would reprint the same
// line for the life of the session — the same reasoning warnSubstitutionFor
// documents for undefined variables.
var ingestWarn = func(format string, args ...any) {
	clidiag.WarnOnce("ctxloom", format, args...)
}

// add ingests one fragment, reporting whether it was ingested (true) or dropped
// as a second copy of content already ingested (false).
//
// SILENCE POLICY. A drop is SILENT when the duplicate arrived under the SAME
// ref as the occurrence already ingested: the two asks were textually identical,
// so there is no reading under which the user meant two different things, and
// the same collapse has always been silent upstream in dedupeFragmentRefs —
// warning about it would put a line on stderr for every existing configuration
// that composes overlapping profiles.
//
// No assembly reachable TODAY takes that branch — dedupeFragmentRefs collapses
// two identical ref strings before either reaches ingest — but this is the
// ingest layer and it may not assume its callers pre-deduped for it, so the
// branch is live and pinned (see the accumulator-level subtest in
// TestIngest_DropIsSilentForTheSameRefAndSpeaksForADifferentOne, which is
// deliberately not routed through AssembleContext for exactly that reason).
//
// A drop under a DIFFERENT ref WARNS, once per pair. That is the only case
// where the user wrote two textually different selections and got one fragment,
// so "I meant two different fragments and mis-spelled one" is a live reading
// that a silent drop would mask — precisely the mistake this codebase's silent
// no-op history is made of. It is also the new case: it is what an injected
// builtin that is ALSO selectable by ref produces, so the diagnostic appears
// exactly when a configuration starts relying on the idempotence rather than on
// every session.
func (in *contextIngest) add(f ingestedFragment) bool {
	id := ingestIdentity{item: ingestItemKey(f.Ref), content: strings.TrimSpace(f.Content)}
	if kept, dup := in.seen[id]; dup {
		if kept != f.Ref {
			ingestWarn(
				"the same fragment reached this context twice: %q is already assembled and %q is the same item with identical content, so the second copy was dropped "+
					"— if these were meant to be two DIFFERENT fragments, one of the two references is wrong",
				kept, f.Ref)
		}
		return false
	}
	in.seen[id] = f.Ref
	in.parts = append(in.parts, f)
	return true
}

// fragments reports every ingested fragment in arrival order, INCLUDING ones
// whose content is blank. join drops those from the assembled string, but a
// caller delivering fragments onward needs them: it is what lets
// agent.WriteContextFile tell "no context was configured" (no fragments at all)
// from "every configured fragment resolved to nothing" (fragments that assemble
// to zero bytes — agent.ErrNoContext), which are different facts and only one
// of them is fine.
func (in *contextIngest) fragments() []ingestedFragment {
	return in.parts
}

// join renders the ingested fragments as the assembled context string.
//
// Blank sections are omitted: a "---" separator with nothing on one side of it
// is framing around no content, and both other assemblers of this string
// (agent.AssembleContext and agent.assembleDedupedContext) already skip empty
// fragments. Returns "" when nothing non-blank was ingested.
func (in *contextIngest) join() string {
	parts := make([]string, 0, len(in.parts))
	for _, f := range in.parts {
		if strings.TrimSpace(f.Content) == "" {
			continue
		}
		parts = append(parts, f.Content)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, contextSectionSeparator)
}

// ingestItemKey reduces an item ref to the source-agnostic identity dedup keys
// on: trust.Ref.Key(), "<bundle>#<kind>/<name>".
//
// A ref that does not parse as an item ref is used verbatim. That is the
// fail-safe direction: an unparseable ref can then only ever match another
// occurrence spelled byte-for-byte the same way, so a ref this grammar does not
// understand is never collapsed into a DIFFERENT one.
func ingestItemKey(ref string) string {
	tRef, _, _, err := trust.ParseItemRef(ref)
	if err != nil {
		return ref
	}
	return tRef.Key()
}
