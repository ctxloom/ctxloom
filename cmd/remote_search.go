package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/benjaminabbitt/scm/internal/remote"
)

var searchType string

var remoteSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for bundles and profiles across remotes",
	Long: `Search for bundles and profiles across all configured remotes.

By default searches both bundles and profiles. Use --type to filter.

Query syntax:
  Plain text         Full-text search on name and description
  tag:foo/bar        Tags with AND (default)
  tag:foo/bar/OR     Tags with OR
  tag:foo/NOT        Negated tag
  author:name        Filter by author
  version:spec       Version constraint

Examples:
  scm remote search golang
  scm remote search "tag:golang/testing"
  scm remote search security --type bundle
  scm remote search "tag:enterprise author:alice"`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRemoteSearch,
}

func runRemoteSearch(cmd *cobra.Command, args []string) error {
	queryStr := strings.Join(args, " ")
	query := remote.ParseSearchQuery(queryStr)

	registry, err := remote.NewRegistry("")
	if err != nil {
		return fmt.Errorf("failed to initialize registry: %w", err)
	}

	remotes := registry.List()
	if len(remotes) == 0 {
		fmt.Println("No remotes configured. Add one with: scm remote add <name> <url>")
		return nil
	}

	auth := remote.LoadAuth("")

	// Determine which types to search
	var types []remote.ItemType
	switch searchType {
	case "bundle":
		types = []remote.ItemType{remote.ItemTypeBundle}
	case "profile":
		types = []remote.ItemType{remote.ItemTypeProfile}
	default:
		types = []remote.ItemType{remote.ItemTypeBundle, remote.ItemTypeProfile}
	}

	// Search all remotes and types in parallel
	var wg sync.WaitGroup
	resultsCh := make(chan []remote.SearchResult, len(remotes)*len(types))
	errorsCh := make(chan error, len(remotes)*len(types))

	for _, rem := range remotes {
		for _, itemType := range types {
			wg.Add(1)
			go func(r *remote.Remote, t remote.ItemType) {
				defer wg.Done()

				results, err := searchRemote(cmd.Context(), r, t, query, auth)
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

	// Print errors
	for err := range errorsCh {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	if len(allResults) == 0 {
		fmt.Printf("No results found matching: %s\n", queryStr)
		return nil
	}

	// Display results
	fmt.Printf("Found %d results matching: %s\n\n", len(allResults), queryStr)
	fmt.Printf("  %-8s │ %-12s │ %-20s │ %-25s │ %s\n", "Type", "Remote", "Name", "Tags", "Author")
	fmt.Printf("──────────┼──────────────┼──────────────────────┼───────────────────────────┼────────────\n")

	for _, r := range allResults {
		tags := strings.Join(r.Entry.Tags, ", ")
		if len(tags) > 23 {
			tags = tags[:20] + "..."
		}

		name := r.Entry.Name
		if len(name) > 18 {
			name = name[:15] + "..."
		}

		itemType := "bundle"
		if r.ItemType == remote.ItemTypeProfile {
			itemType = "profile"
		}

		fmt.Printf("  %-8s │ %-12s │ %-20s │ %-25s │ %s\n",
			itemType, r.Remote, name, tags, r.Entry.Author)
	}

	fmt.Println()
	fmt.Println("Install with: scm install <remote>/<name>")

	return nil
}

// searchRemote searches a single remote for matching items.
func searchRemote(ctx context.Context, rem *remote.Remote, itemType remote.ItemType, query remote.SearchQuery, auth remote.AuthConfig) ([]remote.SearchResult, error) {
	fetcher, err := remote.NewFetcher(rem.URL, auth)
	if err != nil {
		return nil, err
	}

	owner, repo, err := remote.ParseRepoURL(rem.URL)
	if err != nil {
		return nil, err
	}

	// Get default branch
	branch, err := fetcher.GetDefaultBranch(ctx, owner, repo)
	if err != nil {
		return nil, err
	}

	// Try to fetch manifest first (faster)
	manifestPath := fmt.Sprintf("scm/%s/manifest.yaml", rem.Version)
	manifestContent, err := fetcher.FetchFile(ctx, owner, repo, manifestPath, branch)
	if err == nil {
		// Parse manifest and search
		return searchManifest(rem, manifestContent, itemType, query)
	}

	// Fall back to directory listing
	return searchDirectory(ctx, fetcher, rem, owner, repo, branch, itemType, query)
}

// searchManifest searches the manifest for matching items.
func searchManifest(rem *remote.Remote, content []byte, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	var manifest remote.Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
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

// searchDirectory searches by listing directory contents.
func searchDirectory(ctx context.Context, fetcher remote.Fetcher, rem *remote.Remote, owner, repo, branch string, itemType remote.ItemType, query remote.SearchQuery) ([]remote.SearchResult, error) {
	dirPath := fmt.Sprintf("scm/%s/%s", rem.Version, itemType.DirName())

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

		// Create a minimal manifest entry for matching
		manifestEntry := remote.ManifestEntry{
			Name: name,
		}

		// Only do text matching without fetching full content
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

func init() {
	remoteCmd.AddCommand(remoteSearchCmd)

	remoteSearchCmd.Flags().StringVarP(&searchType, "type", "t", "",
		"Filter by type: bundle or profile")
}
