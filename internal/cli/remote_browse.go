package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var browseRecursive bool

var remoteBrowseCmd = &cobra.Command{
	Use:   "browse <remote>",
	Short: "Browse bundles in a remote",
	Long: `List bundles available in a remote repository.

Examples:
  ctxloom remote browse ctxloom-default`,
	Args: cobra.ExactArgs(1),
	RunE: runRemoteBrowse,
}

func runRemoteBrowse(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	remoteName := args[0]
	out := cmd.OutOrStdout()

	// Only bundles are distributed at the top level (top-level profile
	// distribution was retired; profiles ship inside bundles) — so this
	// browses exactly one item type. It used to loop over a slice of
	// item types (a leftover from when profiles were browsable
	// separately); U039-F09 inlined that to the single call it always was.
	const itemType = "bundle"
	result, err := operations.BrowseRemote(cmd.Context(), cfg, operations.BrowseRemoteRequest{
		Remote:    remoteName,
		ItemType:  itemType,
		Recursive: browseRecursive,
	})
	if err != nil {
		// U039-F03: a browse failure (network, auth, an unresolvable remote
		// name) must not be reported as "No bundles found" — that asserts a
		// false fact (the remote may be full of bundles; the browse just
		// never reached it) and would otherwise exit 0.
		clidiag.Fwarn(cmd.ErrOrStderr(), "ctxloom", "failed to browse %ss: %v", itemType, err)
		return fmt.Errorf("browse %s: %w", remoteName, err)
	}

	if result.Count == 0 {
		fmt.Fprintf(out, "No bundles found in %s\n", remoteName)
		return nil
	}

	title := strings.ToUpper(itemType[:1]) + itemType[1:] + "s"
	fmt.Fprintf(out, "%s in %s (%s):\n\n", title, result.Remote, result.URL)

	// Sort entries by path
	items := result.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].Path < items[j].Path
	})

	for _, item := range items {
		fmt.Fprintf(out, "  %s\n", item.PullRef)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Use one: add its ref to a profile (ctxloom profile create/modify), then ctxloom remote pull")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteBrowseCmd)

	remoteBrowseCmd.Flags().BoolVarP(&browseRecursive, "recursive", "r", true,
		"List items in subdirectories")
}
