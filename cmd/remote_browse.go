package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benjaminabbitt/scm/internal/operations"
)

var (
	browseRecursive bool
	browseType      string
)

var remoteBrowseCmd = &cobra.Command{
	Use:   "browse <remote>",
	Short: "Browse bundles and profiles in a remote",
	Long: `List bundles and profiles available in a remote repository.

By default shows both bundles and profiles. Use --type to filter.

Examples:
  scm remote browse scm-github
  scm remote browse scm-github --type bundle
  scm remote browse scm-github --type profile`,
	Args: cobra.ExactArgs(1),
	RunE: runRemoteBrowse,
}

func runRemoteBrowse(cmd *cobra.Command, args []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	remoteName := args[0]

	// Determine which types to browse
	types := []string{"bundle", "profile"}
	if browseType != "" {
		types = []string{browseType}
	}

	totalCount := 0

	for _, itemType := range types {
		result, err := operations.BrowseRemote(cmd.Context(), cfg, operations.BrowseRemoteRequest{
			Remote:    remoteName,
			ItemType:  itemType,
			Recursive: browseRecursive,
		})
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to browse %ss: %v\n", itemType, err)
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
		fmt.Printf("No bundles or profiles found in %s\n", remoteName)
		return nil
	}

	fmt.Println()
	fmt.Println("Install with: scm install <reference>")

	return nil
}

func init() {
	remoteCmd.AddCommand(remoteBrowseCmd)

	remoteBrowseCmd.Flags().BoolVarP(&browseRecursive, "recursive", "r", true,
		"List items in subdirectories")
	remoteBrowseCmd.Flags().StringVarP(&browseType, "type", "t", "",
		"Filter by type: bundle or profile")
}
