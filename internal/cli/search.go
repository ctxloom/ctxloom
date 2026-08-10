package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// searchLocalLimit caps the local half of a search — a huge tag-only query
// (e.g. a popular tag across hundreds of fragments) must not dump an
// unreadable wall of text. See noteHiddenLocalMatches: whatever this cap
// hides is reported, never silently dropped (remote search is not limited).
const searchLocalLimit = 100

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

By default, searches both local content (fragments, commands, skills,
profiles) and remote repositories (bundles). --type narrows to one kind;
--local/--remote narrow to one source. A query and/or --tag is required —
an empty search matches everything and is refused rather than dumping the
whole library.

Local results are capped at 100; if a query matches more, a note on stderr
says how many were hidden and how to narrow (this never affects --format
json, which reports the same count via "hidden_local"). Remote results are
not capped.

Use one match: add its ref to a profile (ctxloom profile create/modify),
then ctxloom deps pull for a remote bundle, or ctxloom fragment show /
ctxloom command show / ctxloom skill show for local content.`,
	Example: `  ctxloom search cache                    # search all sources
  ctxloom search -t golang                # search by tag
  ctxloom search -t golang,testing        # search by multiple tags (OR)
  ctxloom search --local cache            # search only local content
  ctxloom search --remote golang          # search only remote repositories
  ctxloom search --type fragment cache    # search only fragments
  ctxloom search --type skill review      # search only Agent Skills
  ctxloom search --type bundle golang     # search only remote bundles
  ctxloom search --format json cache      # machine-readable output`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSearch,
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := ""
	if len(args) > 0 {
		query = args[0]
	}
	return runUnifiedSearch(cmd, query, searchTags, searchItemFilter, searchLocalOnly, searchRemoteOnly)
}

func init() {
	rootCmd.AddCommand(searchCmd)

	searchCmd.Flags().StringSliceVarP(&searchTags, "tag", "t", nil, "Filter by tags (comma-separated; matches any)")
	searchCmd.Flags().StringVar(&searchItemFilter, "type", "", "Filter by type: fragment, command, skill, profile, bundle, mcp_server (default: all)")
	searchCmd.Flags().BoolVar(&searchLocalOnly, "local", false, "Search only local content")
	searchCmd.Flags().BoolVar(&searchRemoteOnly, "remote", false, "Search only remote repositories")
}

// unifiedSearchOutput is the --format json shape for `ctxloom search`: local
// and remote results plus the same hidden-match signal the text renderer
// prints as a stderr hint, so a script sees the truncation too instead of
// silently getting a partial answer that looks complete.
type unifiedSearchOutput struct {
	Query       string                         `json:"query"`
	Local       []operations.SearchResult      `json:"local"`
	Remote      []operations.SearchRemoteEntry `json:"remote"`
	Count       int                            `json:"count"`
	HiddenLocal int                            `json:"hidden_local,omitempty"`
}

