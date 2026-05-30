package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

var (
	searchTags         []string
	searchItemFilter   string
	searchLocalOnly    bool
	searchRemoteOnly   bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search content across local and remote sources",
	Long: `Search for content by name, tags, or description.

By default, searches both local content (fragments, prompts, profiles) and
remote repositories (bundles, profiles).

Examples:
  ctxloom search cache                    # Search all sources
  ctxloom search -t golang                # Search by tag
  ctxloom search --local cache            # Search only local content
  ctxloom search --remote golang          # Search only remote repositories
  ctxloom search --type fragment cache    # Search only fragments
  ctxloom search --type bundle golang     # Search only remote bundles`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		return runUnifiedSearch(cmd.Context(), query, searchTags, searchItemFilter, searchLocalOnly, searchRemoteOnly)
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)

	searchCmd.Flags().StringSliceVarP(&searchTags, "tag", "t", nil, "Filter by tags (comma-separated)")
	searchCmd.Flags().StringVar(&searchItemFilter, "type", "", "Filter by type (fragment, prompt, profile, bundle, mcp_server)")
	searchCmd.Flags().BoolVar(&searchLocalOnly, "local", false, "Search only local content")
	searchCmd.Flags().BoolVar(&searchRemoteOnly, "remote", false, "Search only remote repositories")
}

// runUnifiedSearch searches both local and remote sources.
func runUnifiedSearch(ctx context.Context, query string, tags []string, itemType string, localOnly, remoteOnly bool) error {
	if query == "" && len(tags) == 0 {
		return fmt.Errorf("please provide a search query or tags")
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Determine scope - if neither flag set, search both
	searchLocalScope := !remoteOnly || localOnly
	searchRemoteScope := !localOnly || remoteOnly
	if !localOnly && !remoteOnly {
		searchLocalScope = true
		searchRemoteScope = true
	}

	// Determine types to search
	var localTypes, remoteTypes []string
	if itemType != "" {
		switch itemType {
		case "fragment", "prompt", "mcp_server":
			localTypes = []string{itemType}
		case "profile":
			// Profile exists in both local and remote
			localTypes = []string{itemType}
			remoteTypes = []string{itemType}
		case "bundle":
			// Bundle is remote-only
			remoteTypes = []string{itemType}
		default:
			return fmt.Errorf("unknown type: %s (valid: fragment, prompt, profile, bundle, mcp_server)", itemType)
		}
	} else {
		localTypes = []string{"fragment", "prompt", "profile", "mcp_server"}
		remoteTypes = []string{"bundle", "profile"}
	}

	var localResults []operations.SearchResult
	var remoteResults []operations.SearchRemoteEntry
	var localErr, remoteErr error

	var wg sync.WaitGroup

	// Search local content
	if searchLocalScope && len(localTypes) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := operations.SearchContent(ctx, cfg, operations.SearchContentRequest{
				Query:        query,
				Types:        localTypes,
				Tags:         tags,
				SearchLocal:  true,
				SearchRemote: false,
				Limit:        100,
			})
			if err != nil {
				localErr = err
				return
			}
			localResults = result.Results
		}()
	}

	// Search remote content. Route through operations.SearchRemotes (the same
	// tag-aware path the search_remotes MCP tool uses). The op takes a single
	// item_type: pass it only when the caller narrowed to exactly one, else
	// empty to search both bundles and profiles.
	if searchRemoteScope && len(remoteTypes) > 0 {
		remoteItemType := ""
		if len(remoteTypes) == 1 {
			remoteItemType = remoteTypes[0]
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := operations.SearchRemotes(ctx, cfg, operations.SearchRemotesRequest{
				Query:    query,
				ItemType: remoteItemType,
			})
			if err != nil {
				remoteErr = err
				return
			}
			remoteResults = result.Results
		}()
	}

	wg.Wait()

	// Report errors as warnings
	if localErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: local search error: %v\n", localErr)
	}
	if remoteErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: remote search error: %v\n", remoteErr)
	}

	totalCount := len(localResults) + len(remoteResults)
	if totalCount == 0 {
		fmt.Println("No results found.")
		return nil
	}

	fmt.Printf("Results (%d):\n\n", totalCount)

	// Print local results
	if len(localResults) > 0 {
		printLocalResults(localResults)
	}

	// Print remote results
	if len(remoteResults) > 0 {
		if len(localResults) > 0 {
			fmt.Println()
		}
		printRemoteResults(remoteResults)
	}

	return nil
}

// printLocalResults prints local search results grouped by type.
func printLocalResults(results []operations.SearchResult) {
	// Group by type
	byType := make(map[string][]operations.SearchResult)
	for _, r := range results {
		byType[r.Type] = append(byType[r.Type], r)
	}

	typeOrder := []string{"fragment", "prompt", "profile", "mcp_server"}
	typeNames := map[string]string{
		"fragment":   "Fragments",
		"prompt":     "Prompts",
		"profile":    "Profiles",
		"mcp_server": "MCP Servers",
	}

	for _, t := range typeOrder {
		items := byType[t]
		if len(items) == 0 {
			continue
		}

		fmt.Printf("%s:\n", typeNames[t])
		for _, item := range items {
			fmt.Printf("  - %s", item.Name)
			if len(item.Tags) > 0 {
				fmt.Printf(" [%s]", strings.Join(item.Tags, ", "))
			}
			if item.Source != "" {
				fmt.Printf(" (%s)", item.Source)
			}
			fmt.Println()
		}
	}
}

// printRemoteResults prints remote search results in table format.
func printRemoteResults(results []operations.SearchRemoteEntry) {
	fmt.Println("Remote:")
	fmt.Printf("  %-8s │ %-12s │ %-20s │ %s\n", "Type", "Remote", "Name", "Tags")
	fmt.Printf("  ─────────┼──────────────┼──────────────────────┼────────────\n")

	for _, r := range results {
		tags := strings.Join(r.Tags, ", ")
		if len(tags) > 20 {
			tags = tags[:17] + "..."
		}

		name := r.Name
		if len(name) > 18 {
			name = name[:15] + "..."
		}

		itemType := r.Type

		fmt.Printf("  %-8s │ %-12s │ %-20s │ %s\n", itemType, r.Remote, name, tags)
	}

	fmt.Println()
	fmt.Println("Install with: ctxloom pull <remote>/<name>")
}

