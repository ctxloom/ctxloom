package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
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
	var remoteResults []remote.SearchResult
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

	// Search remote content
	if searchRemoteScope && len(remoteTypes) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, err := searchRemotesForContent(ctx, query, remoteTypes)
			if err != nil {
				remoteErr = err
				return
			}
			remoteResults = results
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

// searchRemotesForContent searches all configured remote repositories.
func searchRemotesForContent(ctx context.Context, query string, types []string) ([]remote.SearchResult, error) {
	registry, err := remote.NewRegistry("")
	if err != nil {
		return nil, err
	}

	remotes := registry.List()
	if len(remotes) == 0 {
		return nil, nil // No remotes configured
	}

	auth := remote.LoadAuth("")
	parsedQuery := remote.ParseSearchQuery(query)

	// Determine item types to search
	var itemTypes []remote.ItemType
	for _, t := range types {
		switch t {
		case "bundle":
			itemTypes = append(itemTypes, remote.ItemTypeBundle)
		case "profile":
			itemTypes = append(itemTypes, remote.ItemTypeProfile)
		}
	}

	// Search all remotes in parallel
	var wg sync.WaitGroup
	resultsCh := make(chan []remote.SearchResult, len(remotes)*len(itemTypes))
	errorsCh := make(chan error, len(remotes)*len(itemTypes))

	for _, rem := range remotes {
		for _, itemType := range itemTypes {
			wg.Add(1)
			go func(r *remote.Remote, t remote.ItemType) {
				defer wg.Done()

				fetcher, err := remote.NewFetcher(r.URL, auth)
				if err != nil {
					errorsCh <- fmt.Errorf("%s: %w", r.Name, err)
					return
				}

				owner, repo, err := remote.ParseRepoURL(r.URL)
				if err != nil {
					errorsCh <- fmt.Errorf("%s: %w", r.Name, err)
					return
				}

				branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
				if err != nil {
					errorsCh <- fmt.Errorf("%s: %w", r.Name, err)
					return
				}

				results, err := searchRemoteManifest(ctx, fetcher, r, owner, repo, branch, t, parsedQuery)
				if err != nil {
					errorsCh <- fmt.Errorf("%s (%s): %w", r.Name, t, err)
					return
				}
				resultsCh <- results
			}(rem, itemType)
		}
	}

	wg.Wait()
	close(resultsCh)
	close(errorsCh)

	// Collect results
	var allResults []remote.SearchResult
	for results := range resultsCh {
		allResults = append(allResults, results...)
	}

	// Print errors as warnings
	for err := range errorsCh {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	return allResults, nil
}

// searchRemoteManifest searches a remote's manifest for matching items.
func searchRemoteManifest(ctx context.Context, fetcher remote.Fetcher, rem *remote.Remote, owner, repo, branch string, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	// Try manifest first
	manifestPath := fmt.Sprintf("ctxloom/%s/manifest.yaml", rem.Version)
	manifestContent, err := fetcher.FetchFile(ctx, owner, repo, manifestPath, branch)
	if err != nil {
		// Fall back to directory listing
		return searchRemoteDirectory(ctx, fetcher, rem, owner, repo, branch, itemType, query)
	}

	var manifest remote.Manifest
	if err := parseYAML(manifestContent, &manifest); err != nil {
		return nil, err
	}

	var entries []remote.ManifestEntry
	switch itemType {
	case remote.ItemTypeBundle:
		entries = manifest.Bundles
	case remote.ItemTypeProfile:
		entries = manifest.Profiles
	}

	var results []remote.SearchResult
	for _, entry := range entries {
		if remote.MatchesQuery(entry, query) {
			results = append(results, remote.SearchResult{
				Remote:    rem.Name,
				Entry:     entry,
				RemoteURL: rem.URL,
				ItemType:  itemType,
			})
		}
	}

	return results, nil
}

// searchRemoteDirectory searches by listing directory contents.
func searchRemoteDirectory(ctx context.Context, fetcher remote.Fetcher, rem *remote.Remote, owner, repo, branch string, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	dirPath := fmt.Sprintf("ctxloom/%s/%s", rem.Version, itemType.DirName())

	entries, err := fetcher.ListDir(ctx, owner, repo, dirPath, branch)
	if err != nil {
		return nil, err
	}

	var results []remote.SearchResult
	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".yaml") {
			continue
		}

		name := strings.TrimSuffix(entry.Name, ".yaml")
		manifestEntry := remote.ManifestEntry{Name: name}

		if remote.MatchesQuery(manifestEntry, query) {
			results = append(results, remote.SearchResult{
				Remote:    rem.Name,
				Entry:     manifestEntry,
				RemoteURL: rem.URL,
				ItemType:  itemType,
			})
		}
	}

	return results, nil
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
func printRemoteResults(results []remote.SearchResult) {
	fmt.Println("Remote:")
	fmt.Printf("  %-8s │ %-12s │ %-20s │ %s\n", "Type", "Remote", "Name", "Tags")
	fmt.Printf("  ─────────┼──────────────┼──────────────────────┼────────────\n")

	for _, r := range results {
		tags := strings.Join(r.Entry.Tags, ", ")
		if len(tags) > 20 {
			tags = tags[:17] + "..."
		}

		name := r.Entry.Name
		if len(name) > 18 {
			name = name[:15] + "..."
		}

		itemType := "bundle"
		if r.ItemType == remote.ItemTypeProfile {
			itemType = "profile"
		}

		fmt.Printf("  %-8s │ %-12s │ %-20s │ %s\n", itemType, r.Remote, name, tags)
	}

	fmt.Println()
	fmt.Println("Install with: ctxloom pull <remote>/<name>")
}

// parseYAML is a helper to parse YAML content.
func parseYAML(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}
