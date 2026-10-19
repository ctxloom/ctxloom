package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
)

// Bare `ctxloom session` lists the recorded sessions: the collection is the
// one thing the noun is about, and reading it touches nothing.
var sessionCmd = groupNodeDefault(&cobra.Command{
	Use:   "session",
	Short: "Browse and manage your recorded sessions",
	Long: `Read and manage the named sessions under ~/.ctxloom/sessions:
one directory per session, each carrying its own record. Use to list/show/edit/remove
sessions without launching the LLM. Sessions appear here automatically
once ` + "`ctxloom run`" + ` has been used to launch a backend.`,
}, "list")

var (
	sessionListAll bool
	// sessionListState backs --state: only rows in that state.
	sessionListState string
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded sessions (default: current project; --all for everything)",
	Example: `  ctxloom session list
  ctxloom session list --all
  ctxloom session list --state live`,
	RunE: runSessionList,
}

func runSessionList(cmd *cobra.Command, _ []string) error {
	if err := checkSessionState(sessionListState); err != nil {
		return err
	}
	entries, err := loadSessionEntries(sessionListAll)
	if err != nil {
		return err
	}
	// The rows render from the one read model (operations.SessionView),
	// never from the entry: harp, title (the first user prompt), state,
	// start, end. State is one lock probe per row; the title one transcript
	// parse per row.
	views := filterSessionState(operations.ViewSessions(entries, sessionStateOf), sessionListState)
	return emitSessionRows(cmd, views)
}

// loadSessionEntries reads the session index: every project's sessions when
// all is set (ListAllSessions — enriched + activity-sorted like the
// per-project path), the cwd's project otherwise.
func loadSessionEntries(all bool) ([]sessions.Entry, error) {
	load := operations.ListSessionsForProject
	var entries []sessions.Entry
	var err error
	if all {
		entries, err = operations.ListAllSessions()
	} else {
		wd, _ := os.Getwd()
		entries, err = load(wd)
	}
	if err != nil {
		return nil, err
	}
	return entries, nil
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <session-name>",
	Short: "Show a named session: its title, state, engine and times",
	Long: `Shows one session's record: its title (the first thing its user asked,
from the session's recorded transcript), its state now (from its liveness
lock), its engine and native session, its project, when it started and
ended, and whether it was purged or is marked keep.`,
	Example: `  ctxloom session show amber-swift-owl`,
	Args:    cobra.ExactArgs(1),
	RunE:    runSessionShow,
}

func runSessionShow(cmd *cobra.Command, args []string) error {
	harp := args[0]
	entry, err := operations.GetSession(harp)
	if err != nil {
		return err
	}
	if entry == nil {
		return errNoSession(harp)
	}
	view := operations.ViewSession(*entry)
	view.State = sessionStateOf(harp)
	return emit(cmd, view, func() error { return renderSessionShow(cmd.OutOrStdout(), view) })
}

// renderSessionShow is the human render of one session: one fact per line,
// absent facts left out rather than printed blank.
func renderSessionShow(w io.Writer, v operations.SessionView) error {
	ew := errwriter.New(w)
	if v.EndReason != nil {
		// Ahead of the title: for a session that never launched, why it ended
		// is the whole story.
		ew.Println(endReasonText(*v.EndReason))
	}
	title := v.Title
	if title == "" {
		title = "(no summary)"
	}
	ew.Printf("%s\n%s\n\n", v.Harp, title)
	ew.Printf("state:    %s\n", v.State)
	ew.Printf("engine:   %s\n", v.Engine)
	if v.NativeSession != "" {
		ew.Printf("native:   %s\n", v.NativeSession)
	}
	ew.Printf("project:  %s\n", v.Project)
	ew.Printf("started:  %s\n", sessionTime(v.StartedAt))
	if v.EndedAt != nil && v.State != operations.SessionLive {
		ew.Printf("ended:    %s\n", sessionTime(*v.EndedAt))
	}
	if v.Purged {
		ew.Println("purged:   its transcript was destroyed")
	}
	if v.Kept {
		ew.Println("kept:     its transcript is kept at any age (`ctxloom session keep --undo` releases it)")
	}
	return ew.Err()
}

// endReasonText renders a session's end reason for `session show`: its class
// and exit status, the error ctxloom reported, then each fatal finding as it
// was printed.
func endReasonText(r sessions.EndReason) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ended before its engine launched: %s (exit %d)", r.Class, r.Exit)
	if r.Message != "" {
		b.WriteString("\n" + r.Message)
	}
	for _, f := range r.Findings {
		b.WriteString("\n" + f)
	}
	return b.String()
}

