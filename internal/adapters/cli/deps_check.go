package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

var depsCheckCmd = &cobra.Command{
	Use:   "check [reference]",
	Short: "Report which dependencies have a newer commit available",
	Long: `Check the installed closure against its remotes and report what is out of date.

Without arguments, checks every lockfile entry. With a reference, checks only
that one. The reference is a canonical bundle reference — a full repository URL
plus its bundle path, e.g. https://github.com/alice/ctxloom@bundles/security —
not a remote name (see "ctxloom remote create --help" for the repository URL
formats a remote itself may take).

CHECK READS; UPGRADE WRITES. This reports and changes nothing, so it is safe to
run anywhere and on anything. 'ctxloom deps upgrade' is the verb that advances
the pins it names.

The check is constraint-aware: an entry is out of date only when a newer commit
actually satisfies what its manifest asked for. An entry pinned to an exact tag
or SHA is never out of date, and is not fetched for.

An entry that could NOT be checked — an unreachable remote, an unparseable
reference — is reported as unchecked rather than folded into "up to date".

Examples:
  ctxloom deps check
  ctxloom deps check https://github.com/alice/ctxloom@bundles/security`,
	RunE: runDepsCheck,
}

// runDepsCheck is the frontend over operations.CheckDependencies. The config
// is read here first so a failed load warns the way every fault-tolerant
// startup path warns (loadConfigOrFallback); the service re-reads the same
// memoized generation and proceeds over the same minimal default.
func runDepsCheck(cmd *cobra.Command, args []string) error {
	_ = loadConfigOrFallback(GetConfig, os.Stderr)
	var ref string
	if len(args) > 0 {
		ref = args[0]
	}
	res, err := operations.CheckDependencies(cmd.Context(), App(), operations.CheckDependenciesRequest{Ref: ref})
	if ref != "" {
		// A single reference's clone is refreshed before anything else is
		// attempted, so its failure is reported first — ahead of the status
		// line, and ahead of an error that stopped the check after it.
		warnRefreshFailures(res.Refresh)
	}
	if err != nil {
		return err
	}
	if res.Single != nil {
		renderDependencyStatus(os.Stdout, *res.Single)
		return nil
	}
	renderDependencyCheck(os.Stdout, res)
	return nil
}

// warnRefreshFailures reports each clone the check could not refresh; its
// entries are reported unchecked, not read from the stale clone.
func warnRefreshFailures(failures []operations.RefreshFailure) {
	for _, f := range failures {
		clidiag.Warn("ctxloom", "fetch %s: %v", f.URL, f.Err)
	}
}

// renderDependencyStatus prints a single-reference check: the status line
// and, when the reference is not current, the verb that advances it.
func renderDependencyStatus(out io.Writer, status operations.DependencyStatus) {
	switch status.CurrentSHA {
	case "":
		fmt.Fprintf(out, "%s not found in lockfile, checking latest version...\n", status.Ref)
	case status.LatestSHA:
		fmt.Fprintf(out, "%s is up to date (SHA: %s)\n", status.Ref, gitutil.ShortSHA(status.LatestSHA))
	default:
		fmt.Fprintf(out, "%s has update available:\n", status.Ref)
		fmt.Fprintf(out, "  Current: %s\n", gitutil.ShortSHA(status.CurrentSHA))
		fmt.Fprintf(out, "  Latest:  %s\n", gitutil.ShortSHA(status.LatestSHA))
	}
	if !status.UpToDate() {
		fmt.Fprintln(out, "\nRun 'ctxloom deps upgrade' to advance it.")
	}
}