// runUnifiedSearch searches both local and remote sources and renders the
// combined result via the global --format flag.
func runUnifiedSearch(cmd *cobra.Command, query string, tags []string, itemType string, localOnly, remoteOnly bool) error {
	if query == "" && len(tags) == 0 {
		return fmt.Errorf("please provide a search query or tags — e.g. ctxloom search cache, or ctxloom search --tag golang")
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

	localResults, hiddenLocal, remoteResults, localErr, remoteErr := runSearches(cmd.Context(), cfg, searchParams{
		query:       query,
		tags:        tags,
		localTypes:  localTypes,
		remoteTypes: remoteTypes,
		localScope:  localScope,
		remoteScope: remoteScope,
	})

	// A search whose every attempted scope came back with an error
	// must not report "No results found." — that asserts a false fact (the
	// library may be full of matches; the search just never reached it).
	localAttempted := localScope && len(localTypes) > 0
	remoteAttempted := remoteScope && len(remoteTypes) > 0
	if searchFullyFailed(localAttempted, localErr, remoteAttempted, remoteErr) {
		return fmt.Errorf("search failed: local: %v, remote: %v", localErr, remoteErr)
	}

	// The hint rides stderr regardless of --format, so `search --format json
	// | jq` stays clean while a human running the same command still sees why
	// their answer looks short.
	noteHiddenLocalMatches(cmd.ErrOrStderr(), hiddenLocal)

	out := unifiedSearchOutput{
		Query:       query,
		Local:       localResults,
		Remote:      remoteResults,
		Count:       len(localResults) + len(remoteResults),
		HiddenLocal: hiddenLocal,
	}
	return emit(cmd, out, func() error {
		w := cmd.OutOrStdout()
		if out.Count == 0 {
			fmt.Fprintln(w, "No results found.")
			return nil
		}
		printUnifiedResults(w, localResults, remoteResults)
		return nil
	})
}

// searchFullyFailed reports whether every search scope actually attempted
// (requested via localOnly/remoteOnly/--type narrowing, AND had at least one
// type to search) came back with an error. A scope that was never attempted
// at all contributes nothing to this decision — narrowing to --local when
// --remote was never asked for is not a failure. True only when there was at
// least one attempted scope and ALL of them failed.
func searchFullyFailed(localAttempted bool, localErr error, remoteAttempted bool, remoteErr error) bool {
	attempted, failed := 0, 0
	if localAttempted {
		attempted++
		if localErr != nil {
			failed++
		}
	}
	if remoteAttempted {
		attempted++
		if remoteErr != nil {
			failed++
		}
	}
	return attempted > 0 && attempted == failed
}

// noteHiddenLocalMatches tells the user (on stderr, so stdout/--format json
// stays parseable) when the local search's searchLocalLimit cap truncated
// matches. Mirrors taskloom's hidden-match hint: a query result must never
// shrink without saying why and how to see the rest.
func noteHiddenLocalMatches(w io.Writer, hidden int) {
	if hidden <= 0 {
		return
	}
	fmt.Fprintf(w, "ctxloom: %d more local match(es) hidden by the %d-result cap — narrow with --type or --tag to see the rest\n",
		hidden, searchLocalLimit)
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
		// Profiles and skills are searched locally only; top-level profile
		// distribution was retired, so remote search covers bundles (and the
		// fragments/commands/skills they ship).
		return []string{"fragment", "command", "skill", "profile", "mcp_server"}, []string{"bundle"}, nil
	}
	switch itemType {
	case "fragment", "command", "skill", "mcp_server", "profile":
		return []string{itemType}, nil, nil
	case "bundle":
		return nil, []string{itemType}, nil
	default:
		return nil, nil, fmt.Errorf("unknown type %q (valid: fragment, command, skill, profile, bundle, mcp_server) — see 'ctxloom search --help'", itemType)
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
// their results plus the count of local matches the searchLocalLimit cap hid.
// Errors are reported as warnings (search degrades gracefully).
func runSearches(ctx context.Context, cfg *config.Config, p searchParams) (localResults []operations.SearchResult, hiddenLocal int, remoteResults []operations.SearchRemoteEntry, localErr, remoteErr error) {
	var wg sync.WaitGroup
	if p.localScope && len(p.localTypes) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			localResults, hiddenLocal, localErr = searchLocalContent(ctx, cfg, p.query, p.tags, p.localTypes)
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
	return localResults, hiddenLocal, remoteResults, localErr, remoteErr
}

// searchLocalContent searches local fragments, commands, skills, profiles, and
// MCP servers, and reports how many additional matches searchLocalLimit hid.
func searchLocalContent(ctx context.Context, cfg *config.Config, query string, tags, types []string) ([]operations.SearchResult, int, error) {
	result, err := operations.SearchContent(ctx, cfg, operations.SearchContentRequest{
		Query: query,
		Types: types,
		Tags:  tags,
		Limit: searchLocalLimit,
	})
	if err != nil {
		return nil, 0, err
	}
	return result.Results, result.TotalMatches - result.Count, nil
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

// printUnifiedResults prints the combined local and remote search results to
// w — the command's own writer, never process-global stdout, so a caller that
// redirects the command actually receives the output it asked for.
func printUnifiedResults(w io.Writer, localResults []operations.SearchResult, remoteResults []operations.SearchRemoteEntry) {
	fmt.Fprintf(w, "Results (%d):\n\n", len(localResults)+len(remoteResults))
	if len(localResults) > 0 {
		printLocalResults(w, localResults)
	}
	if len(remoteResults) > 0 {
		if len(localResults) > 0 {
			fmt.Fprintln(w)
		}
		printRemoteResults(w, remoteResults)
	}
}

// printLocalResults prints local search results grouped by type.
func printLocalResults(w io.Writer, results []operations.SearchResult) {
	// Group by type
	byType := make(map[string][]operations.SearchResult)
	for _, r := range results {
		byType[r.Type] = append(byType[r.Type], r)
	}

	typeOrder := []string{"fragment", "command", "skill", "profile", "mcp_server"}
	typeNames := map[string]string{
		"fragment":   "Fragments",
		"command":    "Commands",
		"skill":      "Skills",
		"profile":    "Profiles",
		"mcp_server": "MCP Servers",
	}

	for _, t := range typeOrder {
		items := byType[t]
		if len(items) == 0 {
			continue
		}

		fmt.Fprintf(w, "%s:\n", typeNames[t])
		for _, item := range items {
			fmt.Fprintf(w, "  - %s", item.Name)
			if len(item.Tags) > 0 {
				fmt.Fprintf(w, " [%s]", strings.Join(item.Tags, ", "))
			}
			if item.Source != "" {
				fmt.Fprintf(w, " (%s)", item.Source)
			}
			fmt.Fprintln(w)
		}
	}
}

// The remote-results table's geometry. Header, rule and every data row derive
// from these, so a width can never be changed in one place and left stale in
// another — the rule in particular used to be a hand-drawn literal.
const (
	remoteColIndent   = "  "
	remoteTypeWidth   = 8
	remoteRemoteWidth = 12
	remoteNameWidth   = 20
	// remoteNameCap truncates a name two columns short of its own cell so a
	// maximal name still leaves a gutter before the next separator. The
	// ellipsis is reserved by Ellipsize, not pre-subtracted here.
	remoteNameCap = remoteNameWidth - 2
	// remoteTagsCap has no column width to answer to: Tags is the last column
	// and runs to end of line, so its cap is a readability limit alone.
	remoteTagsCap = 20
	// remoteTagsRule is cosmetic — the rule under an unbounded last column is
	// drawn to a fixed length rather than to any real cell width.
	remoteTagsRule = 12
)

// remoteTableRow renders one row (and, given the header words, the header) at
// the widths above.
func remoteTableRow(itemType, remote, name, tags string) string {
	return fmt.Sprintf("%s%-*s │ %-*s │ %-*s │ %s\n", remoteColIndent,
		remoteTypeWidth, itemType, remoteRemoteWidth, remote, remoteNameWidth, name, tags)
}

// remoteTableRule draws the rule under the header with each ┼ under the │
// above it. A column's rule spans its own width plus the space on either side
// of the separator; the first column's leading space is part of the row
// indent, so it takes one fewer.
func remoteTableRule() string {
	seg := func(width int) string { return strings.Repeat("─", width) }
	return remoteColIndent +
		seg(remoteTypeWidth+1) + "┼" +
		seg(remoteRemoteWidth+2) + "┼" +
		seg(remoteNameWidth+2) + "┼" +
		seg(remoteTagsRule) + "\n"
}

// printRemoteResults prints remote search results in table format.
func printRemoteResults(w io.Writer, results []operations.SearchRemoteEntry) {
	fmt.Fprintln(w, "Remote:")
	fmt.Fprint(w, remoteTableRow("Type", "Remote", "Name", "Tags"))
	fmt.Fprint(w, remoteTableRule())

	for _, r := range results {
		fmt.Fprint(w, remoteTableRow(
			r.Type,
			r.Remote,
			textutil.Ellipsize(r.Name, remoteNameCap),
			textutil.Ellipsize(strings.Join(r.Tags, ", "), remoteTagsCap),
		))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Use one: add its ref to a profile (ctxloom profile create/modify), then ctxloom deps pull")
}
