package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/operations"
)

var (
	discoverSource string
	discoverLimit  int
	discoverStars  int
)

var remoteDiscoverCmd = &cobra.Command{
	Use:   "discover [query]",
	Short: "Search GitHub/GitLab for SCM repositories",
	Long: `Discover SCM repositories on GitHub and GitLab.

Searches for repositories named 'ctxloom' or starting with 'scm-'.
Only repositories with valid ctxloom/v1/ structure are shown.

Examples:
  ctxloom remote discover                      # Find all SCM repos
  ctxloom remote discover golang               # Filter by 'golang' in description
  ctxloom remote discover --source github      # Search GitHub only
  ctxloom remote discover --stars 10           # Only repos with 10+ stars`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := GetConfig()
		if err != nil {
			return err
		}

		query := ""
		if len(args) > 0 {
			query = strings.Join(args, " ")
		}

		fmt.Printf("Searching repositories...")

		result, err := operations.DiscoverRemotes(cmd.Context(), cfg, operations.DiscoverRemotesRequest{
			Query:    query,
			Source:   discoverSource,
			MinStars: discoverStars,
			Limit:    discoverLimit,
		})
		if err != nil {
			return err
		}

		fmt.Printf(" found %d\n", result.Count)

		// Print errors
		for _, errMsg := range result.Errors {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", errMsg)
		}

		if result.Count == 0 {
			fmt.Println("\nNo SCM repositories found.")
			if query != "" {
				fmt.Printf("Try a different search term or remove the filter.\n")
			}
			return nil
		}

		// Display results
		fmt.Println()
		fmt.Printf("  # │ Forge  │ Repository          │ Stars │ Description\n")
		fmt.Printf("────┼────────┼─────────────────────┼───────┼─────────────────────────────────────\n")

		for i, r := range result.Repositories {
			// Truncate description
			desc := r.Description
			if len(desc) > 35 {
				desc = desc[:32] + "..."
			}

			repoName := fmt.Sprintf("%s/%s", r.Owner, r.Name)
			if len(repoName) > 19 {
				repoName = repoName[:16] + "..."
			}

			forgeDisplay := "GitHub"
			if r.Forge == "gitlab" {
				forgeDisplay = "GitLab"
			}

			fmt.Printf("%3d │ %-6s │ %-19s │ %5d │ %s\n",
				i+1, forgeDisplay, repoName, r.Stars, desc)
		}

		fmt.Println()

		// Interactive add
		if err := interactiveAdd(cmd, result.Repositories); err != nil {
			return err
		}

		return nil
	},
}

// interactiveAdd prompts the user to add a discovered repo as a remote.
func interactiveAdd(cmd *cobra.Command, repos []operations.RepoEntry) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("Add remote? Enter number (or 'q' to quit): ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return nil // EOF is ok
		}

		input = strings.TrimSpace(input)
		if input == "q" || input == "" {
			return nil
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > len(repos) {
			fmt.Printf("Invalid selection. Enter 1-%d or 'q'.\n", len(repos))
			continue
		}

		repo := repos[num-1]

		// Suggest name
		defaultName := repo.Owner
		fmt.Printf("Name for remote [%s]: ", defaultName)
		nameInput, _ := reader.ReadString('\n')
		name := strings.TrimSpace(nameInput)
		if name == "" {
			name = defaultName
		}

		// Add remote using operations
		result, err := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
			Name: name,
			URL:  repo.URL,
		})
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		if result.Warning != "" {
			fmt.Printf("Warning: %s\n", result.Warning)
		}

		fmt.Printf("Added remote '%s' → %s\n\n", result.Name, result.URL)
	}
}

func init() {
	remoteCmd.AddCommand(remoteDiscoverCmd)

	remoteDiscoverCmd.Flags().StringVarP(&discoverSource, "source", "s", "all",
		"Search specific forge: github, gitlab, or all")
	remoteDiscoverCmd.Flags().IntVarP(&discoverLimit, "limit", "n", 30,
		"Maximum results per forge")
	remoteDiscoverCmd.Flags().IntVar(&discoverStars, "stars", 0,
		"Minimum star count")
}