// renderDependencyCheck prints the whole-closure check in the order the
// service established it: the refresh failures, then each unchecked entry
// in lockfile order, then the verdict — which is a claim about entries that
// were actually CHECKED. When some entries' checks failed, saying "up to
// date" unconditionally reads as "everything was verified current" when in
// fact part of the closure was never resolved at all.
func renderDependencyCheck(out io.Writer, res operations.CheckDependenciesResult) {
	if res.Entries == 0 {
		fmt.Fprintln(out, "Nothing is installed, so there is nothing to check.")
		fmt.Fprintln(out, "Install this project's closure with: ctxloom deps pull")
		return
	}

	fmt.Fprintf(out, "Checking %d items for updates...\n\n", res.Entries)

	warnRefreshFailures(res.Refresh)
	for _, u := range res.Unchecked {
		renderUncheckedDependency(out, u)
	}

	if res.SkippedEmpty > 0 {
		fmt.Fprintf(out, "Skipped %d entries with empty SHA (run 'ctxloom deps pull' to clean up)\n\n", res.SkippedEmpty)
	}

	if len(res.Updates) == 0 {
		if len(res.Unchecked) > 0 {
			fmt.Fprintf(out, "No updates found among the entries that could be checked — %d entry(ies) could not be checked (see warnings above).\n", len(res.Unchecked))
		} else {
			fmt.Fprintln(out, "All items are up to date!")
		}
		return
	}

	fmt.Fprintf(out, "Found %d items with updates available:\n\n", len(res.Updates))

	printAvailableUpdates(out, res.Updates)
	reportMissingDefaults(out, res.MissingDefaults, res.MissingDefaultsErr)

	fmt.Fprintln(out, "\nRun 'ctxloom deps upgrade' to advance these pins.")
}

// renderUncheckedDependency says why one entry could not be checked. A
// reference with no repository URL is a line in the report; every other
// reason is a warning, since it is an attempt that failed rather than a
// fact about the entry.
func renderUncheckedDependency(out io.Writer, u operations.UncheckedDependency) {
	switch u.Reason {
	case operations.UncheckedUnparseable:
		clidiag.Warn("ctxloom", "%s: could not parse reference (%v); skipping the update check for it", u.Ref, u.Err)
	case operations.UncheckedNoRepositoryURL:
		fmt.Fprintf(out, "  %s: reference has no repository URL\n", u.Ref)
	case operations.UncheckedUnreachable:
		clidiag.Warn("ctxloom", "%s: could not reach %s (%v); skipping the update check for it", u.Ref, u.URL, u.Err)
	case operations.UncheckedUnresolvable:
		clidiag.Warn("ctxloom", "%s: could not resolve %q (%v); skipping the update check for it", u.Ref, u.Constraint, u.Err)
	case operations.UncheckedNotRefreshed:
		fmt.Fprintf(out, "  %s: not checked — %s could not be fetched (see warning above)\n", u.Ref, u.URL)
	}
}

// printAvailableUpdates lists pending bundle updates; the section is omitted when
// empty.
func printAvailableUpdates(out io.Writer, bundleUpdates []operations.DependencyUpdate) {
	if len(bundleUpdates) > 0 {
		fmt.Fprintln(out, "Bundles:")
		for _, u := range bundleUpdates {
			fmt.Fprintf(out, "  %s  (%s)\n", u.Ref, u.SelectorLabel())
			fmt.Fprintf(out, "    Current: %s → Latest: %s\n", gitutil.ShortSHA(u.CurrentSHA), gitutil.ShortSHA(u.LatestSHA))
		}
	}
}

// reportMissingDefaults warns about configured default profiles that don't
// exist. Silent when there are none — but never silent when err says the check
// could not be made: an unperformed check has no clean result to report, and
// printing nothing is indistinguishable from "they all exist".
func reportMissingDefaults(out io.Writer, missing []string, err error) {
	if err != nil {
		fmt.Fprintf(out, "\nWarning: could not check the default profiles: %v\n", err)
		return
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(out, "\n--- Nonexistent default profiles ---\n")
	fmt.Fprintln(out, "The following default profiles do not exist:")
	for _, name := range missing {
		fmt.Fprintf(out, "  - %s\n", name)
	}
	fmt.Fprintln(out, "\nUpdate your ctxloom.yaml to fix the defaults.profiles list.")
}

func init() {
	depsCmd.AddCommand(depsCheckCmd)
}
