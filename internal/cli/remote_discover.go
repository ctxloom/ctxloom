package cli

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

var (
	discoverSource string
	discoverLimit  int
	discoverStars  int
)

var remoteDiscoverCmd = &cobra.Command{
	Use:   "discover [query]",
	Short: "Search GitHub for ctxloom repositories",
	Long: `Discover ctxloom repositories on GitHub.

Searches for repositories named 'ctxloom' or starting with 'ctxloom-'.
Only repositories with valid ctxloom/ structure are shown.

Examples:
  ctxloom remote discover                      # Find all ctxloom repos
  ctxloom remote discover golang               # Filter by 'golang' in description
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
			clidiag.Warn("ctxloom", "%s", errMsg)
		}

		if result.Count == 0 {
			fmt.Println("\nNo ctxloom repositories found.")
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
				desc = textutil.TruncateBytes(desc, 32) + "..."
			}

			repoName := fmt.Sprintf("%s/%s", r.Owner, r.Name)
			if len(repoName) > 19 {
				repoName = textutil.TruncateBytes(repoName, 16) + "..."
			}

			fmt.Printf("%3d │ %-6s │ %-19s │ %5d │ %s\n",
				i+1, "GitHub", repoName, r.Stars, desc)
		}

		fmt.Println()

		// Interactive add
		if err := interactiveAdd(cmd, cfg, result.Repositories); err != nil {
			return err
		}

		return nil
	},
}

// interactiveAdd prompts the user to add a discovered repo as a remote. It
// reuses the cfg already loaded by the caller — a second GetConfig() here would
// re-run config.Load and re-print any config warnings to the user.
func interactiveAdd(cmd *cobra.Command, cfg *config.Config, repos []operations.RepoEntry) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		num, quit := readRepoChoice(reader, len(repos))
		if quit {
			return nil
		}
		if num == 0 {
			continue // invalid selection — re-prompt
		}

		repo := repos[num-1]
		name := promptRemoteName(reader, repo.Owner)
		addDiscoveredRemote(cmd, cfg, name, repo.URL)
	}
}

// readRepoChoice prompts for a repo selection. quit is true on q/empty/EOF; a
// returned num of 0 (with quit false) means an invalid entry the caller should
// re-prompt, otherwise num is a 1-based index into the repo list.
func readRepoChoice(reader *bufio.Reader, count int) (num int, quit bool) {
	fmt.Print("Add remote? Enter number ('q' or Enter to quit): ")
	input, err := reader.ReadString('\n')
	if err != nil {
		return 0, true // EOF is ok
	}
	input = strings.TrimSpace(input)
	if input == "q" || input == "" {
		return 0, true
	}
	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || n > count {
		fmt.Printf("Invalid selection. Enter 1-%d or 'q'.\n", count)
		return 0, false
	}
	return n, false
}

// promptRemoteName asks for a remote name, defaulting to defaultName on empty
// input.
func promptRemoteName(reader *bufio.Reader, defaultName string) string {
	fmt.Printf("Name for remote [%s]: ", defaultName)
	nameInput, _ := reader.ReadString('\n')
	name := strings.TrimSpace(nameInput)
	if name == "" {
		return defaultName
	}
	return name
}

// addDiscoveredRemote adds a remote and reports the outcome (errors and
// warnings are printed, not returned — the interactive loop continues).
func addDiscoveredRemote(cmd *cobra.Command, cfg *config.Config, name, url string) {
	result, err := operations.AddRemote(cmd.Context(), cfg, operations.AddRemoteRequest{
		Name: name,
		URL:  url,
	})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	if result.Warning != "" {
		clidiag.Warn("ctxloom", "%s", result.Warning)
	}
	fmt.Printf("Added remote '%s' → %s\n\n", result.Name, result.URL)
}

func init() {
	remoteCmd.AddCommand(remoteDiscoverCmd)

	remoteDiscoverCmd.Flags().StringVarP(&discoverSource, "source", "s", "all",
		"Search source: github or all (only GitHub is searchable)")
	remoteDiscoverCmd.Flags().IntVarP(&discoverLimit, "limit", "n", 30,
		"Maximum results")
	remoteDiscoverCmd.Flags().IntVar(&discoverStars, "stars", 0,
		"Minimum star count")
}
