package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/harpmarker"
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
	sessionListAll bool
)

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List harp-named sessions (default: current project; --all for everything)",
	RunE: func(cmd *cobra.Command, args []string) error {
		var entries []sessions.Entry
		var err error
		if sessionListAll {
			entries, err = operations.ListSessions()
		} else {
			wd, _ := os.Getwd()
			entries, err = operations.ListSessionsForProject(wd)
		}
		if err != nil {
			return err
		}
		if entries == nil {
			entries = []sessions.Entry{}
		}
		// Enrich each row with its essence presence + path (computed, not stored),
		// so a client reads `distilled`/`essence_path` straight from the listing
		// instead of re-deriving them or rebuilding the ~/.ctxloom path itself.
		appDir := ""
		if cfg, cErr := config.Load(); cErr == nil {
			appDir = cfg.AppDir
		}
		for i := range entries {
			path, distilled := sessionEssenceInfo(entries[i].HarpName, &entries[i], appDir)
			entries[i].Distilled = distilled
			entries[i].EssencePath = path
		}
		return emit(cmd, entries, func() error {
			if len(entries) == 0 {
				w := iox.NewErrWriter(cmd.OutOrStdout())
				w.Println("(no sessions)")
				return w.Err()
			}
			return renderSessionTable(cmd.OutOrStdout(), entries)
		})
	},
}

