// Package writeprod is the write-discipline rule's production fixture.
package writeprod

import (
	"os"
	xos "os"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
)

var created, _ = os.Create("seed") // want `<package-level> calls os.Create directly — raw filesystem writes must route through internal/shared/safefs`

// Write writes around safefs.
func Write() {
	_ = os.WriteFile("x", nil, 0o600) // want `Write calls os.WriteFile directly`
}

// Renamed resolves the qualifier through its import, not its spelling.
func Renamed() {
	_ = xos.WriteFile("x", nil, 0o600) // want `Renamed calls os.WriteFile directly`
}

// Held is an afero.Fs in a variable whose name says nothing about it.
func Held(store afero.Fs) {
	_ = store.Rename("a", "b") // want `Held calls \(afero.Fs\).Rename directly`
}

type wrapper struct{ afero.Fs }

// Promoted reaches afero's Create through an embedded field.
func Promoted(w wrapper) {
	_, _ = w.Create("x") // want `Promoted calls \(afero.Fs\).Create directly`
}

// Expression names afero's method as a value and calls it.
func Expression(m *afero.MemMapFs) {
	_, _ = (*afero.MemMapFs).Create(m, "x") // want `Expression calls \(afero.Fs\).Create directly`
}

// Sanctioned goes through the write library, whose name ends in "fs".
func Sanctioned(store afero.Fs) {
	_, _ = safefs.Create(store, "x")
}

type ledgerfs struct{}

func (ledgerfs) Create(string) error { return nil }

// NotAfero calls a Create that is not afero's, on a name ending in "fs".
func NotAfero() {
	var cfs ledgerfs
	_ = cfs.Create("x")
}
