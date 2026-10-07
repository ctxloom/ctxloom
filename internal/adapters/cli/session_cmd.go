package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
	sessionListAll     bool
	sessionListCompact bool
	sessionListFull    bool
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recorded sessions (default: current project; --all for everything)",
	Example: `  ctxloom session list
  ctxloom session list --all`,
	RunE: runSessionList,
}

func runSessionList(cmd *cobra.Command, _ []string) error {
	entries, err := loadSessionEntries(sessionListAll)
	if err != nil {
		return err
	}
	// appDir is the global ctxloom home (cwd-independent), used by --compact
	// below to detect missing essences.
	appDir := sessionAppDir()
	// --compact: compact every row whose essence is missing or stale so the
	// listing shows a title everywhere. Then re-read the index so the fresh
	// summaries/sizes render. Without the flag, title-less rows stay as-is.
	if sessionListCompact {
		compactMissingOrStale(cmd, entries, appDir)
		if refreshed, rErr := loadSessionEntries(sessionListAll); rErr == nil {
			entries = refreshed
		}
	}
	// The rows render from the one read model (operations.SessionView),
	// never from the entry: a lightweight projection — harp, single-line
	// summary, start, end, essence path — by default, or each session's
	// complete essence body under --full (see session_full.go);
	// emitSessionRows owns both shapes.
	return emitSessionRows(cmd, operations.ViewSessions(entries), sessionListFull)
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

// sessionAppDir resolves the global ctxloom home, degrading to "" when config
// cannot be loaded — the session index is cwd-independent, so a listing must
// still render for a project with a broken config.
func sessionAppDir() string {
	cfg, err := GetConfig()
	if err != nil {
		return ""
	}
	return cfg.GetAppDir()
}

// sessionEssence is the structured result of `session show`. In json mode a
// session that isn't compacted yet returns compacted:false with an empty essence
// (not an error), so a frontend can show a "not compacted yet" hint on hover
// without branching on an exit code.
type sessionEssence struct {
	Harp      string `json:"harp"`
	Compacted bool   `json:"compacted"`
	Essence   string `json:"essence"`
	// EssencePath is the absolute path to the essence file when compacted, "" (and
	// omitted) otherwise — so a client can open the real file rather than rebuild
	// the <output dir>/essence.md path itself.
	EssencePath string `json:"essence_path,omitempty"`
}

var sessionShowCmd = &cobra.Command{
	Use:     "show <session-name>",
	Short:   "Print the compacted summary of a named session",
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
	essence, compacted := readSessionEssence(afero.NewOsFs(), view)
	return emit(cmd, sessionEssence{Harp: harp, Compacted: compacted, Essence: essence, EssencePath: view.EssencePath}, func() error {
		if !compacted {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), uncompactedSessionHint(harp, view.NativeSession))
			return err
		}
		_, _ = cmd.OutOrStdout().Write([]byte(essence))
		return nil
	})
}

// The two hints `session show` prints in place of a summary that does not
// exist yet. Not having one is a state, not a failure, so the command exits 0
// in every format; the structured shape says compacted:false.
const (
	// sessionPendingHint: no backend session is bound, so there is nothing to
	// compact and no command to name.
	sessionPendingHint = "session %q is pending: no backend session is bound yet, so there is nothing to summarize"
	// sessionUncompactedHint: a bound session nobody has compacted yet.
	sessionUncompactedHint = "%q has no summary yet; run `ctxloom session compact %s` to write one"
)

// uncompactedSessionHint explains why there is nothing to print, telling the
// pending case (sessionID empty) from the not-yet-compacted one.
func uncompactedSessionHint(harp, sessionID string) string {
	if sessionID == "" {
		return fmt.Sprintf(sessionPendingHint, harp)
	}
	return fmt.Sprintf(sessionUncompactedHint, harp, harp)
}

