//go:build acceptance

package acceptance

import (
	"errors"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// gateContainerRow turns containercell.Select's decision into the godog
// outcome it names, for every @container step: Fail is an ERROR, Skip is
// godog.ErrSkip with the reason on the row's doc line, Proceed is nil.
//
// The decision — not Runtime.Available — is the thing to branch on. A step
// that skipped on Available alone read CTXLOOM_REQUIRE_DOCKER=1's Fail as a
// skip: the gate's "ran NOTHING" message went out under a SKIPPED banner and
// the run exited 0, which is the exact green-for-zero-coverage the floor
// exists to prevent. Routing every row through here keeps that one rule in
// one place (dockergate.RuntimeDecision) with one translation.
func gateContainerRow(w *World, row string, decision dockergate.Decision, msg string) error {
	switch decision {
	case dockergate.Fail:
		return errors.New(msg)
	case dockergate.Skip:
		fmt.Printf("SKIPPED (%s): %s\n", row, msg)
		w.docStepMaterialized = "SKIPPED: " + msg
		return godog.ErrSkip
	case dockergate.Proceed:
		return nil
	}
	return fmt.Errorf("%s: unknown container gate decision %v", row, decision)
}
