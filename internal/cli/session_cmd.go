package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Bare `ctxloom session` lists the recorded sessions: the collection is the
// one thing the noun is about, and reading it touches nothing.
var sessionCmd = groupNodeDefault(&cobra.Command{
	Use:   "session",
	Short: "Browse and manage harp-named sessions",
	Long: `Read and manage the harp-keyed session index at
~/.ctxloom/sessions/index.yaml. Use to list/show/edit/remove
sessions without launching the LLM. Sessions appear here automatically
once ` + "`ctxloom run`" + ` has been used to launch a backend.`,
}, "list")

var (
	sessionListAll     bool
	sessionListDistill bool
	sessionListFull    bool
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List harp-named sessions (default: current project; --all for everything)",
	RunE:  runSessionList,
}

func runSessionList(cmd *cobra.Command, _ []string) error {
	entries, err := loadSessionEntries(sessionListAll)
	if err != nil {
		return err
	}
	// appDir is the global ctxloom home (cwd-independent), used by --distill
	// below to detect missing essences.
	appDir := sessionAppDir()
	// --distill: compact every row whose essence is missing or stale so the
	// listing shows a title everywhere. Then re-read the index so the fresh
	// summaries/sizes render. Without the flag, title-less rows stay as-is.
	if sessionListDistill {
		distillMissingOrStale(cmd, entries, appDir)
		if refreshed, rErr := loadSessionEntries(sessionListAll); rErr == nil {
			entries = refreshed
		}
	}
	// Default output shape (CLI-primary reorg plan, decision 13): a
	// lightweight projection — harp, single-line summary, start, end,
	// essence path — never the full Entry (session_id, transcript paths,
	// etc. stay off this wire; internal/sessions.Entry's own json posture
	// is untouched). --full swaps in each session's complete essence body
	// (see session_full.go); emitSessionRows owns both shapes.
	return emitSessionRows(cmd, entries, sessionListFull, appDir)
}

// loadSessionEntries reads the session index: every project's sessions when
// all is set (ListAllSessions — enriched + activity-sorted like the
// per-project path), the cwd's project otherwise.
//
// It NORMALIZES nil to an empty slice. That is load-bearing rather than
// cosmetic: `session list --format json` renders this value directly, and a nil
// slice marshals to `null` where an empty one marshals to `[]` — the difference
// between "no sessions" and "the field is missing" for a consumer. Called twice
// per --distill run (before and after distillation), so the normalization has
// to live here, not at one call site.
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
	if entries == nil {
		entries = []sessions.Entry{}
	}
	return entries, nil
}

// sessionAppDir resolves the global ctxloom home, degrading to "" when config
// cannot be loaded — the session index is cwd-independent, so a listing must
// still render for a project with a broken config.
func sessionAppDir() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	return cfg.GetAppDir()
}

// sessionEssence is the structured result of `session show`. In json mode a
// session that isn't distilled yet returns distilled:false with an empty essence
// (not an error), so a frontend can show a "not distilled yet" hint on hover
// without branching on an exit code.
type sessionEssence struct {
	Harp      string `json:"harp"`
	Distilled bool   `json:"distilled"`
	Essence   string `json:"essence"`
	// EssencePath is the absolute path to the essence file when distilled, "" (and
	// omitted) otherwise — so a client can open the real file rather than rebuild
	// the ~/.ctxloom/sessions/<harp>/essence.md path itself.
	EssencePath string `json:"essence_path,omitempty"`
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <harp-name>",
	Short: "Print the distilled essence of a harp-named session",
	Args:  cobra.ExactArgs(1),
	RunE:  runSessionShow,
}

func runSessionShow(cmd *cobra.Command, args []string) error {
	harp := args[0]
	entry, err := operations.GetSession(harp)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("harp not found: %q", harp)
	}
	essence, distilled := readSessionEssence(harp, entry)
	essencePath := ""
	if distilled {
		essencePath, _ = operations.SessionEssenceInfo(harp, entry, sessionAppDir())
	}
	return emit(cmd, sessionEssence{Harp: harp, Distilled: distilled, Essence: essence, EssencePath: essencePath}, func() error {
		if !distilled {
			return undistilledSessionError(harp, entry.SessionID)
		}
		_, _ = cmd.OutOrStdout().Write([]byte(essence))
		return nil
	})
}

// undistilledSessionError explains why there is nothing to print, telling the
// two cases apart: a harp with no backend session bound yet is PENDING (there
// is nothing to distill), while a bound one just has not been compacted and
// names the command that would do it. Text-format only — the structured shape
// reports distilled:false rather than erroring, so a frontend can show a hint
// on hover without branching on an exit code.
func undistilledSessionError(harp, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("harp %q is pending (no backend session ID bound yet)", harp)
	}
	return fmt.Errorf("no essence for %q (run `ctxloom session distill %s` to compact this session first)", harp, harp)
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
	Use:   "remove <harp-name>",
	Short: "Remove a session entirely: its index entry, its transcript and its essence",
	Long: `Removes all three of a session's own artifacts — the index entry, the
recorded transcript, and the distilled essence.

Authored files in the harp directory are never destroyed; they are named in
the report and left where they are, so removing a session cannot take work
nobody filed with it.

Without --yes this only reports; nothing on disk or in the session index
changes, on a TTY or not.

A session that was never distilled is refused, because removing it would
destroy the only record of what happened. To do it deliberately, destroy the
transcript first with 'ctxloom session transcript purge <harp> --undistilled
--yes', then remove.

To empty a session but keep it listed, use 'ctxloom session purge'.`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionRemove,
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
	ew := iox.NewErrWriter(w)
	if out.IndexEntryRemoved {
		ew.Printf("index entry %s: removed\n", out.Harp)
	} else {
		ew.Printf("index entry %s: would be removed\n", out.Harp)
	}
	return ew.Err()
}

