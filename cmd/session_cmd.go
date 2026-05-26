package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
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

	// If no session_id was forward-bound, fall back to a time-window
	// lookup against the backend's transcript list. The harp index
	// always records started_at (and, since MarkEnded on shutdown,
	// ended_at), so we have a small interval to match against the
	// backend's SessionMeta.StartTime values.
	sessionID := entry.SessionID
	if sessionID == "" {
		found, err := discoverSessionByTime(backendName, entry.ProjectDir, entry.StartedAt, entry.EndedAt)
		if err != nil {
			return fmt.Errorf("harp %q has no session_id bound and time-window discovery failed: %w", harpName, err)
		}
		sessionID = found
		// Persist the discovery so future distill calls skip the lookup.
		if err := mgr.BindSession(harpName, sessionID, ""); err == nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "ctxloom: bound %s → %s via time-window match\n", harpName, sessionID)
		}
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "ctxloom: distilling %s (session_id=%s)...\n", harpName, sessionID)
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    cfg.GetCompactionPlugin(),
		Model:     cfg.GetCompactionModel(),
		Backend:   backendName,
		ChunkSize: cfg.GetCompactionChunkSize(),
		SessionID: sessionID,
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

// discoverSessionByTime scans the backend's session list for the entry
// whose StartTime best matches the harp's recorded started_at (and, when
// available, ended_at). Used as a fallback when forward-bind never
// recorded a session_id — typically a session that ended without any
// MCP method call (no bind middleware trigger, no compact_session).
//
// The match algorithm is intentionally simple: closest StartTime within
// a 5-minute window, tie-broken by lowest |start diff| + |end diff|.
// Two ctxloom-launched LLM sessions starting within 5 minutes of each
// other against the same backend would be ambiguous, but Claude's UUID
// transcripts are project-scoped (ListSessions is workDir-filtered) so
// practical collisions are rare.
func discoverSessionByTime(backendName, workDir string, harpStarted time.Time, harpEnded *time.Time) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("workDir required for time-window discovery")
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return "", fmt.Errorf("unknown backend: %s", backendName)
	}
	history := backend.History()
	if history == nil {
		return "", fmt.Errorf("backend %q does not support session history", backendName)
	}
	metas, err := history.ListSessions(workDir)
	if err != nil {
		return "", fmt.Errorf("list backend sessions: %w", err)
	}
	bestID := selectClosestSession(metas, harpStarted, harpEnded, sessionDiscoveryWindow)
	if bestID == "" {
		return "", fmt.Errorf("no backend session within %v of harp start time %s", sessionDiscoveryWindow, harpStarted.Format(time.RFC3339))
	}
	return bestID, nil
}

// sessionDiscoveryWindow caps how far the time-window matcher will look
// for a backend session matching a harp's recorded start time. 5 minutes
// is generous — Claude's session UUIDs usually land within seconds of
// ctxloom run minting the harp, but slow startups (cold cache, large
// initial context) can drift past a minute. Tightening this risks
// missing valid matches; widening it risks cross-matching unrelated
// sessions in heavy-use projects.
const sessionDiscoveryWindow = 5 * time.Minute

// selectClosestSession is the pure matching function under
// discoverSessionByTime. Returns the SessionMeta.ID whose StartTime
// best matches harpStarted within window. Tie-broken by adding the
// |EndTime - harpEnded| delta when both endpoints are known. Returns
// the empty string when no candidate falls inside the window.
//
// Extracted as a top-level function so we can unit-test the matching
// logic without needing to spin up a backend registry.
func selectClosestSession(metas []backends.SessionMeta, harpStarted time.Time, harpEnded *time.Time, window time.Duration) string {
	bestID := ""
	bestScore := time.Duration(-1)
	for _, m := range metas {
		startDiff := absDuration(m.StartTime.Sub(harpStarted))
		if startDiff > window {
			continue
		}
		score := startDiff
		if harpEnded != nil && !m.EndTime.IsZero() {
			score += absDuration(m.EndTime.Sub(*harpEnded))
		}
		if bestScore < 0 || score < bestScore {
			bestScore = score
			bestID = m.ID
		}
	}
	return bestID
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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
