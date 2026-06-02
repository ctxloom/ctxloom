package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/harpmarker"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/paths"
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
		path := filepath.Join(paths.ProjectSessionsDir(cfg.AppDir), entry.SessionID+".md")
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
	p, err := paths.HarpEssencePath(harpName)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
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

// sessionBindCmd is the SessionStart hook target and the sole path for
// recording the harp → session_id mapping in the index: Claude Code (and
// other backends with SessionStart hooks) fire this exactly once per
// session, with the backend's session ID and transcript path already in
// the documented hook payload. The compactor also forward-binds at
// compact time as a backstop.
var sessionBindCmd = &cobra.Command{
	Use:    "bind",
	Short:  "Bind the current backend session to the active harp (internal — used by the SessionStart hook)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		harp := os.Getenv("CTXLOOM_SESSION_HARP")
		// Read the hook payload once: the marker doesn't need it, the bind does.
		raw, _ := io.ReadAll(cmd.InOrStdin())
		// Emit the deterministic harp self-id marker as SessionStart context so
		// the transcript carries a greppable owner tag, independent of the index,
		// the binding, or PID bookkeeping. This is the SessionStart hook installed
		// for every ctxloom session, so it identifies the harp even when no
		// project context (inject-context) is configured. Best-effort: a hook must
		// never fail the host backend's startup, so failures past this point only
		// skip the index bind — the marker is already on stdout.
		emitHarpMarker(cmd.OutOrStdout(), harp)
		mgr, err := sessions.Open("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: session index open failed: %v\n", err)
			return nil
		}
		return bindSessionFromPayload(bytes.NewReader(raw), harp, mgr)
	},
}

// emitHarpMarker writes the harp self-id marker to w as a SessionStart hook
// output (the same envelope inject-context uses), so the backend injects it into
// the session and it lands in the transcript. No-op when harp is empty.
func emitHarpMarker(w io.Writer, harp string) {
	marker := harpmarker.Format(harp)
	if marker == "" {
		return
	}
	out := HookOutput{HookSpecificOutput: &HookSpecificOutput{
		HookEventName:     "SessionStart",
		AdditionalContext: marker,
	}}
	// Best-effort: a marshal/write failure must not fail the bind hook.
	if b, err := json.Marshal(out); err == nil {
		_, _ = w.Write(append(b, '\n'))
	}
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
(the SessionStart bind hook records it for sessions launched via ctxloom run).`,
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
		backendName = cfg.GetDefaultLLM()
	}

	// session_id is recorded forward by the `ctxloom session bind`
	// SessionStart hook (see sessionBindCmd), which reads it straight from
	// the backend's documented hook payload. A harp with no bound ID never
	// started a backend session under that hook — there is nothing to
	// distill, so fail clearly rather than guess at a binding.
	sessionID := entry.SessionID
	if sessionID == "" {
		return fmt.Errorf("harp %q has no session_id bound; nothing to distill (the SessionStart bind hook records the ID for sessions launched via ctxloom run)", harpName)
	}

	// Progress notes go to stderr as best-effort status.
	progress := iox.NewErrWriter(cmd.ErrOrStderr())
	progress.Printf("ctxloom: distilling %s (session_id=%s)...\n", harpName, sessionID)
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:    cfg.GetCompactionLLM(),
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