// sessionRemoveCmd is the canonical spine's `remove` for the session noun,
// and the only verb here that destroys a session ENTIRELY.
//
// It removes all three artifacts, not just the index entry. Dropping the entry
// alone leaves the transcript and the essence on disk: a removal only from the
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
	Short: "Remove a session entirely: its index entry, its transcript and its summary",
	Long: `Removes all three of a session's own artifacts — the index entry, the
recorded transcript, and the compacted summary.

Authored files in the session directory are never destroyed; they are named in
the report and left where they are, so removing a session cannot take work
nobody filed with it.

Without --yes this only reports; nothing on disk or in the session index
changes, on a TTY or not.

A session that was never compacted is refused, because removing it would
destroy the only record of what happened. To do it deliberately, destroy the
transcript first with 'ctxloom session transcript purge <session-name> --uncompacted
--yes', then remove.

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

	// Files first, index entry second. PurgeSession stamps purged_at BEFORE
	// it unlinks anything, so a process that dies partway leaves a row that
	// reads "purged" over its now-missing transcript rather than one that
	// silently reconciles away. Dropping the entry first and then dying would
	// instead leave files nothing in the index accounts for.
	res, purgeErr := operations.PurgeSession(harp, operations.PurgeSessionRequest{
		Harp: harp,
		Populations: []operations.PurgePopulation{
			operations.PurgePopulationTranscript,
			operations.PurgePopulationArtifacts,
		},
		Apply: apply,
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

var sessionCompactCmd = &cobra.Command{
	Use:   "compact <session-name>",
	Short: "Compact a named session. Compaction is on-demand: nothing compacts a session automatically when it ends.",
	Long: `Looks up the session's bound session_id in its session record,
