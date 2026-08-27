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
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

// registerCLIFormatSteps registers the format-aware assertions steps_cli.go's
// three (as/matching/containing) cannot express.
func registerCLIFormatSteps(ctx *godog.ScenarioContext) {
	// THE EMPTY CASE OF A STRUCTURED LIST, which none of steps_cli.go's three
	// format-aware steps can state: `as` demands a scalar and a null/[] is not
	// one, and `containing` demands a NON-empty array by design (its empty
	// guard is what stops "contains" from passing vacuously). So "nothing is
	// declared" — a fact the text renderer spells as a whole sentence — had no
	// structured spelling at all, and a scenario asserting it could not be
	// tabled by format without this.
	//
	// The prose argument is what the RENDERED formats say; the path is what the
	// structured ones must show empty. Both halves state the same fact, which is
	// what keeps the row honest either way.
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

	// The negation of steps_cli.go's "containing", PATH-SCOPED rather than a
	// whole-output substring search. A whole-output "does not contain X" is
	// defeated by a large structured payload that carries unrelated prose
	// (a composed fragment's own text) which can contain X as a substring of
	// an unrelated word — e.g. a payload proving profile "ops" did NOT survive
	// false-passed on a stray "the field split dr-OPS- every" in embedded
	// fragment content, once agent show started carrying full composed
	// context. Scoping to the array at PATH removes that false floor: the
	// rendered branch still matches prose as a substring (unchanged from
	// "containing"'s own text arm), since text renders stay short and never
	// carry that embedded prose.
	ctx.Step(`^the output reports "([^"]*)" not containing "([^"]*)"$`,
		func(c context.Context, path, expected string) error {
			w := worldFrom(c)
			format := formatAskedFor(w)
			if !format.Structured() {
				if strings.Contains(w.env.LastOutput(), expected) {
					return fmt.Errorf("the %s rendering unexpectedly reports %q; output:\n%s", format, expected, w.env.LastOutput())
				}
				return nil
			}
			doc, err := lastOutputStructured(w, format)
			if err != nil {
				return err
			}
			v, err := jsonAtPath(doc, path)
			if err != nil {
				return fmt.Errorf("%v; %s stdout:\n%s", err, format, w.env.LastStdout())
			}
			entries, ok := v.([]any)
			if !ok {
				return fmt.Errorf("the %s payload at %q is a %s, not an array; stdout:\n%s", format, path, jsonKind(v), w.env.LastStdout())
			}
			for _, e := range entries {
				if got, ok := jsonScalar(e); ok && got == expected {
					return fmt.Errorf("the %s payload at %q contains %q, which should not have survived; stdout:\n%s", format, path, expected, w.env.LastStdout())
				}
			}
			return nil
		})

	// The structural analogue of a rendered "Fragments (N):" / "Installed
	// bundles (N):" header — a map or array's cardinality at PATH — for a
	// payload that carries the entries themselves but no separate total
	// field of its own. Reuses the "as"/"containing" convention: the SAME
	// per-row string is reinterpreted by branch, parsed as the expected
	// count for the structured payload and matched as a literal substring
	// (the rendered header itself) for prose.
	ctx.Step(`^the output reports "([^"]*)" having "([^"]*)" entries$`,
		func(c context.Context, path, expected string) error {
			return assertOutputReports(worldFrom(c), path, expected, func(v any) error {
				n, err := strconv.Atoi(expected)
				if err != nil {
					return fmt.Errorf("cannot be checked: %q is not an entry count: %v", expected, err)
				}
				switch t := v.(type) {
				case []any:
					if len(t) != n {
						return fmt.Errorf("holds %d element(s), want %d", len(t), n)
					}
					return nil
				case map[string]any:
					if len(t) != n {
						return fmt.Errorf("holds %d key(s), want %d", len(t), n)
					}
					return nil
				}
				return fmt.Errorf("is a %s, not an array or object", jsonKind(v))
			})
		})
}
