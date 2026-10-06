package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

var (
	depsPullForce          bool
	depsPullLock           bool
	depsPullAllowDowngrade []string
)

var depsPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Make this project's installed closure match upstream",
	Long: `Reconcile the installation with the remotes it came from: fetch every remote
bundle this project's profiles depend on, record each one's resolved commit in
the lockfile, apply hooks, and remove anything its remote has stopped
publishing.

BOTH DIRECTIONS, AND NEITHER ASKS. Installed remote content is a projection of
remote state — every byte re-fetchable from the address it came from, none of it
authored here — so removing what upstream withdrew is synchronization in exactly
the sense installing what upstream added is. Each removal is named in the output
after the fact.

A remote that could NOT BE READ is never treated as having deleted anything.
An unreachable host and a revoked credential both look like "nothing came back",
so absence counts as authority only from a repository this run separately proved
it could read; anything else is reported as unchecked and left exactly as it is.

Pull installs exactly what is PINNED. It creates first pins and never moves an
existing one — not when upstream has moved on, not when you changed the
constraint in a profile, and not under --force, which only reinstalls each
reference at its pin. A changed constraint is reported and left for
'ctxloom deps upgrade', which is the one command that moves a pin; 'ctxloom deps
check' is what tells you one could be moved.

Pulling does not expose content to your assistant. Content from an untrusted
source is withheld per item until you accept it with 'ctxloom review'.`,
	Example: `  ctxloom deps pull                      # Install the closure and reconcile it
  ctxloom deps pull --force              # Reinstall every reference at its pin
  ctxloom deps pull --lock=false         # Leave the lockfile alone`,
	RunE: runDepsPull,
}

func runDepsPull(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "Pulling dependencies...")

	result, err := syncDependencies(cmd.Context(), App(), operations.SyncDependenciesRequest{
		Force:          depsPullForce,
		Lock:           depsPullLock,
		ApplyHooks:     true,
		AllowDowngrade: depsPullAllowDowngrade,
	})
	if err != nil {
		return err
	}

	// Reconcile AFTER the install half, and only when the lockfile is this
	// command's to move. --lock=false says "do not touch the lockfile", and a
	// reconcile prunes entries from it, so honoring the install half of that
	// flag while ignoring the removal half would be the flag not meaning what
	// it says.
	var plan *operations.ReconcilePlan
	if depsPullLock {
		plan = reconcileInstalled(cmd.Context(), cfg)
	}

	// The payload goes out BEFORE the exit decision, so a caller whose pull
	// partly failed still gets what did happen.
	if err := emit(cmd, newPullView(result, plan), func() error {
		renderPullSummary(cmd.OutOrStdout(), result)
		if plan != nil {
			renderReconcile(cmd.OutOrStdout(), *plan)
		}
		return nil
	}); err != nil {
		return err
	}
	return pullResultErr(result)
}

// pullView is `deps pull`'s structured result: the sync's outcome, each failed
// item with the fix it names, and — when a reconcile ran — what it removed and
// what it could not check. Every list is always present.
type pullView struct {
	Status      string                `json:"status"`
	Total       int                   `json:"total"`
	Installed   int                   `json:"installed"`
	Reinstalled int                   `json:"reinstalled"`
	Errors      int                   `json:"errors"`
	Synced      []operations.SyncItem `json:"synced"`
	Skipped     []operations.SyncItem `json:"skipped"`
	Retracted   []operations.SyncItem `json:"retracted"`
	Failed      []pullFailureView     `json:"failed"`
	Removed     []string              `json:"removed"`
	Incomplete  bool                  `json:"incomplete"`
	Unreachable []string              `json:"unreachable"`
	// ConstraintChanges are pins whose manifest constraint changed; pull kept
	// them where they are (only `deps upgrade` moves a pin).
	ConstraintChanges []operations.ConstraintChange `json:"constraint_changes"`
	// Changes discloses each pin this pull created.
	Changes []operations.PinChange `json:"changes"`
	Message string                 `json:"message"`
	// Reconcile is nil when no reconcile ran: --lock=false, or a lockfile it
	// could not read. An empty plan would claim a check that never happened.
	Reconcile *reconcileView `json:"reconcile,omitempty"`
}

