package cli

import (
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// The launch's delivery plan, rendered for a human: which root each surface
// lands under. A route onto the project root or the working directory is
// named UNSAFE wherever it is shown — the dry-run plan and the launch banner
// — because it is the one thing a run can do to the shared tree, and it is
// reached only by the binding's roots: selection (ruled 2026-09-21).

// routeJSON is one static route of the plan on the wire.
type routeJSON struct {
	Kind     string `json:"kind"`
	Approach string `json:"approach"`
	Root     string `json:"root"`
	// Unsafe marks a route onto the shared project tree (or the working
	// directory): selected on the binding, never a default.
	Unsafe bool `json:"unsafe"`
}

// unsafeRoot reports whether a root is the shared tree a run writes only by
// selection.
func unsafeRoot(r present.RootKind) bool {
	return r == present.RootProjectRoot || r == present.RootWorkDir
}

// deliveryRoutes is the plan's static routes in plan order.
func deliveryRoutes(plan delivery.Plan) []routeJSON {
	out := make([]routeJSON, 0, len(plan.Static))
	for _, it := range plan.Static {
		out = append(out, routeJSON{Kind: it.Kind.String(), Approach: it.Approach, Root: it.Root.String(), Unsafe: unsafeRoot(it.Root)})
	}
	return out
}

// unsafeRouteLabels names the plan's unsafe routes ("context → project-root"),
// empty when every surface lands in the session home.
func unsafeRouteLabels(plan delivery.Plan) []string {
	var out []string
	for _, it := range plan.Static {
		if unsafeRoot(it.Root) {
			out = append(out, fmt.Sprintf("%s → %s", it.Kind, it.Root))
		}
	}
	return out
}

// printDeliveryRoutes renders the plan's routes as the dry-run's text form.
func printDeliveryRoutes(w io.Writer, routes []routeJSON) {
	fmt.Fprintln(w, "\n=== Delivery ===")
	if len(routes) == 0 {
		fmt.Fprintln(w, "(nothing to deliver)")
		return
	}
	for _, r := range routes {
		if r.Unsafe {
			fmt.Fprintf(w, "  %s → %s via %s  (unsafe: the binding's roots: selection writes the shared tree)\n", r.Kind, r.Root, r.Approach)
			continue
		}
		fmt.Fprintf(w, "  %s → %s via %s\n", r.Kind, r.Root, r.Approach)
	}
}
