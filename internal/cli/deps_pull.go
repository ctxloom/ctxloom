package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

var (
	depsPullForce bool
	depsPullLock  bool
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

Pull installs exactly what is PINNED. It never advances an existing pin — an
item whose upstream has moved on is kept at its locked commit and reported as
such. 'ctxloom deps upgrade' is what advances a pin; 'ctxloom deps check' is
what tells you one could be advanced.

A held entry ('ctxloom deps hold') stays at its commit even under --force,
which otherwise re-resolves every reference.

Pulling does not expose content to your assistant. Content from an untrusted
source is withheld per item until you accept it with 'ctxloom review'.

Examples:
  ctxloom deps pull                      # Install the closure and reconcile it
  ctxloom deps pull --force              # Re-resolve every reference
  ctxloom deps pull --lock=false         # Leave the lockfile alone`,
	RunE: runDepsPull,
}

func runDepsPull(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Pulling dependencies...")

	result, err := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
		Force:      depsPullForce,
		Lock:       depsPullLock,
		ApplyHooks: true,
	})
	if err != nil {
		return err
	}

	renderPullSummary(cmd.OutOrStdout(), result)

	// Reconcile AFTER the install half, and only when the lockfile is this
	// command's to move. --lock=false says "do not touch the lockfile", and a
	// reconcile prunes entries from it, so honoring the install half of that
	// flag while ignoring the removal half would be the flag not meaning what
	// it says.
	if depsPullLock {
		reconcileInstalled(cmd.Context(), cfg, cmd.OutOrStdout())
	}

	return pullResultErr(result)
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
// The failures are printed to stdout by renderPullSummary, so a caller
// scripting on the EXIT CODE rather than scraping stdout has to be able to see
// them here.
func pullResultErr(result *operations.SyncDependenciesResult) error {
	if result.Errors == 0 {
		return nil
	}
	var refs []string
	for _, item := range result.Failed {
		refs = append(refs, item.Reference)
	}
	return fmt.Errorf("deps pull: %d failed (%s)", result.Errors, strings.Join(refs, ", "))
}

// renderPullSummary prints a completed pull.
//
// The skipped line deliberately does NOT say "already installed".
// Pull installs exactly the PINNED set; moving an existing pin is `deps
// upgrade`'s job. So an item whose upstream content has changed is skipped, and
// "already installed" reads as "you are current" when you are not: upstream
// moved and you are still served the old content. A human (or an agent)
// reasonably concludes there is nothing to do. The line says what is actually
// true — the pin was honored — and names the command that moves it.
func renderPullSummary(w io.Writer, result *operations.SyncDependenciesResult) {
	if result.Total == 0 {
		fmt.Fprintln(w, "No remote dependencies to pull.")
		return
	}

	fmt.Fprintf(w, "\nPulled %d items:\n", result.Total)
	if result.Installed > 0 {
		fmt.Fprintf(w, "  Installed: %d\n", result.Installed)
	}
	if result.Updated > 0 {
		fmt.Fprintf(w, "  Updated: %d\n", result.Updated)
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(w, "  Skipped (kept at their locked commit): %d\n", len(result.Skipped))
		fmt.Fprintln(w, "    Pull never moves an existing pin, so these may have upstream changes.")
		fmt.Fprintln(w, "    Run 'ctxloom deps upgrade' to advance them.")
	}
	if len(result.Retracted) > 0 {
		fmt.Fprintf(w, "  Retracted: %d\n", len(result.Retracted))
		for _, item := range result.Retracted {
			fmt.Fprintf(w, "    - %s: retracted (%s)\n", item.Reference, item.Error)
		}
	}
	if result.Errors > 0 {
		fmt.Fprintf(w, "  Failed: %d\n", result.Errors)
		for _, item := range result.Failed {
			fmt.Fprintf(w, "    - %s: %s\n", item.Reference, item.Error)
		}
	}
}

func init() {
	depsCmd.AddCommand(depsPullCmd)

	depsPullCmd.Flags().BoolVarP(&depsPullForce, "force", "f", false,
		"Re-resolve every reference instead of honoring what is already installed")
	depsPullCmd.Flags().BoolVar(&depsPullLock, "lock", true,
		"Update lockfile after pull")
}
