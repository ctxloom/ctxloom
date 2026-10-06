package operations

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// WriteConstraintChanges tells the user, per pin, that the manifest now asks
// for something the pin was not resolved from, and that only `deps upgrade`
// applies it. Pull, init and startup never move an existing pin, so without
// this a constraint edit would look applied when it is not.
func WriteConstraintChanges(w io.Writer, changes []ConstraintChange) {
	for _, c := range changes {
		fmt.Fprintf(w, "  %s: the manifest now asks for %s; the pin stays at %s (resolved from %s).\n",
			c.Identity, constraintLabel(c.Declared), gitutil.ShortSHA(c.SHA), constraintLabel(c.Pinned))
	}
	if len(changes) > 0 {
		fmt.Fprintln(w, "  Only 'ctxloom deps upgrade' moves a pin: run it to see the change, then 'ctxloom deps upgrade --yes' to apply it.")
	}
}

// constraintLabel names an empty constraint for a human.
func constraintLabel(expr string) string {
	if expr == "" {
		return "the default branch"
	}
	return expr
}
