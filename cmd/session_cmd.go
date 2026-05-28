package cmd

import (
	"bytes"
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

// harpSessionMarkerPrefix tags a session with its own harp name in a
// uniquely-greppable form. ctxloom emits the full marker into the MCP
// ServerOptions.Instructions block (cmd/mcp_server.go), so it lands in the
// session's raw transcript exactly once — in that session's own
// instructions. Transcript-scan discovery anchors on it (see
// discoverSessionByHarpName) to latch onto the session that IS the harp,
// not one that merely mentions it. Kept distinct from any human-readable
// prose so discovery never depends on wording that may change.
const harpSessionMarkerPrefix = "ctxloom harp session: "

// harpSessionMarker returns the canonical discovery marker for a harp. The
// name is double-quoted so the marker is self-delimiting: a substring scan
// for `…: "foo"` won't spuriously match `…: "foo-bar"`. Both the emitter and
// the scanner call this, so the wire format can't drift.
func harpSessionMarker(harp string) string {
	return harpSessionMarkerPrefix + `"` + harp + `"`
}

// discoverSessionByHarpName finds the backend session that IS the given
// harp, used as last-resort rescue when no forward-bind landed.
//
// It scans each transcript for the canonical marker `<prefix><harp>` that
// ctxloom injects into ServerOptions.Instructions (see harpSessionMarker /
// cmd/mcp_server.go), but counts a hit ONLY when the marker rides in that
// transcript's MCP-instructions entry — the one Claude Code writes the
// instructions block into at session init. So a match means this session IS
// the harp, never one that merely *mentions* it.
//
// Why scope to the instructions entry: the marker is plain text, so a
// transcript that discusses a harp (this session distilling another, picker
// listings, list_sessions / task_list output, or — in this very repo — code
// that quotes the marker format) carries the bytes in conversational
// entries. A whole-file byte scan would mis-bind to the wrong conversation.
// The marker only lands in the instructions entry of the session it belongs
// to, so gating on that entry is exact: no other session's instructions
// carry this harp's marker.
//
// How the entry is identified: Claude Code records the instructions block as
// a `type: attachment` entry (attachment.type `mcp_instructions_delta`) whose
// addedBlocks hold each server's instructions text; the normalized parser
// drops it, and the schema is undocumented and unstable
// (anthropics/claude-code#53516). We decode only the few fields that identify
// the entry, and only on lines whose raw bytes already carry the marker
// prefix — see fileContainsMarker. If that subtype string drifts, discovery
// degrades to "not auto-discoverable, bind explicitly" rather than
// mis-binding. Sessions that predate the marker are likewise not
// auto-discoverable.
//
// Per project: typically <100 sessions. Linear scan is fine; the first
// marker hit wins since harps are unique by construction.
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

	marker := harpSessionMarker(harpName)
	for _, m := range metas {
		if m.Path != "" && fileContainsMarker(m.Path, marker) {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("no backend session in %q carries the ctxloom marker for harp %q (only sessions started after the marker landed are auto-discoverable; bind it explicitly otherwise)", workDir, harpName)
}

// instructionsAttachmentType is the attachment subtype Claude Code stamps on
// the entry that records an MCP server's `initialize` instructions block. The
// harp marker is appended to ServerOptions.Instructions (cmd/mcp_server.go),
// so the canonical marker lands in exactly this entry — once, at session
// init. Scanning is scoped to it (see fileContainsMarker) so a later
// conversational mention of the marker can never be mistaken for the binding.
// Part of Claude Code's undocumented transcript schema
// (anthropics/claude-code#53516); if it drifts, discovery degrades to
// "bind explicitly" rather than mis-binding.
const instructionsAttachmentType = "mcp_instructions_delta"

// fileContainsMarker reports whether the transcript at path carries marker in
// its MCP-instructions entry — i.e. whether this session IS the harp, not one
// that merely mentions it (see discoverSessionByHarpName for why this matters).
//
// The whole file is read because a single jsonl entry can exceed
// bufio.Scanner's line limit (megabytes). Each line is one transcript entry.
// We cheaply pre-filter to lines whose RAW bytes carry the marker prefix
// (quote-free, so it survives JSON escaping), then decode only those: the
// marker rides JSON-encoded inside the instructions delta's addedBlocks, so
// its quotes are backslash-escaped on the wire and a raw scan for the
// literal-quote marker would miss it. Matching the *decoded* block restores
// the quotes and keeps the self-delimiting guarantee. A read error, or a line
// that doesn't decode, is treated as "no match" — a transcript or entry we
// can't read can't be the one we want. Cost is irrelevant on this rare
// fallback path.
func fileContainsMarker(path, marker string) bool {
	if marker == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	prefix := []byte(harpSessionMarkerPrefix)
	for line := range bytes.Lines(data) {
		if !bytes.Contains(line, prefix) {
			continue
		}
		var entry struct {
			Type       string `json:"type"`
			Attachment struct {
				Type        string   `json:"type"`
				AddedBlocks []string `json:"addedBlocks"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if entry.Type != "attachment" || entry.Attachment.Type != instructionsAttachmentType {
			continue
		}
		for _, block := range entry.Attachment.AddedBlocks {
			if strings.Contains(block, marker) {
				return true
			}
		}
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
