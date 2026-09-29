//go:build arch

// FIELD-SET PARITY: every value that crosses a process
// boundary is carried once, typed, by a codec whose two ends carry the same
// field set. This is the harness the migration's codecs register into as
// they land — the launch on the coordination wire, the identity in the
// process environment, the composite package's carrier — each as one
// parity.FieldSetPair whose two sides are read live. The comparator itself
// is proven by its negative self-probe in internal/testsupport/parity.
package arch

import (
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/testsupport/parity"
)

// fieldSetPairs is the table. A pair names both sides and reads each live.
var fieldSetPairs = []parity.FieldSetPair{
	{
		Name:      "launch.Launch <-> the proto Launch",
		Left:      func() []string { return exportedFieldNames(reflect.TypeOf(launch.Launch{})) },
		Right:     coordgrpc.WireFieldNames,
		LeftName:  "launch.Launch",
		RightName: "the proto Launch (coordgrpc.WireFieldNames)",
	},
}

// exportedFieldNames reads a struct's field names live.
func exportedFieldNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		names = append(names, typ.Field(i).Name)
	}
	return names
}

// TestArch_FieldSetParity walks the table.
func TestArch_FieldSetParity(t *testing.T) {
	if len(fieldSetPairs) == 0 {
		t.Fatal("fieldSetPairs is empty: a codec exists (coordgrpc.EncodeLaunch) and must be registered here")
	}
	parity.CheckFieldSets(t, fieldSetPairs)
}
