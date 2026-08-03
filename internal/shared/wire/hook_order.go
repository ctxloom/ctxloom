package wire

// HookOrderStep is the spacing ctxloom leaves between consecutive hook order
// values when it assigns them: the first hook in an event gets 100, the second
// 200, and so on.
//
// # Why sparse integers, and not the alternatives
//
// Order exists to solve exactly one problem: inserting a hook must not change any
// OTHER hook's bytes. Under the retired `<NN>-<slug>` filename ordinal, inserting
// at the top of an event renamed every hook below it — their files moved, their
// bytes changed, and their countersignatures staled for a change they had not
// made. That is connascence of position wearing a different hat.
//
//   - DENSE integers (0, 1, 2…) fail the same way: an insert renumbers the tail.
//     They are the problem, not a fix.
//   - FRACTIONAL keys (100.5, 100.25…) never exhaust, but a float has more than
//     one canonical spelling in YAML (100.5 / 100.50 / 1.005e2) and repeated
//     halving accumulates binary-fraction error. These bytes are SIGNED; a
//     representation whose round-trip depends on an encoder's float formatting is
//     a signature that stops verifying for no visible reason.
//   - LEXICOGRAPHIC keys (LexoRank-style base62) never exhaust either and compare
//     as strings, but they are not hand-computable. These files are authored by
//     humans in a text editor, and a scheme where inserting between "aaa" and
//     "aab" requires running an algorithm trades a rare failure for a constant
//     cost.
//
// Sparse integers keep one canonical spelling per value, stay legible, and give
// roughly seven free subdivisions at any single insertion point (100 → 150 → 125
// → 112 → 106 → 103 → 101). When a gap does exhaust, the author renumbers — a
// deliberate, visible edit, not a silent side effect of an unrelated insert. That
// is the trade: an unbounded-but-opaque key, or a bounded-but-obvious one whose
// bound is far past any realistic hook count in one event.
const HookOrderStep = 100

// HookOrderLess is THE hook ordering rule, in one place, for every layer that has
// to apply it — the bundle-authoring shape, the content-tree surface, and the
// resolved apply-time list. Restating it per layer is how two layers come to
// disagree about execution order while both look right in isolation.
//
// The rule, given each hook's declared order (nil when it declared none) and a
// tie-break key:
//
//   - A DECLARED order sorts before an ABSENT one. A hook with no order made no
//     claim about where it goes, so it must not silently overtake a hook that did.
//     Sorting absent-as-zero would put every hook authored before this field
//     existed ahead of every ordered hook — a silent reorder, which is precisely
//     the failure this field exists to prevent. Sorting it last preserves the pure
//     APPEND semantics hooks already had.
//   - Lower declared order first.
//   - EQUAL orders are legitimate, not an error: "these two may run in either
//     order" is a real statement, and rejecting it would force authors to invent
//     precision they do not have. But resolution must still be TOTAL and stable or
//     the same inputs enumerate differently on two machines, so ties fall to the
//     key — the hook's name in the tree, its authored index on the apply path.
func HookOrderLess(aOrder *int, aKey string, bOrder *int, bKey string) bool {
	switch {
	case aOrder == nil && bOrder == nil:
		return aKey < bKey
	case aOrder == nil:
		return false
	case bOrder == nil:
		return true
	case *aOrder != *bOrder:
		return *aOrder < *bOrder
	default:
		return aKey < bKey
	}
}
