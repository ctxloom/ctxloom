package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// sessionQueryAll mirrors sessionListAll: search every project's sessions
// instead of just the ones started in the current working directory.
var sessionQueryAll bool

// sessionQueryState mirrors sessionListState: only matches in that state.
var sessionQueryState string

// sessionSearchCmd is the session noun's content search. It was spelled
// `query`; `search` is the one spelling for content search, matching the
// top-level `ctxloom search` (verb-spine reorg §5).
var sessionSearchCmd = &cobra.Command{
	Use:   "search <word>...",
	Short: "Search sessions by name, title and times (default: current project; --all for everything)",
	Long: `Searches session metadata: the session name, its title (the first thing
its user asked), and its start/end as ` + "`session list`" + ` shows them. Every given
word must match, case-insensitively, somewhere in a session's metadata for
that session to be included (an AND across words, not an OR). Results are
the same rows ` + "`session list`" + ` renders.

--state narrows the matches to sessions in that state (live, ended or
unknown), read from each session's liveness lock as ` + "`session list`" + ` does.
A live session's END is blank, so its previous run's end stamp is not
searched either.`,
	Example: `  ctxloom session search lockfile migration
  ctxloom session search --state ended lockfile`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSessionQuery,
}

func init() {
	sessionSearchCmd.Flags().BoolVar(&sessionQueryAll, "all", false, "Search sessions from every project (default: filter to cwd)")
	addSessionStateFlag(sessionSearchCmd, &sessionQueryState)
	sessionCmd.AddCommand(sessionSearchCmd)
}

// runSessionQuery loads the same candidate set `session list` would (project-
// scoped by default, every project under --all), keeps only the entries
// sessionMatchesQuery accepts, and renders them through the same
// emitSessionRows tail `session list` uses — so a query result is
// indistinguishable in shape from a list result, just pre-filtered.
func runSessionQuery(cmd *cobra.Command, args []string) error {
	if err := checkSessionState(sessionQueryState); err != nil {
		return err
	}
	var entries []sessions.Entry
	var err error
	if sessionQueryAll {
		entries, err = operations.ListAllSessions()
	} else {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return wdErr
		}
		entries, err = operations.ListSessionsForProject(wd)
	}
	if err != nil {
		return err
	}

	// Filtered by state before matching.
	views := filterSessionState(operations.ViewSessions(entries, sessionStateOf), sessionQueryState)
	matched := make([]operations.SessionView, 0, len(views))
	for _, v := range views {
		if sessionMatchesQuery(v, args) {
			matched = append(matched, v)
		}
	}
	return emitSessionRows(cmd, matched)
}

// sessionMatchesQuery reports whether every word in words is found
// case-insensitively in the session's metadata: harp name, title, and the
// start/end as the row shows them.
func sessionMatchesQuery(v operations.SessionView, words []string) bool {
	return allWordsMatch(sessionMetadataHaystack(v), words)
}

// sessionMetadataHaystack builds the lowercase text session query matches
// metadata words against: harp name, title, and the same start/end
// formatting SessionRow renders, so a query for a date fragment behaves
// like a query against what the user can actually see in the row — which
// has no end while the session is live.
func sessionMetadataHaystack(v operations.SessionView) string {
	var b strings.Builder
	b.WriteString(v.Harp)
	b.WriteByte(' ')
	b.WriteString(v.Title)
	b.WriteByte(' ')
	b.WriteString(sessionTime(v.StartedAt).String())
	if v.EndedAt != nil && v.State != operations.SessionLive {
		b.WriteByte(' ')
		b.WriteString(sessionTime(*v.EndedAt).String())
	}
	return strings.ToLower(b.String())
}

// allWordsMatch reports whether every word appears case-insensitively in
// haystack — an AND across the query terms: `session query harp bugfix`
// matches sessions whose combined text contains both words, not either.
func allWordsMatch(haystack string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(haystack, strings.ToLower(w)) {
			return false
		}
	}
	return true
}