runs the compactor on that backend session, and writes a fresh essence.md
under the session directory. Errors if the session has no session_id bound
(the SessionStart bind hook records it for sessions launched via ctxloom run).`,
	Example: `  ctxloom session compact amber-swift-owl`,
	Args:    cobra.ExactArgs(1),
	RunE:    runSessionCompact,
}

// sessionCompactPromptDir backs --prompt-dir: it points compaction at prompt
// files on disk instead of the binary's embedded copies, so a prompt-evaluation
// harness can A/B variants against the same transcript without a rebuild.
var sessionCompactPromptDir string

func init() {
	sessionListCmd.Flags().BoolVar(&sessionListAll, "all", false, "Include sessions from every project (default: filter to cwd)")
	sessionListCmd.Flags().BoolVar(&sessionListCompact, "compact", false, "Compact sessions whose summary is missing or stale before listing, so every row shows a title")
	sessionRemoveCmd.Flags().BoolVarP(&sessionRemoveYes, "yes", "y", false,
		"apply the plan this invocation printed (default: report only)")
	sessionListCmd.Flags().BoolVar(&sessionListFull, "full", false, "Include each session's complete compacted summary body (text output pages through $PAGER on a terminal)")
	sessionCompactCmd.Flags().StringVar(&sessionCompactPromptDir, "prompt-dir", "",
		"Load compaction prompts from this directory instead of the built-in ones (expects <dir>/session-compact.md and <dir>/result-finding.md; a missing prompt is an error, not a fallback)")
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionEditCmd, sessionRemoveCmd, sessionCompactCmd, sessionApprovalsCmd)
	rootCmd.AddCommand(sessionCmd)
}

// runSessionCompact is the cobra RunE for `ctxloom session compact <harp>`.
// It composes:
//  1. Look up the harp in the session index.
//  2. Read its bound session_id (recorded forward by the SessionStart
//     bind hook, or by the compactor at compact time).
//  3. Run memory.Compactor against that session_id.
//  4. The compactor's existing write path stamps the harp dir
//     essence.md + the index summary.
//
// Sessions whose bind step never landed error here with a clear message.
// Pre-release sessions are unaffected by design; we don't backfill harp
// names for them.
func runSessionCompact(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	entry, err := operations.GetSession(harpName)
	if err != nil {
		return err
	}
	if entry == nil {
		return errNoSession(harpName)
	}

	enterSessionProjectDir(entry.ProjectDir, harpName)

	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.GetAppRoot() == "" {
		return fmt.Errorf("project root not found; run inside a project with .ctxloom/")
	}

	// Progress notes go to stderr as best-effort status.
	progress := errwriter.New(cmd.ErrOrStderr())
	if entry.SessionID != "" {
		progress.Printf("ctxloom: compacting %s (session_id=%s)...\n", harpName, entry.SessionID)
	} else {
		progress.Printf("ctxloom: compacting %s (by transcript path, no session_id bound)...\n", harpName)
	}

	// Ruled sub-choice #1 (adopt): `session compact`
	// now HEALS before compacting — a harp that was `/clear`ed has a
	// canonical transcript frozen at whatever moment a live /recover last
	// ran, and this command used to compact that frozen prefix and report
	// success. A one-shot CLI process genuinely cannot tell whether the
	// session it was pointed at is still growing elsewhere, so it heals
	// unconditionally every call — slower, and truthful.
	src, herr := operations.ResolveAndHeal(cmd.Context(), safefs.New(), App().Engines(), harpName)
	if herr != nil {
		return herr
	}
	if src.HealErr != nil {
		// The stored transcript may still be readable; a refresh failure
		// costs freshness, not the compact — warn and fall through to
		// compacting whatever is on disk rather than refusing outright.
		progress.Printf("ctxloom: could not refresh transcript for %s before compacting: %v\n", harpName, src.HealErr)
	}
	// Only the fallback direction carries information: when resolve produced no
	// entry, hand it the one already read above. entry is not read after this
	// point, so copying src.Entry back into it would be dead.
	if src.Entry == nil {
		src.Entry = entry
	}
	result, err := operations.CompactResolved(cmd.Context(), App().LaunchFacts(), src, cfg, operations.CompactOptions{
		Hosts:     internalRunHosts(),
		Progress:  progress,
		PromptDir: sessionCompactPromptDir,
	})
	if err != nil {
		return err
	}
	return reportCompactResult(cmd.OutOrStdout(), harpName, result)
}

// enterSessionProjectDir situates this one-shot process in the session's
// recorded project dir before config or the transcript is read. The backend
// transcript reader is self-situated — it derives the agent's store path (e.g.
// claude-code's ~/.claude/projects/<mangled-cwd>/) from the ambient cwd, not
// from the session id. So compacting a harp whose project dir differs from
// where we were launched (being run from a subdir or another project is
// enough) would look for the transcript under the wrong dir and fail with "no
// such file". chdir is safe: `session compact` is a short-lived process that
// exits after this call.
func enterSessionProjectDir(projectDir, harpName string) {
	if projectDir == "" {
		return
	}
	if cwd, _ := os.Getwd(); cwd == projectDir {
		return
	}
	if cerr := os.Chdir(projectDir); cerr != nil {
		// Don't hard-fail: the ambient cwd may still resolve (same project),
		// and a usable "couldn't compact" beats blocking the caller (CLAUDE.md).
		clidiag.Warn("ctxloom", "could not enter project dir %q for %s: %v", projectDir, harpName, cerr)
	}
}

// reportCompactResult prints the one-line compact summary to out.
func reportCompactResult(out io.Writer, harpName string, result *memory.CompactionResult) error {
	w := errwriter.New(out)
	reduced := ""
	if result.InputReduced {
		// Say so rather than reporting the token counts alone: the essence is
		// complete in shape either way, but early detail was thinned before the
		// model saw it, and that is not visible in the numbers.
		reduced = ", older content compressed to fit"
	}
	w.Printf("compacted %s in %s (%d → %d tokens%s)\nessence: %s\n",
		harpName, result.Duration, result.TotalTokensIn, result.TotalTokensOut, reduced, result.CompactedPath)
	return w.Err()
}
