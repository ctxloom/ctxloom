//go:build acceptance

// One format-aware assertion the three in steps_cli.go cannot express: that a
// command found NOTHING.
//
// "as", "matching" and "containing" all address a VALUE — a scalar to compare,
// a pattern to match, an array to look inside. An empty result has none: a
// listing with no entries renders as prose ("no trusted signers") and encodes
// as `null` or `[]`, so `as` reports "is a null, not a scalar" and `containing`
// reports the array is empty. Both are correct refusals, and neither is the
// claim the scenario makes.
//
// The claim is that the command looked and found nothing, and it has to hold in
// BOTH encodings — otherwise an absence scenario can only be written for the
// renderer, which is the coverage hole this whole conversion exists to close.
// So the structured branch requires the payload at PATH to be genuinely empty
// (null, or a zero-length array/object/string) while the rendered branch keeps
// the exact prose the scenario always asserted, carried in the step rather than
// an Examples column — the two branches assert the same fact and neither row
// needs a value the other row would ignore.
//
// Emptiness is asserted POSITIVELY (an empty array is empty) rather than by the
// absence of a name, because absence-satisfies-absence passes just as well
// against a payload that was never produced at all.
package acceptance

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

// registerCLIFormatSteps registers the empty-payload assertion.
func registerCLIFormatSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the output reports "([^"]*)" as empty, saying "([^"]*)"$`,
		func(c context.Context, path, saying string) error {
			return assertOutputReports(worldFrom(c), path, saying, func(v any) error {
				switch t := v.(type) {
				case nil:
					return nil
				case []any:
					if len(t) == 0 {
						return nil
					}
					return fmt.Errorf("holds %d element(s), so the command did find something", len(t))
				case map[string]any:
					if len(t) == 0 {
						return nil
					}
					return fmt.Errorf("holds %d key(s), so the command did find something", len(t))
				case string:
					if t == "" {
						return nil
					}
					return fmt.Errorf("is %q, so the command did find something", t)
				}
				return fmt.Errorf("is %v, so the command did find something", v)
			})
		})
}
