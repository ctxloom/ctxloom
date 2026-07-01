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

	// Only bundles are distributed at the top level (top-level profile
	// distribution was retired; profiles ship inside bundles).
	types := []string{"bundle"}

	totalCount := 0

	for _, itemType := range types {
		result, err := operations.BrowseRemote(cmd.Context(), cfg, operations.BrowseRemoteRequest{
			Remote:    remoteName,
			ItemType:  itemType,
			Recursive: browseRecursive,
		})
		if err != nil {
			clidiag.Fwarn(cmd.ErrOrStderr(), "ctxloom", "failed to browse %ss: %v", itemType, err)
			continue
		}

		if result.Count == 0 {
			continue
		}

		totalCount += result.Count

		// Display results
		title := strings.ToUpper(itemType[:1]) + itemType[1:] + "s"
		if len(types) > 1 {
			fmt.Printf("\n%s:\n", title)
		} else {
			fmt.Printf("%s in %s (%s):\n\n", title, result.Remote, result.URL)
		}

		// Sort entries by path
		items := result.Items
		sort.Slice(items, func(i, j int) bool {
			return items[i].Path < items[j].Path
		})

		for _, item := range items {
			fmt.Printf("  %s\n", item.PullRef)
		}
	}

	if totalCount == 0 {
		fmt.Printf("No bundles found in %s\n", remoteName)
		return nil
	}

	fmt.Println()
	fmt.Println("Use one: add its ref to a profile (ctxloom profile create/modify), then ctxloom remote pull")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteBrowseCmd)

	remoteBrowseCmd.Flags().BoolVarP(&browseRecursive, "recursive", "r", true,
		"List items in subdirectories")
}
