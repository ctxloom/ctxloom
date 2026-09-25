// Package writeprod is the write-discipline rule's production fixture.
package writeprod

import "os"

var created, _ = os.Create("seed") // want `<package-level> calls os.Create directly — raw filesystem writes must route through internal/shared/iox`

// Write writes around iox.
func Write() {
	_ = os.WriteFile("x", nil, 0o600) // want `Write calls os.WriteFile directly`
}
