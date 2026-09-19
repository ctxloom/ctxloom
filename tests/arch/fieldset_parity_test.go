//go:build arch

// FIELD-SET PARITY (docs/architecture/audit-2026-09-18/30-decided-
// architecture.md, Part 0 invariant 2): every value that crosses a process
// boundary is carried once, typed, by a codec whose two ends carry the same
// field set. This is the harness the migration's codecs register into as
// they land — the launch on the coordination wire, the identity in the
// process environment, the composite package's carrier — each as one
// parity.FieldSetPair whose two sides are read live. The table is EMPTY
// until the first codec exists; the comparator itself is proven by its
// negative self-probe in internal/testsupport/parity.
package arch

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/parity"
)

// fieldSetPairs is the table. A pair names both sides and reads each live.
var fieldSetPairs = []parity.FieldSetPair{}

// TestArch_FieldSetParity walks the table. While it is empty the test says
// so rather than passing silently: only the self-probe is doing live work
// until a codec registers.
func TestArch_FieldSetParity(t *testing.T) {
	if len(fieldSetPairs) == 0 {
		t.Log("fieldSetPairs is empty: no codec has registered a pair yet, so this gate proves nothing beyond " +
			"the comparator's own self-probe. The first is slice 8's launch.Launch ↔ coordgrpc.WireFieldNames.")
		return
	}
	parity.CheckFieldSets(t, fieldSetPairs)
}
