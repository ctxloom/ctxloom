package cli

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/config"
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
	RunE: runRemoteDiscoverCmd,
}

func runRemoteDiscoverCmd(cmd *cobra.Command, args []string) error {
	return runRemoteDiscover(cmd, args, GetConfig, nil)
}

// runRemoteDiscover searches for discoverable ctxloom repositories, emits the
// result, and — in text mode on an interactive terminal only — offers to add
// one. loadConfig and fetcher are injected so the search's failure and result
// paths are testable without a cobra dispatch or a network; fetcher is nil in
// production (operations.DiscoverRemotes falls back to the real GitHub
// fetcher).
func runRemoteDiscover(cmd *cobra.Command, args []string, loadConfig func() (*config.Config, error), fetcher remote.Fetcher) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	query := ""
	if len(args) > 0 {
		query = strings.Join(args, " ")
	}

	// Progress is a diagnostic, never part of the result: stderr.
	progress := cmd.ErrOrStderr()
	fmt.Fprint(progress, "Searching repositories...")

	result, err := operations.DiscoverRemotes(cmd.Context(), cfg, operations.DiscoverRemotesRequest{
		Query:         query,
		Source:        discoverSource,
		MinStars:      discoverStars,
		Limit:         discoverLimit,
		GitHubFetcher: fetcher,
	})
	if err != nil {
		// The progress line above is deliberately newline-less so the result
		// count can be appended to it. Nothing appends on this path, so close
		// it here — otherwise the error prints as a continuation of it.
		fmt.Fprintln(progress)
		return err
	}

	fmt.Fprintf(progress, " found %d\n", result.Count)

	for _, errMsg := range result.Errors {
		clidiag.Warn("ctxloom", "%s", errMsg)
	}

	// A total search failure (every configured source errored — today that is
	// the sole GitHub fetcher, so any Error here means the WHOLE search failed)
	// is not an empty result: there is no result to emit, and an empty one
	// would claim a clean search.
	if result.Count == 0 && len(result.Errors) > 0 {
		return fmt.Errorf("repository search failed (see warning(s) above); no results could be retrieved")
	}

	return emit(cmd, result, func() error {
		out := cmd.OutOrStdout()
		if result.Count == 0 {
			fmt.Fprintln(out, "No ctxloom repositories found.")
			if query != "" {
				fmt.Fprintln(out, "Try a different search term or remove the filter.")
			}
			return nil
		}
		renderDiscoveredRepos(out, result.Repositories)

		// The add-loop is a prompt/answer conversation, so it is only offered
		// on a terminal — off one it would write a question nobody can answer
		// into the caller's own output and then quit on the resulting EOF. Say
		// how to get it rather than skipping silently.
		if !isInteractiveTerminal() {
			fmt.Fprintln(out, "Run 'ctxloom remote discover' in a terminal to add one of these interactively,")
			fmt.Fprintln(out, "or add it directly: ctxloom remote create <name> <url>")
			return nil
		}
		return interactiveAdd(cmd, cfg, result.Repositories)
	})
}

// discoverDescriptionCap bounds the trailing Description column, the one
// field the table truncates.
const discoverDescriptionCap = 35

// renderDiscoveredRepos is the human table of a non-empty discovery. The
// Repository column is as wide as its widest owner/name, so a name is never
// cut; only the trailing Description is.
func renderDiscoveredRepos(out io.Writer, repos []operations.RepoEntry) {
	names := make([]string, len(repos))
	repoW := utf8.RuneCountInString("Repository")
	for i, r := range repos {
		names[i] = fmt.Sprintf("%s/%s", r.Owner, r.Name)
		repoW = max(repoW, utf8.RuneCountInString(names[i]))
	}
	seg := func(width int) string { return strings.Repeat("─", width) }
	fmt.Fprintf(out, "  # │ Forge  │ %-*s │ Stars │ Description\n", repoW, "Repository")
	fmt.Fprintf(out, "────┼────────┼%s┼───────┼%s\n", seg(repoW+2), seg(discoverDescriptionCap+2))

	for i, r := range repos {
		fmt.Fprintf(out, "%3d │ %-6s │ %-*s │ %5d │ %s\n",
			i+1, "GitHub", repoW, names[i], r.Stars, textutil.Ellipsize(r.Description, discoverDescriptionCap))
	}

	fmt.Fprintln(out)
}

// interactiveAdd prompts the user to add a discovered repo as a remote. It
// reuses the cfg the caller holds — a second GetConfig() here would re-print
// the generation's config warnings to the user.
//
// Input comes from the process-wide stdinReader (run.go), never a fresh reader:
// a second buffered reader over os.Stdin discards whatever the first one
// buffered past its line, so type-ahead answered to an earlier prompt would
// vanish here — and the lines this loop reads would vanish from later prompts.
func interactiveAdd(cmd *cobra.Command, cfg *config.Config, repos []operations.RepoEntry) error {
	reader := stdinReader
	for {
		num, quit := readRepoChoice(reader, len(repos))
		if quit {
			return nil
		}
		if num == 0 {
			continue // invalid selection — re-prompt
		}

		repo := repos[num-1]
		name, ok := promptRemoteName(reader, repo.Owner)
		if !ok {
			// Input ended between choosing a repo and naming it. Adding one
			// under the default name here would register a remote the user
			// never confirmed, so stop instead.
			return nil
		}
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

// promptRemoteName asks for a remote name, defaulting to defaultName when the
// user answers with an empty line. ok is false when the line could not be read
// at all (EOF, closed stdin): an ANSWER of "" is consent to the default, a
// failure to read one is not, and the two must not collapse into the same
// value — the caller registers a remote off the result.
func promptRemoteName(reader *bufio.Reader, defaultName string) (name string, ok bool) {
	fmt.Printf("Name for remote [%s]: ", defaultName)
	nameInput, err := reader.ReadString('\n')
	name = strings.TrimSpace(nameInput)
	// ReadString reports io.EOF alongside a final line that carries no
	// trailing newline, so bytes read ARE an answer whatever the terminator;
	// only a read that produced none is a failure to answer.
	if err != nil && name == "" {
		return "", false
	}
	if name == "" {
		return defaultName, true
	}
	return name, true
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
