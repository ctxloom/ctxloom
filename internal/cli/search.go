package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

var (
	searchTags       []string
	searchItemFilter string
	searchLocalOnly  bool
	searchRemoteOnly bool
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

	localScope, remoteScope := searchScopes(localOnly, remoteOnly)
	localTypes, remoteTypes, err := resolveSearchTypes(itemType)
	if err != nil {
		return err
	}

	localResults, remoteResults := runSearches(ctx, cfg, searchParams{
		query:       query,
		tags:        tags,
		localTypes:  localTypes,
		remoteTypes: remoteTypes,
		localScope:  localScope,
		remoteScope: remoteScope,
	})

	if len(localResults)+len(remoteResults) == 0 {
		fmt.Println("No results found.")
		return nil
	}
	printUnifiedResults(localResults, remoteResults)
	return nil
}

// searchScopes resolves which sources to search from the --local/--remote flags.
// Setting neither (or both) searches both sources.
func searchScopes(localOnly, remoteOnly bool) (local, remote bool) {
	return !remoteOnly || localOnly, !localOnly || remoteOnly
}

// resolveSearchTypes maps a --type filter to the local and remote item types to
// search. An empty filter searches every type; profile lives in both scopes.
func resolveSearchTypes(itemType string) (localTypes, remoteTypes []string, err error) {
	if itemType == "" {
		// Profiles are searched locally only; top-level profile distribution was
		// retired, so remote search covers bundles (and the fragments/skills/mcp
		// they ship).
		return []string{"fragment", "skill", "profile", "mcp_server"}, []string{"bundle"}, nil
	}
	switch itemType {
	case "fragment", "skill", "mcp_server":
		return []string{itemType}, nil, nil
	case "profile":
		return []string{itemType}, nil, nil
	case "bundle":
		return nil, []string{itemType}, nil
	default:
		return nil, nil, fmt.Errorf("unknown type: %s (valid: fragment, prompt, profile, bundle, mcp_server)", itemType)
	}
}

// searchParams bundles the inputs for a unified search fan-out.
type searchParams struct {
	query                   string
	tags                    []string
	localTypes, remoteTypes []string
	localScope, remoteScope bool
}

// runSearches fans out the local and remote searches concurrently and returns
// their results. Errors are reported as warnings (search degrades gracefully).
func runSearches(ctx context.Context, cfg *config.Config, p searchParams) ([]operations.SearchResult, []operations.SearchRemoteEntry) {
	var localResults []operations.SearchResult
	var remoteResults []operations.SearchRemoteEntry
	var localErr, remoteErr error

	var wg sync.WaitGroup
	if p.localScope && len(p.localTypes) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localResults, localErr = searchLocalContent(ctx, cfg, p.query, p.tags, p.localTypes)
		}()
	}
	if p.remoteScope && len(p.remoteTypes) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			remoteResults, remoteErr = searchRemoteContent(ctx, cfg, p.query, p.remoteTypes)
		}()
	}
	wg.Wait()

	if localErr != nil {
		clidiag.Warn("ctxloom", "local search error: %v", localErr)
	}
	if remoteErr != nil {
		clidiag.Warn("ctxloom", "remote search error: %v", remoteErr)
	}
	return localResults, remoteResults
}

// searchLocalContent searches local fragments, prompts, profiles, and MCP servers.
func searchLocalContent(ctx context.Context, cfg *config.Config, query string, tags, types []string) ([]operations.SearchResult, error) {
	result, err := operations.SearchContent(ctx, cfg, operations.SearchContentRequest{
		Query:        query,
		Types:        types,
		Tags:         tags,
		SearchLocal:  true,
		SearchRemote: false,
		Limit:        100,
	})
	if err != nil {
		return nil, err
	}
	return result.Results, nil
}

// searchRemoteContent searches remote repositories via the same tag-aware path
// the search_remotes MCP tool uses. SearchRemotes takes a single item_type:
// pass it only when narrowed to exactly one, else empty to search bundles and
// profiles both.
func searchRemoteContent(ctx context.Context, cfg *config.Config, query string, types []string) ([]operations.SearchRemoteEntry, error) {
	itemType := ""
	if len(types) == 1 {
		itemType = types[0]
	}
	result, err := operations.SearchRemotes(ctx, cfg, operations.SearchRemotesRequest{
		Query:    query,
		ItemType: itemType,
	})
	if err != nil {
		return nil, err
	}
	return result.Results, nil
}

// printUnifiedResults prints the combined local and remote search results.
func printUnifiedResults(localResults []operations.SearchResult, remoteResults []operations.SearchRemoteEntry) {
	fmt.Printf("Results (%d):\n\n", len(localResults)+len(remoteResults))
	if len(localResults) > 0 {
		printLocalResults(localResults)
	}
	if len(remoteResults) > 0 {
		if len(localResults) > 0 {
			fmt.Println()
		}
		printRemoteResults(remoteResults)
	}
}

// printLocalResults prints local search results grouped by type.
func printLocalResults(results []operations.SearchResult) {
	// Group by type
	byType := make(map[string][]operations.SearchResult)
	for _, r := range results {
		byType[r.Type] = append(byType[r.Type], r)
	}

	typeOrder := []string{"fragment", "skill", "profile", "mcp_server"}
	typeNames := map[string]string{
		"fragment":   "Fragments",
		"skill":      "Skills",
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
			tags = textutil.TruncateBytes(tags, 17) + "..."
		}

		name := r.Name
		if len(name) > 18 {
			name = textutil.TruncateBytes(name, 15) + "..."
		}

		itemType := r.Type

		fmt.Printf("  %-8s │ %-12s │ %-20s │ %s\n", itemType, r.Remote, name, tags)
	}

	fmt.Println()
	fmt.Println("Use one: add its ref to a profile (ctxloom profile create/modify), then ctxloom remote pull")
}
