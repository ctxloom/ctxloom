package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Browse and manage harp-named sessions",
	Long: `Read and manage the harp-keyed session index at
~/.ctxloom/sessions/index.yaml. Use to list/show/rename/forget
sessions without launching the LLM. Sessions appear here automatically
once ` + "`ctxloom run`" + ` has been used to launch a backend.`,
}

var (
	sessionListAll     bool
	sessionListDistill bool
	sessionListFull    bool
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List harp-named sessions (default: current project; --all for everything)",
	RunE: func(cmd *cobra.Command, args []string) error {
		// A closure so --distill can re-read the index after writing fresh
		// essences, picking up the new summaries and SourceSize. --all spans
		// every project (ListAllSessions: enriched + activity-sorted like the
		// per-project path); the default filters to the cwd's project.
		loadEntries := func() ([]sessions.Entry, error) {
			if sessionListAll {
				return operations.ListAllSessions()
			}
			wd, _ := os.Getwd()
			return operations.ListSessionsForProject(wd)
		}
		entries, err := loadEntries()
		if err != nil {
			return err
		}
		if entries == nil {
			entries = []sessions.Entry{}
		}
		// appDir is the global ctxloom home (cwd-independent), used by --distill
		// below to detect missing essences.
		appDir := ""
		if cfg, cErr := config.Load(); cErr == nil {
			appDir = cfg.GetAppDir()
		}
		// --distill: compact every row whose essence is missing or stale so the
		// listing shows a title everywhere. Then re-read the index so the fresh
		// summaries/sizes render. Without the flag, title-less rows stay as-is.
		if sessionListDistill {
			distillMissingOrStale(cmd, entries, appDir)
			if refreshed, rErr := loadEntries(); rErr == nil {
				entries = refreshed
			}
			if entries == nil {
				entries = []sessions.Entry{}
			}
		}
		// Default output shape (CLI-primary reorg plan, decision 13): a
		// lightweight projection — harp, single-line summary, start, end,
		// essence path — never the full Entry (session_id, transcript paths,
		// etc. stay off this wire; internal/sessions.Entry's own json posture
		// is untouched). --full swaps in each session's complete essence body
		// (see session_full.go); emitSessionRows owns both shapes.
		return emitSessionRows(cmd, entries, sessionListFull, appDir)
	},
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
	RunE: func(cmd *cobra.Command, args []string) error {
		entry, err := operations.GetSession(args[0])
		if err != nil {
			return err
		}
		if entry == nil {
			return fmt.Errorf("harp not found: %q", args[0])
		}
		essence, distilled := readSessionEssence(args[0], entry)
		essencePath := ""
		if distilled {
			appDir := ""
			if cfg, cErr := config.Load(); cErr == nil {
				appDir = cfg.GetAppDir()
			}
			essencePath, _ = sessionEssenceInfo(args[0], entry, appDir)
		}
		return emit(cmd, sessionEssence{Harp: args[0], Distilled: distilled, Essence: essence, EssencePath: essencePath}, func() error {
			if !distilled {
				if entry.SessionID == "" {
					return fmt.Errorf("harp %q is pending (no backend session ID bound yet)", args[0])
				}
				return fmt.Errorf("no essence for %q (run `ctxloom session distill %s` to compact this session first)", args[0], args[0])
			}
			_, _ = cmd.OutOrStdout().Write([]byte(essence))
			return nil
		})
	},
}

var sessionRenameCmd = &cobra.Command{
	Use:   "rename <old-harp> <new-harp>",
	Short: "Rename a harp entry. The backend transcript is unaffected.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := operations.RenameSession(args[0], args[1]); err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("renamed %s → %s\n", args[0], args[1])
		return w.Err()
	},
}

var sessionForgetCmd = &cobra.Command{
	Use:   "forget <harp-name>",
	Short: "Drop a harp entry from the index. Transcript and essence files stay on disk.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := operations.ForgetSession(args[0]); err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("forgot %s\n", args[0])
		return w.Err()
	},
}

var sessionDistillCmd = &cobra.Command{
	Use:   "distill <harp-name>",
	Short: "Force-distill a session by harp name. Useful for sessions that ended before auto-compact ran.",
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
	sessionListCmd.Flags().BoolVar(&sessionListFull, "full", false, "Include each session's complete distilled essence body (text/markdown output pages through $PAGER on a terminal)")
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionRenameCmd, sessionForgetCmd, sessionDistillCmd, sessionWatchCmd)
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
	result, err := compactEntry(cmd.Context(), entry, cfg, "", progress)
	if err != nil {
		return err
	}
	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("distilled %s in %s (%d chunks, %d → %d tokens)\nessence: %s\n",
		harpName, result.Duration, result.ChunksCreated, result.TotalTokensIn, result.TotalTokensOut, result.DistilledPath)
	return w.Err()
}
