package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/sessions"
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
	sessionListAll bool
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List harp-named sessions (default: current project; --all for everything)",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := sessions.Open("")
		if err != nil {
			return err
		}
		idx, err := mgr.Load()
		if err != nil {
			return err
		}
		entries := idx.Sessions
		if !sessionListAll {
			wd, _ := os.Getwd()
			filtered := entries[:0]
			for _, e := range entries {
				if e.ProjectDir == wd {
					filtered = append(filtered, e)
				}
			}
			entries = filtered
		}
		if len(entries) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "(no sessions)")
			return nil
		}
		renderSessionTable(cmd.OutOrStdout(), entries)
		return nil
	},
}

var sessionShowCmd = &cobra.Command{
	Use:   "show <harp-name>",
	Short: "Print the distilled essence of a harp-named session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := sessions.Open("")
		if err != nil {
			return err
		}
		entry, err := mgr.Find(args[0])
		if err != nil {
			return err
		}
		if entry == nil {
			return fmt.Errorf("harp not found: %q", args[0])
		}
		if entry.SessionID == "" {
			return fmt.Errorf("harp %q is pending (no backend session ID bound yet)", args[0])
		}
		// Prefer the Phase 3.6 harp-dir layout (~/.ctxloom/sessions/<harp>/
		// essence.md); fall back to the legacy <sessionsDir>/<sessionID>.md
		// path for sessions distilled before 3.6 landed.
		if data, err := readHarpEssence(args[0]); err == nil {
			_, _ = cmd.OutOrStdout().Write(data)
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		path := filepath.Join(resolveSessionsDir(cfg), entry.SessionID+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read essence: %w (run `ctxloom session distill %s` to compact this session first)", err, args[0])
		}
		_, _ = cmd.OutOrStdout().Write(data)
		return nil
	},
}

// readHarpEssence returns the bytes of ~/.ctxloom/sessions/<harp>/essence.md.
// Errors when home can't be resolved or the file is missing; callers fall
// back to the legacy layout in either case.
func readHarpEssence(harpName string) ([]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(home, ".ctxloom", "sessions", harpName, "essence.md"))
}

var sessionRenameCmd = &cobra.Command{
	Use:   "rename <old-harp> <new-harp>",
	Short: "Rename a harp entry. The backend transcript is unaffected.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := sessions.Open("")
		if err != nil {
			return err
		}
		if err := mgr.Rename(args[0], args[1]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "renamed %s → %s\n", args[0], args[1])
		return nil
	},
}

var sessionForgetCmd = &cobra.Command{
	Use:   "forget <harp-name>",
	Short: "Drop a harp entry from the index. Transcript and essence files stay on disk.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := sessions.Open("")
		if err != nil {
			return err
		}
		if err := mgr.Forget(args[0]); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "forgot %s\n", args[0])
		return nil
	},
}

var sessionDistillCmd = &cobra.Command{
	Use:   "distill <harp-name>",
	Short: "Force-distill a session by harp name. Useful for sessions that ended before auto-compact ran.",
	Long: `Looks up the harp's bound session_id in ~/.ctxloom/sessions/index.yaml,
runs the compactor on that backend session, and writes a fresh essence.md
under the harp directory. Errors if the harp has no session_id bound
yet (sessions need at least one MCP tool call for the bind middleware
to forward-record the ID).`,
	Args: cobra.ExactArgs(1),
	RunE: runSessionDistill,
}

func init() {
	sessionListCmd.Flags().BoolVar(&sessionListAll, "all", false, "Include sessions from every project (default: filter to cwd)")
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionRenameCmd, sessionForgetCmd, sessionDistillCmd)
	rootCmd.AddCommand(sessionCmd)
}

// runSessionDistill is the cobra RunE for `ctxloom session distill <harp>`.
// It composes:
//   1. Look up the harp in the session index.
//   2. Read its bound session_id (set forward at compact time and/or
//      by the MCP session-bind middleware on first tool call).
//   3. Run memory.Compactor against that session_id.
//   4. The compactor's existing write path stamps the harp dir
//      essence.md + the index summary.
//
// Sessions whose bind step never landed (the bind middleware would have
// needed at least one MCP method call) error here with a clear message.
// Pre-release sessions are unaffected by design; we don't backfill harp
// names for them.
func runSessionDistill(cmd *cobra.Command, args []string) error {
	harpName := args[0]
	mgr, err := sessions.Open("")
	if err != nil {
		return fmt.Errorf("session index: %w", err)
	}
	entry, err := mgr.Find(harpName)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("harp not found: %q", harpName)
	}
	if entry.SessionID == "" {
		return fmt.Errorf("harp %q has no session_id bound — the bind middleware needs at least one MCP tool call during the session's lifetime to record it. This session is unrecoverable for distillation.", harpName)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.AppRoot == "" {
		return fmt.Errorf("project root not found; run inside a project with .ctxloom/")
	}

	backendName := entry.Backend
	if backendName == "" {
		backendName = cfg.GetDefaultLLMPlugin()
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "ctxloom: distilling %s (session_id=%s)...\n", harpName, entry.SessionID)
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    cfg.GetCompactionPlugin(),
		Model:     cfg.GetCompactionModel(),
		Backend:   backendName,
		ChunkSize: cfg.GetCompactionChunkSize(),
		SessionID: entry.SessionID,
		WorkDir:   entry.ProjectDir,
		HarpName:  harpName,
	})
	if err != nil {
		return fmt.Errorf("create compactor: %w", err)
	}
	result, err := compactor.Compact(cmd.Context())
	if err != nil {
		return fmt.Errorf("distillation failed: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "distilled %s in %s (%d chunks, %d → %d tokens)\nessence: %s\n",
		harpName, result.Duration, result.ChunksCreated, result.TotalTokensIn, result.TotalTokensOut, result.DistilledPath)
	return nil
}

// renderSessionTable writes a tab-aligned listing of session entries to w.
// Entries are assumed pre-sorted by the caller; this function only formats.
func renderSessionTable(w io.Writer, entries []sessions.Entry) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "HARP\tSTARTED\tSUMMARY")
	for _, e := range entries {
		summary := e.Summary
		if summary == "" {
			summary = "(no summary)"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n",
			e.HarpName,
			e.StartedAt.Local().Format("2006-01-02 15:04"),
			summary,
		)
	}
	_ = tw.Flush()
}

// resolveSessionsDir mirrors the sessions-dir resolution from
// cmd/mcp_tools_memory.go so CLI lookups land at the same path the
// MCP server writes to. Kept local to avoid coupling cmd files.
func resolveSessionsDir(cfg *config.Config) string {
	if cfg.AppDir != "" {
		return filepath.Join(cfg.AppDir, "sessions")
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".ctxloom", "sessions")
	}
	return filepath.Join(".ctxloom", "sessions")
}