// pullFailureView is one failed item and the fix its failure names
// (operations.SyncItem.Remedy), which the item itself does not serialize.
type pullFailureView struct {
	Reference string `json:"reference"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Fix       string `json:"fix"`
}

// reconcileView is operations.ReconcilePlan with snake_case keys.
type reconcileView struct {
	Gone        []trust.BundleKey          `json:"gone"`
	Unreachable []reconcileUnreachableView `json:"unreachable"`
}

type reconcileUnreachableView struct {
	URL    string            `json:"url"`
	Reason string            `json:"reason"`
	Refs   []trust.BundleKey `json:"refs"`
}

func newPullView(result *operations.SyncDependenciesResult, plan *operations.ReconcilePlan) pullView {
	view := pullView{
		Status:      result.Status,
		Total:       result.Total,
		Installed:   result.Installed,
		Reinstalled: result.Reinstalled,
		Errors:      result.Errors,
		Synced:      result.Synced,
		Skipped:     result.Skipped,
		Retracted:   result.Retracted,
		Removed:     result.Removed,
		Incomplete:  result.Incomplete,
		Unreachable: result.Unreachable,
		Message:     result.Message,

		ConstraintChanges: result.ConstraintChanges,
		Changes:           result.Changes,
	}
	for _, item := range result.Failed {
		view.Failed = append(view.Failed, pullFailureView{
			Reference: item.Reference, Type: item.Type, Status: item.Status, Error: item.Error, Fix: item.Remedy(),
		})
	}
	if plan != nil {
		view.Reconcile = &reconcileView{Gone: plan.Gone}
		for _, u := range plan.Unreachable {
			view.Reconcile.Unreachable = append(view.Reconcile.Unreachable,
				reconcileUnreachableView{URL: u.URL, Reason: u.Reason, Refs: u.Refs})
		}
	}
	return view
}

// pullResultErr decides the exit code from what the pull actually did.
//
// Skipped items are NOT a failure (pull never moves an existing pin, by design
// — see renderPullSummary's doc comment). Retracted is NOT a failure either: it
// is the retraction mechanism working as designed — a bad dependency detected
// and withheld automatically while the rest of the sync proceeds (see
// j001500/j001700/trust_surface's acceptance journeys, whose entire narrative
// is "the sync still succeeds; the retracted content just never reaches the
// user"). Only Errors — a real fetch or apply failure — makes the pull fail.
//
// The failures are in the payload (and in renderPullSummary's text), so a
// caller scripting on the EXIT CODE rather than reading the output has to be
// able to see them here.
//
// The error names a fix only when every failed item shares one (the usual
// single-failure case): one remedy field cannot honestly stand for several,
// and the per-item fix lines renderPullSummary prints carry the rest.
func pullResultErr(result *operations.SyncDependenciesResult) error {
	if result.Errors == 0 {
		return nil
	}
	var refs []string
	fix := ""
	for i, item := range result.Failed {
		refs = append(refs, item.Reference)
		switch r := item.Remedy(); {
		case i == 0:
			fix = r
		case r != fix:
			fix = ""
		}
	}
	return report.Error{Msg: fmt.Sprintf("deps pull: %d failed (%s)", result.Errors, strings.Join(refs, ", ")), Fix: fix}
}

// renderPullSummary prints a completed pull.
//
// The skipped line deliberately does NOT say "already installed", and it
// deliberately does NOT say "run deps upgrade". Pull installs exactly the
// PINNED set and never reaches upstream, so it can observe neither that
// upstream has moved nor that it has not: "already installed" claims currency
// pull cannot establish, and "run upgrade" claims drift it cannot establish —
// a pin the user advanced one command ago gets told to advance again. The line
// says the one thing pull does know — the pin was honored — and names the
// command that can actually answer the question (`deps check`).
func renderPullSummary(w io.Writer, result *operations.SyncDependenciesResult) {
	if result.Total == 0 {
		fmt.Fprintln(w, "No remote dependencies to pull.")
		return
	}

	fmt.Fprintf(w, pullSummaryHeaderFormat, result.Total)
	if result.Installed > 0 {
		fmt.Fprintf(w, "  Installed: %d\n", result.Installed)
	}
	if result.Reinstalled > 0 {
		fmt.Fprintf(w, "  Reinstalled at their pin: %d\n", result.Reinstalled)
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(w, "  Skipped (kept at their locked commit): %d\n", len(result.Skipped))
		fmt.Fprintln(w, "    Pull never moves an existing pin and does not ask upstream whether one could move.")
		fmt.Fprintln(w, "    Run 'ctxloom deps check' to find out.")
	}
	if len(result.Retracted) > 0 {
		fmt.Fprintf(w, "  Retracted: %d\n", len(result.Retracted))
		for _, item := range result.Retracted {
			fmt.Fprintf(w, "    - %s: retracted (%s)\n", inertField(item.Reference), inertBody(item.Error, 0, false).Text)
		}
	}
	for _, identity := range result.Removed {
		fmt.Fprintf(w, "  Removed %s from the lockfile: nothing this project composes depends on it any more.\n", inertField(identity))
	}
	if result.Errors > 0 {
		fmt.Fprintf(w, "  Failed: %d\n", result.Errors)
		for _, item := range result.Failed {
			fmt.Fprintf(w, "    - %s: %s%s\n", inertField(item.Reference), inertBody(item.Error, 0, false).Text,
				clifmt.FixLine("      ", inertBody(item.Remedy(), 0, false).Text))
		}
	}
	renderIncompleteLock(w, result)
	operations.WriteConstraintChanges(w, result.ConstraintChanges)
	operations.WriteNewPins(w, result.Changes)
}

// renderIncompleteLock names the items the post-pull lock rebuild could not
// reach, when there were any.
func renderIncompleteLock(w io.Writer, result *operations.SyncDependenciesResult) {
	if !result.Incomplete {
		return
	}
	names := make([]string, 0, len(result.Unreachable))
	for _, ref := range result.Unreachable {
		names = append(names, inertField(ref))
	}
	fmt.Fprintf(w, pullIncompleteFormat, strings.Join(names, ", "))
}

// pullSummaryHeaderFormat opens a pull's summary with the size of the
// dependency set it reconciled. It does not say "pulled": most pulls install
// nothing and keep every pin, and the lines below say which outcome each
// dependency had.
const pullSummaryHeaderFormat = "\nDependencies (%d):\n"

// pullIncompleteFormat is the summary line for a pull whose lock rebuild could
// not reach part of the closure; it takes the unreachable items, joined.
const pullIncompleteFormat = "  Lock incomplete: could not reach %s; their previous lock entries were kept.\n"

func init() {
	depsCmd.AddCommand(depsPullCmd)

	depsPullCmd.Flags().BoolVarP(&depsPullForce, "force", "f", false,
		"Reinstall every reference at its pin instead of skipping what is already installed")
	depsPullCmd.Flags().BoolVar(&depsPullLock, "lock", true,
		"Update lockfile after pull")
	depsPullCmd.Flags().StringArrayVar(&depsPullAllowDowngrade, "allow-downgrade", nil,
		"Accept a lower signed version (or unsigned content) for this ref, and record it as the new floor; repeat per ref")
}
