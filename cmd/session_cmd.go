package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/iox"
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
			w := iox.NewErrWriter(cmd.OutOrStdout())
			w.Println("(no sessions)")
			return w.Err()
		}
		return renderSessionTable(cmd.OutOrStdout(), entries)
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
		mgr, err := sessions.Open("")
		if err != nil {
			return err
		}
		if err := mgr.Forget(args[0]); err != nil {
			return err
		}
		w := iox.NewErrWriter(cmd.OutOrStdout())
		w.Printf("forgot %s\n", args[0])
		return w.Err()
	},
}

// sessionBindCmd is the SessionStart hook target. It's the primary
// path for recording harp → session_id mapping in the index: Claude
// Code (and other backends with SessionStart hooks) fire this exactly
// once per session, with the backend's session ID and transcript path
// already in the payload. The compactor and the transcript-scan
// fallback in `session distill` remain as belt-and-suspenders.
var sessionBindCmd = &cobra.Command{
	Use:    "bind",
	Short:  "Bind the current backend session to the active harp (internal — used by the SessionStart hook)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := sessions.Open("")
		if err != nil {
			return fmt.Errorf("session index: %w", err)
		}
		return bindSessionFromPayload(cmd.InOrStdin(), os.Getenv("CTXLOOM_SESSION_HARP"), mgr)
	},
}

// bindSessionFromPayload reads a SessionStart hook payload from in,
// extracts session_id / transcript_path, and binds them to harp in the
// given Manager. Idempotent: re-running with the same payload is a
// no-op. Malformed payloads silently succeed (a hook must never fail
// the host backend's startup over a bad message).
//
// Extracted from sessionBindCmd's RunE so the binding logic is testable
// without spinning up cobra or the real os.Stdin.
func bindSessionFromPayload(in io.Reader, harp string, mgr *sessions.Manager) error {
	if harp == "" || mgr == nil {
		return nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	var payload struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil // malformed payload — no-op
	}
	if payload.SessionID == "" {
		return nil
	}
	entry, _ := mgr.Find(harp)
	if entry == nil || entry.SessionID != "" {
		return nil // not in index or already bound
	}
	if err := mgr.BindSession(harp, payload.SessionID, payload.TranscriptPath); err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	return nil
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
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionRenameCmd, sessionForgetCmd, sessionDistillCmd, sessionBindCmd)
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

	// If no session_id was forward-bound, fall back to scanning the
	// backend's transcripts for the harp name. The harp appears in
	// every session's MCP Instructions block (sent on initialize and
	// recorded in the jsonl as part of the assistant's context), plus
	// in any tool output that echoed it (list_sessions, task_list,
	// etc.). Content match is exact — no timing, no clock skew.
	// Progress notes go to stderr as best-effort status. A failed write
	// here must not abort an otherwise-successful distillation, so the
	// captured error is intentionally left unchecked.
	progress := iox.NewErrWriter(cmd.ErrOrStderr())

	sessionID := entry.SessionID
	if sessionID == "" {
		found, err := discoverSessionByHarpName(backendName, entry.ProjectDir, harpName)
		if err != nil {
			return fmt.Errorf("harp %q has no session_id bound and transcript scan failed: %w", harpName, err)
		}
		sessionID = found
		// Persist the discovery so future distill calls skip the scan.
		if err := mgr.BindSession(harpName, sessionID, ""); err == nil {
			progress.Printf("ctxloom: bound %s → %s via transcript scan\n", harpName, sessionID)
		}
	}

	progress.Printf("ctxloom: distilling %s (session_id=%s)...\n", harpName, sessionID)
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
	w := iox.NewErrWriter(cmd.OutOrStdout())
	w.Printf("distilled %s in %s (%d chunks, %d → %d tokens)\nessence: %s\n",
		harpName, result.Duration, result.ChunksCreated, result.TotalTokensIn, result.TotalTokensOut, result.DistilledPath)
	return w.Err()
}

// renderSessionTable writes a tab-aligned listing of session entries to w.
// Entries are assumed pre-sorted by the caller; this function only formats.
func renderSessionTable(out io.Writer, entries []sessions.Entry) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	w := iox.NewErrWriter(tw)
	w.Println("HARP\tSTARTED\tSUMMARY")
	for _, e := range entries {
		summary := e.Summary
		if summary == "" {
			summary = "(no summary)"
		}
		w.Printf("%s\t%s\t%s\n",
			e.HarpName,
			e.StartedAt.Local().Format("2006-01-02 15:04"),
			summary,
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return w.Err()
}

// discoverSessionByHarpName scans every backend transcript for the
// given project, looking for the harp name string in entry content,
// tool input/output, or tool name. Used as last-resort rescue when no
// forward-bind landed.
//
// The harp name is guaranteed to appear in any session ctxloom
// launched: ServerOptions.Instructions on the MCP initialize response
// includes "Your session is named `<harp>`. …", and Claude Code
// records the resolved system context in the jsonl. Tool outputs from
// list_sessions / task_list / etc. echo harp IDs too, so even sessions
// where the system context was somehow lost can match on later turns.
//
// Per project: typically <100 sessions, each <500 entries. Linear scan
// is fine. We stop at the first match because harps are unique by
// construction.
func discoverSessionByHarpName(backendName, workDir, harpName string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("workDir required for transcript scan")
	}
	if harpName == "" {
		return "", fmt.Errorf("harpName required")
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
	for _, m := range metas {
		session, err := history.GetSession(workDir, m.ID)
		if err != nil || session == nil {
			continue
		}
		if sessionContainsHarpName(session, harpName) {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("no backend session in %q contains harp name %q", workDir, harpName)
}

// sessionContainsHarpName scans every entry in a session for the harp
// name string. Extracted as a top-level pure function so the matching
// logic is unit-testable without standing up a backend registry.
func sessionContainsHarpName(session *backends.Session, harpName string) bool {
	if session == nil || harpName == "" {
		return false
	}
	for _, e := range session.Entries {
		if entryMentionsHarp(e, harpName) {
			return true
		}
	}
	return false
}

func entryMentionsHarp(e backends.SessionEntry, harpName string) bool {
	if strings.Contains(e.Content, harpName) {
		return true
	}
	if strings.Contains(e.ToolOutput, harpName) {
		return true
	}
	if strings.Contains(e.ToolName, harpName) {
		return true
	}
	if len(e.ToolInput) > 0 && strings.Contains(string(e.ToolInput), harpName) {
		return true
	}
	return false
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
