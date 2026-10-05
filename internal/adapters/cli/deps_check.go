package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
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
//
// Progress and per-entry warnings go to stderr in every format; the result
// goes through emit().
func runDepsCheck(cmd *cobra.Command, args []string) error {
	_ = loadConfigOrFallback(GetConfig, cmd.ErrOrStderr())
	var ref string
	if len(args) > 0 {
		ref = args[0]
	}
	res, err := checkDependencies(cmd.Context(), App(), operations.CheckDependenciesRequest{Ref: ref})
	if ref != "" {
		// A single reference's clone is refreshed before anything else is
		// attempted, so its failure is reported first — ahead of the status
		// line, and ahead of an error that stopped the check after it.
		warnRefreshFailures(res.Refresh)
	}
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if res.Single != nil {
		status := *res.Single
		return emit(cmd, newCheckRefView(status, res.Refresh), func() error {
			renderDependencyStatus(out, status)
			return nil
		})
	}
	if res.Entries > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "Checking %d items for updates...\n", res.Entries)
		warnRefreshFailures(res.Refresh)
		for _, u := range res.Unchecked {
			warnUncheckedDependency(u)
		}
	}
	return emit(cmd, newCheckView(res), func() error {
		renderDependencyCheck(out, res)
		return nil
	})
}

// checkView is `deps check`'s structured result over the whole closure. SHAs
// are full; every list is always present.
type checkView struct {
	Entries                int                  `json:"entries"`
	Updates                []checkUpdateView    `json:"updates"`
	Unchecked              []checkUncheckedView `json:"unchecked"`
	RefreshFailures        []refreshFailureView `json:"refresh_failures"`
	SkippedEmpty           int                  `json:"skipped_empty"`
	MissingDefaultProfiles []string             `json:"missing_default_profiles"`
	// MissingDefaultsError says the default profiles could not be checked at
	// all, which "none missing" must not be mistaken for.
	MissingDefaultsError string `json:"missing_defaults_error"`
}

type checkUpdateView struct {
	Ref        string              `json:"ref"`
	Type       remote.ItemType     `json:"type"`
	CurrentSHA string              `json:"current_sha"`
	LatestSHA  string              `json:"latest_sha"`
	Selector   remote.SelectorKind `json:"selector"`
	Requested  string              `json:"requested"`
	Resolved   string              `json:"resolved"`
}

type checkUncheckedView struct {
	Ref        string `json:"ref"`
	URL        string `json:"url"`
	Constraint string `json:"constraint"`
	Reason     string `json:"reason"`
	Error      string `json:"error"`
}

type refreshFailureView struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// checkRefView is a single-reference check's structured result.
type checkRefView struct {
	Ref             string               `json:"ref"`
	InLockfile      bool                 `json:"in_lockfile"`
	CurrentSHA      string               `json:"current_sha"`
	LatestSHA       string               `json:"latest_sha"`
	UpToDate        bool                 `json:"up_to_date"`
	RefreshFailures []refreshFailureView `json:"refresh_failures"`
}

func newCheckView(res operations.CheckDependenciesResult) checkView {
	view := checkView{
		Entries:                res.Entries,
		RefreshFailures:        newRefreshFailureViews(res.Refresh),
		SkippedEmpty:           res.SkippedEmpty,
		MissingDefaultProfiles: res.MissingDefaults,
		MissingDefaultsError:   errorText(res.MissingDefaultsErr),
	}
	for _, u := range res.Updates {
		view.Updates = append(view.Updates, checkUpdateView{
			Ref: u.Ref, Type: u.Type, CurrentSHA: u.CurrentSHA, LatestSHA: u.LatestSHA,
			Selector: u.Kind, Requested: u.RequestedVersion, Resolved: u.Version,
		})
	}
	for _, u := range res.Unchecked {
		view.Unchecked = append(view.Unchecked, checkUncheckedView{
			Ref: u.Ref, URL: u.URL, Constraint: u.Constraint, Reason: uncheckedReasonName(u.Reason), Error: errorText(u.Err),
		})
	}
	return view
}

func newCheckRefView(status operations.DependencyStatus, refresh []operations.RefreshFailure) checkRefView {
	return checkRefView{
		Ref:             status.Ref,
		InLockfile:      status.CurrentSHA != "",
		CurrentSHA:      status.CurrentSHA,
		LatestSHA:       status.LatestSHA,
		UpToDate:        status.UpToDate(),
		RefreshFailures: newRefreshFailureViews(refresh),
	}
}

func newRefreshFailureViews(failures []operations.RefreshFailure) []refreshFailureView {
	var views []refreshFailureView
	for _, f := range failures {
		views = append(views, refreshFailureView{URL: f.URL, Error: errorText(f.Err)})
	}
	return views
}

// uncheckedReasonName is the payload's spelling of each
// operations.UncheckedReason.
func uncheckedReasonName(reason operations.UncheckedReason) string {
	switch reason {
	case operations.UncheckedUnparseable:
		return "unparseable"
	case operations.UncheckedNoRepositoryURL:
		return "no_repository_url"
	case operations.UncheckedUnreachable:
		return "unreachable"
	case operations.UncheckedUnresolvable:
		return "unresolvable"
	case operations.UncheckedNotRefreshed:
		return "not_refreshed"
	}
	panic(fmt.Sprintf("uncheckedReasonName: unhandled operations.UncheckedReason %d", reason))
}

// errorText is err's message, or "" for no error.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
// service established it: each unchecked entry in lockfile order (the refresh
// failures and the warning-worthy reasons already went to stderr), then the
// verdict — which is a claim about entries that
// were actually CHECKED. When some entries' checks failed, saying "up to
// date" unconditionally reads as "everything was verified current" when in
// fact part of the closure was never resolved at all.
func renderDependencyCheck(out io.Writer, res operations.CheckDependenciesResult) {
	if res.Entries == 0 {
		fmt.Fprintln(out, "Nothing is installed, so there is nothing to check.")
		fmt.Fprintln(out, "Install this project's closure with: ctxloom deps pull")
		return
	}

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

// warnUncheckedDependency warns, on stderr, about an entry whose check was
// an attempt that failed; renderUncheckedDependency covers the reasons that
// are facts about the entry rather than failures.
func warnUncheckedDependency(u operations.UncheckedDependency) {
	switch u.Reason {
	case operations.UncheckedUnparseable:
		clidiag.Warn("ctxloom", "%s: could not parse reference (%v); skipping the update check for it", u.Ref, u.Err)
	case operations.UncheckedUnreachable:
		clidiag.Warn("ctxloom", "%s: could not reach %s (%v); skipping the update check for it", u.Ref, u.URL, u.Err)
	case operations.UncheckedUnresolvable:
		clidiag.Warn("ctxloom", "%s: could not resolve %q (%v); skipping the update check for it", u.Ref, u.Constraint, u.Err)
	case operations.UncheckedNoRepositoryURL, operations.UncheckedNotRefreshed:
	}
}

// renderUncheckedDependency is the report line for an entry that could not be
// checked for a reason that is a fact about it, not a failed attempt.
func renderUncheckedDependency(out io.Writer, u operations.UncheckedDependency) {
	switch u.Reason {
	case operations.UncheckedNoRepositoryURL:
		fmt.Fprintf(out, "  %s: reference has no repository URL\n", u.Ref)
	case operations.UncheckedNotRefreshed:
		fmt.Fprintf(out, "  %s: not checked — %s could not be fetched (see warning above)\n", u.Ref, u.URL)
	case operations.UncheckedUnparseable, operations.UncheckedUnreachable, operations.UncheckedUnresolvable:
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
