package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// reconcileInstalled runs `deps pull`'s reconcile and returns its plan for the
// caller to render, warning about anything it could not apply. Failures are
// reported and never fatal — reconciliation is a SECOND guarantee layered on a
// pull that has already succeeded — and a lockfile that could not be read
// returns nil: nothing was decided, so there is no plan.
func reconcileInstalled(ctx context.Context, cfg *config.Config) *operations.ReconcilePlan {
	res, err := reconcileInstalledOp(ctx, cfg)
	if err != nil {
		clidiag.Warn("ctxloom", "reconcile: read the lockfile: %v", err)
		return nil
	}
	for _, w := range res.Warnings {
		clidiag.Warn("ctxloom", "reconcile: %s", inertField(w))
	}
	return &res.Plan
}

// renderReconcile says what the reconcile did and what it could not do.
//
// Both halves are load-bearing and neither substitutes for the other. A
// removal nobody can name is a removal nobody can undo: the user finds out by
// missing the content later, with no record of which pull took it. And
// "could not reach this remote" is the sentence that stops them concluding
// their installation was verified against upstream when part of it was never
// checked at all.
func renderReconcile(w io.Writer, plan operations.ReconcilePlan) {
	if len(plan.Gone) > 0 {
		fmt.Fprintf(w, "\nRemoved %d dependency(ies) no longer published by their remote:\n", len(plan.Gone))
		for _, ref := range plan.Gone {
			fmt.Fprintf(w, "  - %s\n", inertField(string(ref)))
		}
		fmt.Fprintln(w, "  Re-adding them upstream and pulling again restores them; nothing authored here was touched.")
	}

	for _, u := range plan.Unreachable {
		where := inertField(u.URL)
		if where == "" {
			where = "an unidentifiable repository"
		}
		fmt.Fprintf(w, "\n%s could not be reached, so its dependencies were left exactly as they are (%s).\n", where, inertField(u.Reason))
		for _, ref := range u.Refs {
			fmt.Fprintf(w, "  - kept: %s\n", inertField(string(ref)))
		}
		fmt.Fprintln(w, "  Nothing is removed on the strength of a remote that could not be read.")
	}
}