var sessionDistillCmd = &cobra.Command{
	Use:   "distill <harp-name>",
	Short: "Distill a session by harp name. Distillation is on-demand: nothing distills a session automatically when it ends.",
	Long: `Looks up the harp's bound session_id in ~/.ctxloom/sessions/index.yaml,
runs the compactor on that backend session, and writes a fresh essence.md
under the harp directory. Errors if the harp has no session_id bound
(the SessionStart bind hook records it for sessions launched via ctxloom run).`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionDistill,
}

func init() {
	sessionListCmd.Flags().BoolVar(&sessionListAll, "all", false, "Include sessions from every project (default: filter to cwd)")
	sessionListCmd.Flags().BoolVar(&sessionListDistill, "distill", false, "Distill sessions whose essence is missing or stale before listing, so every row shows a title")
	sessionRemoveCmd.Flags().BoolVarP(&sessionRemoveYes, "yes", "y", false,
		"apply the plan this invocation printed (default: report only)")
	sessionListCmd.Flags().BoolVar(&sessionListFull, "full", false, "Include each session's complete distilled essence body (text/markdown output pages through $PAGER on a terminal)")
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionEditCmd, sessionRemoveCmd, sessionDistillCmd)
	rootCmd.AddCommand(sessionCmd)
}

// runSessionDistill is the cobra RunE for `ctxloom session distill <harp>`.
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
func runSessionDistill(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	entry, err := operations.GetSession(harpName)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("harp not found: %q", harpName)
	}

	// Situate this one-shot process in the session's recorded project dir before
	// reading config or the transcript. The backend transcript reader is
	// self-situated — it derives the agent's store path (e.g. claude-code's
	// ~/.claude/projects/<mangled-cwd>/) from the ambient cwd, not from the
	// session id. So distilling a harp whose project dir differs from where we
	// were launched (the resume picker's `d<N>` shells out inheriting `ctxloom
	// run`'s cwd; a subdir or another project is enough) would look for the
	// transcript under the wrong dir and fail with "no such file". chdir is safe:
	// `session distill` is a short-lived process that exits after this call.
	if entry.ProjectDir != "" {
		if cwd, _ := os.Getwd(); cwd != entry.ProjectDir {
			if cerr := os.Chdir(entry.ProjectDir); cerr != nil {
				// Don't hard-fail: the ambient cwd may still resolve (same project),
				// and a usable "couldn't distill" beats blocking the picker (CLAUDE.md).
				clidiag.Warn("ctxloom", "could not enter project dir %q for %s: %v", entry.ProjectDir, harpName, cerr)
			}
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.GetAppRoot() == "" {
		return fmt.Errorf("project root not found; run inside a project with .ctxloom/")
	}

	// Progress notes go to stderr as best-effort status.
	progress := iox.NewErrWriter(cmd.ErrOrStderr())
	if entry.SessionID != "" {
		progress.Printf("ctxloom: distilling %s (session_id=%s)...\n", harpName, entry.SessionID)
	} else {
		progress.Printf("ctxloom: distilling %s (by transcript path, no session_id bound)...\n", harpName)
	}

	// Ruled sub-choice #1 (adopt): `session distill`
	// now HEALS before distilling — a harp that was `/clear`ed has a
	// canonical transcript frozen at whatever moment a live /recover last
	// ran, and this command used to distill that frozen prefix and report
	// success. A one-shot CLI process genuinely cannot tell whether the
	// session it was pointed at is still growing elsewhere, so it heals
	// unconditionally (LivenessUnknown) every call — slower, and truthful.
	src, herr := operations.ResolveAndHeal(cmd.Context(), harpName, operations.LivenessUnknown)
	if herr != nil {
		return herr
	}
	if src.HealErr != nil {
		// The stored transcript may still be readable; a refresh failure
		// costs freshness, not the distill — warn and fall through to
		// distilling whatever is on disk rather than refusing outright.
		progress.Printf("ctxloom: could not refresh transcript for %s before distilling: %v\n", harpName, src.HealErr)
	}
	// Only the fallback direction carries information: when resolve produced no
	// entry, hand it the one already read above. The mirrored assignment that
	// used to sit here (entry = src.Entry) wrote a local nothing reads again —
	// entry is last read before this block — so it was dead, not symmetric.
	if src.Entry == nil {
		src.Entry = entry
	}
	result, err := operations.DistillEntry(cmd.Context(), src, cfg, "", progress)
	if err != nil {
		return err
	}
	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("distilled %s in %s (%d chunks, %d → %d tokens)\nessence: %s\n",
		harpName, result.Duration, result.ChunksCreated, result.TotalTokensIn, result.TotalTokensOut, result.DistilledPath)
	return w.Err()
}
