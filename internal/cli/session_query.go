package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// sessionQueryAll mirrors sessionListAll: search every project's sessions
// instead of just the ones started in the current working directory.
var sessionQueryAll bool

// sessionQueryFull mirrors sessionListFull: swap the lean result rows for
// the FULL projection carrying each matched session's complete essence body.
var sessionQueryFull bool

// sessionSearchCmd is the session noun's content search. It was spelled
// `query`; `search` is the one spelling for content search, matching the
// top-level `ctxloom search` (verb-spine reorg §5).
var sessionSearchCmd = &cobra.Command{
	Use:   "search <word>...",
	Short: "Search sessions by harp, summary, and distilled essence content (default: current project; --all for everything)",
	Long: `Searches session metadata (harp name, summary, start/end) and, for
sessions that have already been distilled, the essence body itself. Every
given word must match, case-insensitively, somewhere in a session's
metadata or essence for that session to be included (an AND across words,
not an OR).

The search itself runs CLI-side: the essence body is read off disk, scanned,
and discarded per session — it is never handed to a model and never
returned in a result row (the same lightweight harp/summary/start/end shape
` + "`session list`" + ` renders). Fetch a matched session's full essence with
` + "`session show <harp>`" + `.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSessionQuery,
}

func init() {
	sessionSearchCmd.Flags().BoolVar(&sessionQueryAll, "all", false, "Search sessions from every project (default: filter to cwd)")
	sessionSearchCmd.Flags().BoolVar(&sessionQueryFull, "full", false, "Include each matched session's complete distilled essence body (text/markdown output pages through $PAGER on a terminal)")
	sessionCmd.AddCommand(sessionSearchCmd)
}

// runSessionQuery loads the same candidate set `session list` would (project-
// scoped by default, every project under --all), keeps only the entries
// sessionMatchesQuery accepts, and renders them through the same
// emitSessionRows tail `session list` uses (SessionRow by default, or the
// --full essence-body projection) — so a query result is indistinguishable
// in shape from a list result, just pre-filtered.
func runSessionQuery(cmd *cobra.Command, args []string) error {
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

	appDir := ""
	if cfg, cErr := config.Load(); cErr == nil {
		appDir = cfg.GetAppDir()
	}

	matched := make([]sessions.Entry, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		if sessionMatchesQuery(e, args, appDir) {
			matched = append(matched, *e)
		}
	}
	return emitSessionRows(cmd, matched, sessionQueryFull, appDir)
}

// sessionMatchesQuery reports whether every word in words is found
// case-insensitively in entry's metadata (harp name, summary, formatted
// start/end) OR — only when the metadata search misses — the entry's
// distilled essence body. Metadata is checked first so an entry that
// already matches, or one that was never distilled, never pays the cost of
// reading an essence file off disk; only the entries that genuinely need
// the content fallback do.
func sessionMatchesQuery(e *sessions.Entry, words []string, appDir string) bool {
	if allWordsMatch(sessionMetadataHaystack(e), words) {
		return true
	}
	body, ok := readEssenceForQuery(e, appDir)
	if !ok {
		return false
	}
	return allWordsMatch(strings.ToLower(body), words)
}

// sessionMetadataHaystack builds the lowercase text session query matches
// metadata words against: harp name, summary, and the same start/end
// formatting SessionRow renders, so a query for a date fragment behaves
// like a query against what the user can actually see in the row.
func sessionMetadataHaystack(e *sessions.Entry) string {
	var b strings.Builder
	b.WriteString(e.HarpName)
	b.WriteByte(' ')
	b.WriteString(e.Summary)
	b.WriteByte(' ')
	b.WriteString(sessionTime(e.StartedAt).String())
	if e.EndedAt != nil {
		b.WriteByte(' ')
		b.WriteString(sessionTime(*e.EndedAt).String())
	}
	return strings.ToLower(b.String())
}

// readEssenceForQuery reads a session's distilled essence body for the
// content-search fallback, resolving it the same way `session show` does
// (operations.SessionEssenceInfo: harp-dir layout first, then the legacy
// <sessionsDir>/<sessionID>.md path). ok is false when the session was never
// distilled or the file can't be read — the query fallback is best-effort,
// never an error, matching this codebase's fault-tolerance convention (a
// dead or unreadable essence just doesn't contribute a content match).
func readEssenceForQuery(e *sessions.Entry, appDir string) (string, bool) {
	path, distilled := operations.SessionEssenceInfo(e.HarpName, e, appDir)
	if !distilled {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
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