// sessionRemoveCmd is the canonical spine's `remove` for the session noun,
// and the only verb here that destroys a session ENTIRELY.
//
// It removes the transcript as well as the index entry. Dropping the entry
// alone leaves the transcript on disk: a removal only from the
// index's point of view, where every byte it was supposed to destroy is still
// there and the session is merely unfindable. Someone removing a session to be
// rid of its contents would get a success message and lose nothing.
//
// The boundary with purge: purge EMPTIES a session and leaves it listed;
// remove destroys it. Authored files are outside both — never destroyed, only
// named in the report.
var sessionRemoveYes bool

var sessionRemoveCmd = &cobra.Command{
	Use:   "remove <session-name>",
	Short: "Remove a session entirely: its index entry and its transcript",
	Long: `Removes a session's own artifacts — the index entry and the recorded
transcript. The session's output dir (plans, notes) is the human's and is
left where it is.

Authored files in the session directory are never destroyed; they are named in
the report and left where they are, so removing a session cannot take work
nobody filed with it.

Without --yes this only reports; nothing on disk or in the session index
changes, on a TTY or not.

The transcript is the session's record, so a session that ended within
session_transcript_retention (default 30d), or one marked keep, is refused.

To empty a session but keep it listed, use 'ctxloom session purge'.`,
	Example: `  ctxloom session remove amber-swift-owl --yes`,
	Args:    cobra.ExactArgs(1),
	RunE:    runSessionRemove,
}

// sessionRemoveResult names each artifact separately. A single "removed:true"
// could not tell a caller that the index entry went and the files did not —
// which is precisely the failure this command shipped with.
type sessionRemoveResult struct {
	Harp              string                         `json:"harp"`
	Applied           bool                           `json:"applied"`
	IndexEntryRemoved bool                           `json:"index_entry_removed"`
	Files             *operations.PurgeSessionResult `json:"files"`
}

func runSessionRemove(cmd *cobra.Command, args []string) error {
	harp := args[0]
	apply := sessionRemoveYes
	retention, err := resolveTranscriptRetention(time.Now())
	if err != nil {
		return err
	}

	// Files first, index entry second. PurgeSession stamps purged_at BEFORE
	// it unlinks anything, so a process that dies partway leaves a row that
	// reads "purged" over its now-missing transcript rather than one that
	// silently reconciles away. Dropping the entry first and then dying would
	// instead leave files nothing in the index accounts for.
	res, purgeErr := operations.PurgeSession(harp, operations.PurgeSessionRequest{
		Harp: harp,
		Populations: []operations.PurgePopulation{
			operations.PurgePopulationTranscript,
		},
		Retention: retention,
		Apply:     apply,
	})
	// "No file matched" is a refusal for a purge and NOT for a removal: a
	// session whose files are already gone still has an index entry, and that
	// entry is the thing remove exists to destroy. Refusing here would leave
	// an unremovable row behind for anyone who purged first.
	if errors.Is(purgeErr, operations.ErrPurgeNothingToDo) {
		purgeErr = nil
	}
	refusal := harpPurgeRefusal(harp, purgeErr, "ctxloom session remove")
	if refusal == "" && purgeErr != nil {
		return purgeErr
	}

	out := sessionRemoveResult{Harp: harp, Applied: apply && refusal == "", Files: res}
	if out.Applied {
		if err := operations.ForgetSession(harp); err != nil {
			return err
		}
		out.IndexEntryRemoved = true
	}

	if err := emit(cmd, out, func() error {
		return renderSessionRemove(cmd.OutOrStdout(), out)
	}); err != nil {
		return err
	}

	if refusal != "" {
		return reportRefusal(cmd, refusal)
	}
	if !apply {
		return reportPlanOnly(cmd, fmt.Sprintf("ctxloom session remove %s --yes", harp))
	}
	return nil
}

// renderSessionRemove is the human render: the file plan, then one line for
// the index entry — the artifact that has no row in the file table and would
// otherwise go unreported.
func renderSessionRemove(w io.Writer, out sessionRemoveResult) error {
	if err := renderSessionPurgePlan(w, out.Files); err != nil {
		return err
	}
	ew := errwriter.New(w)
	if out.IndexEntryRemoved {
		ew.Printf("index entry %s: removed\n", out.Harp)
	} else {
		ew.Printf("index entry %s: would be removed\n", out.Harp)
	}
	return ew.Err()
}

func init() {
	sessionListCmd.Flags().BoolVar(&sessionListAll, "all", false, "Include sessions from every project (default: filter to cwd)")
	sessionRemoveCmd.Flags().BoolVarP(&sessionRemoveYes, "yes", "y", false,
		"apply the plan this invocation printed (default: report only)")
	addSessionStateFlag(sessionListCmd, &sessionListState)
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionEditCmd, sessionRemoveCmd, sessionApprovalsCmd)
	rootCmd.AddCommand(sessionCmd)
}
