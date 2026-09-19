//go:build !acceptance

package acceptance

import (
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestMain composes the shipped engines into the backend registry for every
// test in this binary: registration is explicit (no init), so a test binary
// that reads the registry composes it the same way the CLI does.
//
// This is the UNTAGGED twin of the TestMain in acceptance_test.go, which is
// behind //go:build acceptance. The files it serves — the registry↔feature
// agreement checks — are deliberately untagged so they run under plain
// `just test`, and that build compiles no other TestMain. Without this one
// nothing registers there, and every engine lookup fails as "unknown engine"
// while the tagged acceptance run stays green: the two builds would disagree
// about which engines exist. The guard is what keeps exactly one TestMain in
// each build.
func TestMain(m *testing.M) {
	engines.MustRegister()
	os.Exit(m.Run())
}
