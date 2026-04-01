package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

var (
	searchTags       []string
	searchItemFilter string
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search fragments and prompts",
	Long: `Search for fragments and prompts by name, content, or tags.

Examples:
  ctxloom search cache                    # Search by name/content
  ctxloom search -t golang                # Search by tag
  ctxloom search -t golang,testing        # Search by multiple tags
  ctxloom search --type fragment cache    # Search only fragments`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		return runSearch(query, searchTags, searchItemFilter)
	},
}

func init() {
	rootCmd.AddCommand(searchCmd)

	searchCmd.Flags().StringSliceVarP(&searchTags, "tag", "t", nil, "Filter by tags (comma-separated)")
	searchCmd.Flags().StringVar(&searchItemFilter, "type", "", "Filter by type (fragment or prompt)")
}

func runSearch(query string, tags []string, itemType string) error {
	if query == "" && len(tags) == 0 {
		return fmt.Errorf("please provide a search query or tags")
	}

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	loader := bundles.NewLoader(cfg.GetBundleDirs(), false)

	var results []bundles.ContentInfo

	// Load fragments unless filtering to prompts only
	if itemType == "" || itemType == "fragment" {
		fragments, err := loader.ListAllFragments()
		if err != nil {
			return fmt.Errorf("failed to list fragments: %w", err)
		}
		results = append(results, fragments...)
	}

	// Load prompts unless filtering to fragments only
	if itemType == "" || itemType == "prompt" {
		prompts, err := loader.ListAllPrompts()
		if err != nil {
			return fmt.Errorf("failed to list prompts: %w", err)
		}
		results = append(results, prompts...)
	}

	// Filter by tags
	if len(tags) > 0 {
		results = filterByTags(results, tags)
	}

	// Filter by query (name match)
	if query != "" {
		results = filterByQuery(results, query)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	fmt.Printf("Results (%d):\n\n", len(results))
	printResults(results)

	return nil
}

func filterByTags(items []bundles.ContentInfo, tags []string) []bundles.ContentInfo {
	var filtered []bundles.ContentInfo
	for _, item := range items {
		if hasAnyTag(item.Tags, tags) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func hasAnyTag(itemTags, searchTags []string) bool {
	tagSet := make(map[string]bool)
	for _, t := range itemTags {
		tagSet[strings.ToLower(t)] = true
	}
	for _, t := range searchTags {
		if tagSet[strings.ToLower(t)] {
			return true
		}
	}
	return false
}

func filterByQuery(items []bundles.ContentInfo, query string) []bundles.ContentInfo {
	query = strings.ToLower(query)
	var filtered []bundles.ContentInfo
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Name), query) ||
			strings.Contains(strings.ToLower(item.Bundle), query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func printResults(items []bundles.ContentInfo) {
	// Group by type then bundle
	fragments := make(map[string][]bundles.ContentInfo)
	prompts := make(map[string][]bundles.ContentInfo)

	for _, item := range items {
		if item.ItemType == "fragment" {
			fragments[item.Bundle] = append(fragments[item.Bundle], item)
		} else {
			prompts[item.Bundle] = append(prompts[item.Bundle], item)
		}
	}

	if len(fragments) > 0 {
		fmt.Println("Fragments:")
		printGroupedItems(fragments, "fragments")
	}

	if len(prompts) > 0 {
		if len(fragments) > 0 {
			fmt.Println()
		}
		fmt.Println("Prompts:")
		printGroupedItems(prompts, "prompts")
	}
}

func printGroupedItems(grouped map[string][]bundles.ContentInfo, prefix string) {
	for bundle, items := range grouped {
		fmt.Printf("  %s:\n", bundle)
		for _, item := range items {
			fmt.Printf("    - %s", item.Name)
			if len(item.Tags) > 0 {
				fmt.Printf(" [%s]", strings.Join(item.Tags, ", "))
			}
			fmt.Println()
		}
	}
}