// SessionEssence is the structured result of `session show`. In json mode a
// session that isn't distilled yet returns distilled:false with an empty essence
// (not an error), so a frontend can show a "not distilled yet" hint on hover
// without branching on an exit code.
type SessionEssence struct {
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
				appDir = cfg.AppDir
			}
			essencePath, _ = sessionEssenceInfo(args[0], entry, appDir)
		}
		return emit(cmd, SessionEssence{Harp: args[0], Distilled: distilled, Essence: essence, EssencePath: essencePath}, func() error {
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

// readSessionEssence returns a session's distilled essence and whether one was
// found. It prefers the harp-dir layout (~/.ctxloom/sessions/<harp>/essence.md)
// and falls back to the legacy <sessionsDir>/<sessionID>.md path. A pending
// session (no bound id) or a missing essence yields ("", false) rather than an
// error, so callers can present "not distilled yet" uniformly.
func readSessionEssence(harp string, entry *sessions.Entry) (string, bool) {
	if data, err := readHarpEssence(harp); err == nil {
		return string(data), true
	}
	if entry.SessionID == "" {
		return "", false
	}
	cfg, err := config.Load()
	if err != nil {
		return "", false
	}
	path := filepath.Join(paths.ProjectSessionsDir(cfg.AppDir), entry.SessionID+".md")
	if data, err := os.ReadFile(path); err == nil {
		return string(data), true
	}
	return "", false
}

// sessionEssenceInfo resolves a session's essence file path and whether it
// exists (i.e. the session is distilled), WITHOUT reading the file — so the
// listing can report essence_path/distilled cheaply for every row. It mirrors
// readSessionEssence's lookup order: the harp-dir layout first
// (~/.ctxloom/sessions/<harp>/essence.md, which needs no config), then the
// legacy <sessionsDir>/<sessionID>.md (which needs appDir; pass "" to skip it).
func sessionEssenceInfo(harp string, entry *sessions.Entry, appDir string) (string, bool) {
	if p, err := paths.HarpEssencePath(harp); err == nil && fileExists(p) {
		return p, true
	}
	if entry.SessionID != "" && appDir != "" {
		p := filepath.Join(paths.ProjectSessionsDir(appDir), entry.SessionID+".md")
		if fileExists(p) {
			return p, true
		}
	}
	return "", false
}

// fileExists reports whether p is an existing regular file (not a directory).
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
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

// sessionBindCmd is the session-bind hook target and the sole path for
// recording the harp → session_id mapping in the index. Claude Code (and
// other backends with SessionStart hooks) fire this exactly once per
// session, with the backend's session ID and transcript path already in
// the documented hook payload. Antigravity has no session-start event, so
// there the hook is installed with pre_tool_fallback and fires on
// PreToolUse — the bind is first-bind-wins, so repeat firings are no-ops.
// The compactor also forward-binds at compact time as a backstop.
var sessionBindCmd = &cobra.Command{
	Use:    "session-bind",
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
		//
		// Skipped for Antigravity payloads: there this command runs as a
		// PreToolUse hook, where stdout is reserved for decision JSON — the
		// SessionStart additionalContext envelope would be junk at best.
		if !isAntigravityHookPayload(raw) {
			emitHarpMarker(cmd.OutOrStdout(), harp)
		}
		if err := bindSessionFromPayload(bytes.NewReader(raw), harp); err != nil {
			clidiag.Warn("ctxloom", "session bind failed: %v", err)
		}
		return nil
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
		HookEventName:     claude.HookEventSessionStart,
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
func bindSessionFromPayload(in io.Reader, harp string) error {
	if harp == "" {
		return nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	var payload claude.SessionStartPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil // malformed payload — no-op
	}
	// Antigravity fires this as a PreToolUse hook with its own payload shape
	// (camelCase, decoded via the agy module's wire types): the conversation
	// ID is the session ID and the transcript path comes on every payload.
	if payload.SessionID == "" {
		if p, err := antigravity.DecodeHookPayload(raw); err == nil && p.ConversationID != "" {
			payload.SessionID = p.ConversationID
			payload.TranscriptPath = p.TranscriptPath
		}
	}
	// operations.BindSession applies first-bind-wins and no-ops a harp that is
	// absent or already bound.
	return operations.BindSession(harp, payload.SessionID, payload.TranscriptPath)
}

// isAntigravityHookPayload reports whether raw is an Antigravity hook payload
// (identified structurally by its conversationId field — Claude payloads
// carry session_id instead).
func isAntigravityHookPayload(raw []byte) bool {
	p, err := antigravity.DecodeHookPayload(raw)
	return err == nil && p.ConversationID != ""
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
	sessionCmd.AddCommand(sessionListCmd, sessionShowCmd, sessionRenameCmd, sessionForgetCmd, sessionDistillCmd, sessionWatchCmd)
	rootCmd.AddCommand(sessionCmd)

	// session-bind is a machine callback (SessionStart hook target), so it lives
	// under the hidden `hook` namespace, not the user-facing `session` one.
	hookCmd.AddCommand(sessionBindCmd)
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
	if cfg.AppRoot == "" {
		return fmt.Errorf("project root not found; run inside a project with .ctxloom/")
	}

	backendName := entry.Backend
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}

	// session_id is recorded forward by the `ctxloom hook session-bind`
	// SessionStart hook (see sessionBindCmd), which reads it straight from
	// the backend's documented hook payload. A container-runtime harp's
	// bind hook runs INSIDE the container, though, and the host session
	// index (~/.ctxloom/sessions/index.yaml) is deliberately not mounted in
	// (only <harp>/persist* and tasks are) — so a container harp's
	// session_id never gets bound host-side, even though its transcript IS
	// reachable host-side (operations.GetSession already resolved
	// entry.TranscriptPath for it via fillTranscriptByLocation). Load the
	// session by that path instead of failing; only hard-error when we have
	// neither a bound id nor a transcript path — genuinely nothing to
	// distill.
	sessionID := entry.SessionID
	var preloaded *agent.Session
	if sessionID == "" {
		if entry.TranscriptPath == "" {
			return fmt.Errorf("harp %q has no session_id bound and no transcript path recorded; nothing to distill (the SessionStart bind hook records the ID for sessions launched via ctxloom run)", harpName)
		}
		hist, herr := operations.HistoryForBackend(backendName)
		if herr != nil {
			return fmt.Errorf("resolve history reader for backend %q: %w", backendName, herr)
		}
		preloaded, err = hist.GetSessionByPath(entry.TranscriptPath)
		if err != nil {
			return fmt.Errorf("load session from transcript %q: %w", entry.TranscriptPath, err)
		}
	}

	// Progress notes go to stderr as best-effort status.
	progress := iox.NewErrWriter(cmd.ErrOrStderr())
	if sessionID != "" {
		progress.Printf("ctxloom: distilling %s (session_id=%s)...\n", harpName, sessionID)
	} else {
		progress.Printf("ctxloom: distilling %s (by transcript path, no session_id bound)...\n", harpName)
	}
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:              cfg.GetCompactionLLM(),
		Model:            cfg.GetCompactionModel(),
		Backend:          backendName,
		ChunkSize:        cfg.GetCompactionChunkSize(),
		SessionID:        sessionID,
		PreloadedSession: preloaded,
		WorkDir:          entry.ProjectDir,
		HarpName:         harpName,
		Progress:         progress, // a CLI owns its terminal; chunk progress is wanted here
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
		if stale, known := e.SourceStale(); known && stale {
			summary += "  ⚠ out of date"
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
