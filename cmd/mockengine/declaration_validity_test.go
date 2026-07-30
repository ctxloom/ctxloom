package main

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// U079-F09 observes that run() never calls EngineCLI.Validate() on the
// declaration it is about to impersonate, and that a self-inconsistent one
// would degrade silently: a probe naming an undeclared flag never finds a value
// in argv, so it becomes an ordinary present:false row rather than a refusal.
//
// The mechanism is real. The consequence is not reachable through this binary,
// because Validate is TEST-only by design (see its doc) and the declarations
// run() can resolve are guarded by their own anti-drift tests. This test closes
// the residual gap that argument leaves: it enumerates EVERY registered backend
// the mock can select — including any added later, which is where a forgotten
// anti-drift test would actually appear — and validates every surface each one
// declares. A backend that ships a self-inconsistent declaration fails here,
// before any run can quietly report it as an absent surface.
func TestEveryImpersonableDeclarationIsSelfConsistent(t *testing.T) {
	var checked int
	for _, name := range backends.List() {
		clis, ok := backends.EngineCLIsFor(name)
		if !ok {
			continue // ACP-only backend: nothing for the mock to impersonate.
		}
		for _, cli := range clis {
			t.Run(name+"/"+string(cli.Surface), func(t *testing.T) {
				if err := cli.Validate(); err != nil {
					t.Errorf("declaration is self-inconsistent, so the mock would report its probes as absent rather than refusing: %v", err)
				}
			})
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no backend declared an engine CLI: this test validated nothing, which is this project's signature false green")
	}
}
