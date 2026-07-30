package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/antigravity"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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

// readHarpEssence returns the bytes of ~/.ctxloom/sessions/<harp>/essence.md.
// Errors when home can't be resolved or the file is missing.
func readHarpEssence(harpName string) ([]byte, error) {
	p, err := paths.HarpEssencePath(harpName)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// readSessionEssence returns a session's distilled essence and whether one was
// found. It is the READING face of sessionEssenceInfo — one resolution order
// for both, so a session can never read as distilled in one command and
// pending in another. A pending session (no bound id) or a missing essence
// yields ("", false) rather than an error, so callers can present "not
// distilled yet" uniformly; an essence that exists but cannot be READ is a
// different fact and is reported rather than passed off as never-distilled.
func readSessionEssence(harp string, entry *sessions.Entry) (string, bool) {
	appDir := ""
	if cfg, err := config.Load(); err == nil {
		appDir = cfg.GetAppDir()
	}
	path, distilled := sessionEssenceInfo(harp, entry, appDir)
	if !distilled {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		clidiag.Warn("ctxloom", "essence for %s exists at %s but could not be read: %v", harp, path, err)
		return "", false
	}
	return string(data), true
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
		// U042-F03: this must never fail the host backend's tool call over a
		// bad hook message (returning nil is right), but a malformed payload
		// silently skipping the harp->session_id bind with NOTHING reported
		// anywhere left an operator no way to learn why a harp never got
		// captured — one of the two live-reproducible causes behind
		// "no canonical transcript captured for harp ...".
		clidiag.Warn("ctxloom", "session-bind: harp %q: SessionStart hook payload did not parse as JSON: %v — harp<->session_id bind skipped", harp, err)
		return nil
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
	// dizzy-zoom, confirmed live 2026-07-21 against real kiro-cli 2.12.1:
	// kiro's agentSpawn hook stdin payload carries NO session identifier at
	// all ({"hook_event_name":"agentSpawn","cwd":...,"prompt":...} — no
	// session_id/conversation_id field), unlike Claude/Codex/Antigravity's
	// payloads. It DOES set KIRO_SESSION_ID in the hook subprocess's OWN
	// environment, confirmed (by direct sqlite query against the real
	// conversations_v2 table) to equal that conversation's actual
	// conversation_id. Falling back to it here means locateKiroConversation
	// (vendorimport_kiro.go) hits its SessionID-bound fast path — an exact
	// match, not the best-effort enumerate-by-workdir heuristic — for every
	// ctxloom-launched kiro session, since the agentSpawn hook always fires.
	if payload.SessionID == "" {
		payload.SessionID = os.Getenv("KIRO_SESSION_ID")
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
	sessionListCmd.Flags().BoolVar(&sessionListDistill, "distill", false, "Distill sessions whose essence is missing or stale before listing, so every row shows a title")
	sessionListCmd.Flags().BoolVar(&sessionListFull, "full", false, "Include each session's complete distilled essence body (text/markdown output pages through $PAGER on a terminal)")
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

// distillMissingOrStale distills every entry whose essence is missing or stale
// (SourceStale), so `session list --distill` shows a title on every row. Each
// session is compacted in its own project dir — the legacy transcript reader is
// cwd-bound — so the loop chdir's per entry and restores the original cwd on
// return. Per-entry failures are warned and skipped: a session that can't be
// distilled (e.g. a legacy-only session with no reachable transcript) must not
// block the listing (CLAUDE.md — a usable partial listing beats a hard fail).
func distillMissingOrStale(cmd *cobra.Command, entries []sessions.Entry, appDir string) {
	origWd, _ := os.Getwd()
	defer func() {
		if origWd != "" {
			_ = os.Chdir(origWd)
		}
	}()
	progress := iox.NewErrWriter(cmd.ErrOrStderr())
	for i := range entries {
		e := &entries[i]
		_, distilled := sessionEssenceInfo(e.HarpName, e, appDir)
		stale, known := e.SourceStale()
		knownStale := known && stale
		if distilled && !knownStale {
			continue // fresh essence already present
		}
		// Situate in the entry's own project dir before loading config /
		// reading the transcript (see runSessionDistill for why chdir is
		// required and safe for a one-shot CLI) — or back in origWd when
		// this entry has none of its own (U042-F04: leaving the PREVIOUS
		// entry's chdir in place here meant config.Load() silently read the
		// wrong project's config for THIS entry, using another project's
		// cwd-bound legacy LLM/backend settings for a distillation that
		// never intended to touch it at all).
		if cerr := situateForEntry(e, origWd); cerr != nil {
			clidiag.Warn("ctxloom", "could not enter project dir %q for %s: %v", e.ProjectDir, e.HarpName, cerr)
			continue
		}
		cfg, cErr := config.Load()
		if cErr != nil {
			clidiag.Warn("ctxloom", "could not load config to distill %s: %v", e.HarpName, cErr)
			continue
		}
		if _, dErr := compactEntry(cmd.Context(), e, cfg, "", progress); dErr != nil {
			clidiag.Warn("ctxloom", "could not distill %s: %v", e.HarpName, dErr)
		}
	}
}

// situateForEntry chdirs the process to e's own ProjectDir, or back to
// origWd when e has none — the shared cwd-management step distillMissingOrStale
// needs before every config.Load()/compactEntry call, extracted so it is
// independently testable (U042-F04) rather than living as an inline branch
// that only ever changed directory FORWARD and never restored it for an
// entry with no ProjectDir of its own. A no-op when the process is already
// in the wanted directory.
func situateForEntry(e *sessions.Entry, origWd string) error {
	want := e.ProjectDir
	if want == "" {
		want = origWd
	}
	if want == "" {
		return nil // no ProjectDir and no resolvable origWd — nothing to situate
	}
	if cwd, err := os.Getwd(); err == nil && cwd == want {
		return nil
	}
	return os.Chdir(want)
}

// compactionModelFor resolves the model one distill runs with: an explicit
// caller override wins, and "" falls back to the configured compaction model.
// Kept as a named function despite the single call site (compactEntry, which
// is itself the single funnel) because it is the one unit-testable statement
// of the sour-scoop rule — honoring the override on only some distill paths
// is exactly the bug this exists to prevent — and compactEntry needs a live
// session entry and compactor to exercise.
func compactionModelFor(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	return cfg.GetCompactionModel()
}

// compactEntry runs the compactor for a single session entry and returns the
// result. It does NOT change the working directory and does NOT load config —
// the caller supplies cfg and situates the process. This split lets the
// one-shot CLI (`session distill`, `session list --distill`) chdir into the
// entry's project dir first, so the cwd-bound legacy transcript reader
// resolves, while the long-lived MCP server — which must never chdir — calls
// it as-is: canonical-transcript sessions resolve cwd-independently through
// WorkDir, and legacy-only sessions there degrade to the clear "nothing to
// distill" error below rather than corrupting the server's cwd.
//
// session_id is recorded forward by the `ctxloom hook session-bind`
// SessionStart hook (see sessionBindCmd). A container-runtime harp's bind hook
// runs INSIDE the container, though, and the host session index is not mounted
// in — so its session_id never gets bound host-side even though its transcript
// IS reachable (operations.GetSession resolved entry.TranscriptPath via
// fillTranscriptByLocation). Load by that path instead of failing; a canonical
// transcript (a oneshot Execute run's own transcript.jsonl, resolved by
// HarpName inside pb.NewCanonicalFallbackSource) needs no preload. Only
// hard-error when there is neither a bound id, a transcript path, nor a
// captured transcript — genuinely nothing to distill.
// model overrides the compaction model for THIS call; "" uses
// cfg.GetCompactionModel(). It exists so a caller-supplied model override
// reaches the canonical/harp distill path too, not just the backend one
// (sour-scoop) — the same "empty means config default" shape distillSession
// already uses.
func compactEntry(ctx context.Context, entry *sessions.Entry, cfg *config.Config, model string, progress io.Writer) (*memory.CompactionResult, error) {
	model = compactionModelFor(cfg, model)
	backendName := entry.Backend
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}

	sessionID := entry.SessionID
	var preloaded *agent.Session
	if sessionID == "" {
		switch {
		case entry.TranscriptPath != "":
			hist, herr := operations.HistoryForBackend(backendName)
			if herr != nil {
				return nil, fmt.Errorf("resolve history reader for backend %q: %w", backendName, herr)
			}
			preloaded, herr = hist.GetSessionByPath(entry.TranscriptPath)
			if herr != nil {
				return nil, fmt.Errorf("load session from transcript %q: %w", entry.TranscriptPath, herr)
			}
		case entry.CanonicalTranscriptPath != "":
			// Canonical fallback resolves the harp's own transcript by HarpName
			// inside the compactor; nothing to preload here.
		default:
			return nil, fmt.Errorf("harp %q has no session_id bound, no transcript path recorded, and no captured transcript; nothing to distill (the SessionStart bind hook records the ID for sessions launched via ctxloom run)", entry.HarpName)
		}
	}

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:              cfg.GetCompactionLLM(),
		Model:            model,
		Backend:          backendName,
		ChunkSize:        cfg.GetCompactionChunkSize(),
		SessionID:        sessionID,
		PreloadedSession: preloaded,
		WorkDir:          entry.ProjectDir,
		HarpName:         entry.HarpName,
		Progress:         progress,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}
	result, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("distillation failed: %w", err)
	}
	return result, nil
}

// resolveSessionSource resolves the backend (defaulting when empty) and a
// transcript source for it, returning the resolved backend name for display.
// Shared by loadOrDistillSession's callers (mcp_tools_memory.go) — and,
// before the deprecated `memory` command group was deleted (U039-F20), by
// `memory list`/`memory show` too. The legacy leg (pb.SessionReader) reads
// over gRPC to the agent server (self-situated, no workspace passed, works
// for a remote agent); it is wrapped in CanonicalFallbackSource (tough-cloud
// S4) so any harp with a captured canonical transcript is read from that
// instead — workDir scopes the canonical side to this project. A
// session-index open failure degrades to the legacy-only reader rather than
// failing the caller outright.
//
// tough-cloud S5: a pb.RetiredScraperBackends entry (codex/kiro/antigravity/
// claude-code — their scrapers were deleted, not demoted) never gets a legacy
// leg at all: there is no plugin-side History() left to ask, so this never
// even spawns the plugin for that purpose. Every other backend (opencode's
// native reader included) keeps its legacy leg unchanged.
func resolveSessionSource(cfg *config.Config, backendName, workDir string) (pb.SessionSource, string, error) {
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}
	if !backends.Exists(backendName) {
		return nil, backendName, fmt.Errorf("unknown backend: %s", backendName)
	}
	var legacy pb.SessionSource
	if !pb.RetiredScraperBackends[backendName] {
		legacy = pb.NewSessionReader(backendName, 0)
	}
	store, err := sessions.Open("")
	if err != nil {
		clidiag.Warn("ctxloom", "session index open failed, reading legacy transcripts only: %v", err)
		if legacy != nil {
			return legacy, backendName, nil
		}
		return nil, backendName, fmt.Errorf("session index unavailable and %s has no legacy transcript reader: %w", backendName, err)
	}
	return pb.NewCanonicalFallbackSource(legacy, workDir, store), backendName, nil
}

// StartSessionInfo is the read-only pre-spawn summary `ctxloom run` prints
// just before launching the engine (WS-5, Decision 11): this session's harp,
// an assembled-context summary, and a pointer to the previous session for
// this project. Every field is something the caller already computed for
// THIS launch (context assembly, harp assignment) or resolved via the SAME
// primitive the get_previous_session MCP tool reads
// (operations.ResolvePreviousSession, internal/operations/sessions.go) — the
// banner never re-derives a summary of its own.
type StartSessionInfo struct {
	Harp      string
	Backend   string
	Label     string
	Profiles  []string
	Fragments []string
	Tokens    int
	// Previous is the project's prior session, or nil when there is none
	// (first run in this project). Informational only — startup no longer
	// offers to resume it (Decision 11); the in-engine "resume" skill
	// (recover_session/load_session/get_previous_session) is how a user
	// actually pulls it back, from inside the session that just started.
	Previous *operations.PreviousSessionRef
}

// PrintStartSessionBanner renders info to w as the read-only start-session
// display: printed AFTER a fresh harp is minted and BEFORE the engine spawns.
// It replaces the old interactive resume picker (which used to occupy this
// same moment in startup) with a plain, non-interactive summary — startup no
// longer asks the user anything about resuming; it just reports what this
// session is. Every field is optional so a sparsely-populated StartSessionInfo
// (e.g. context assembly or harp assignment degraded per CLAUDE.md fault
// tolerance) still renders a useful, non-erroring banner.
func PrintStartSessionBanner(w io.Writer, info StartSessionInfo) {
	label := info.Label
	if label == "" {
		label = info.Backend
	}
	if label != "" {
		fmt.Fprintf(w, "ctxloom: starting session %s (%s)\n", info.Harp, label)
	} else {
		fmt.Fprintf(w, "ctxloom: starting session %s\n", info.Harp)
	}
	if len(info.Profiles) > 0 {
		fmt.Fprintf(w, "  profiles: %s\n", strings.Join(info.Profiles, ", "))
	}
	switch {
	case len(info.Fragments) > 0:
		fmt.Fprintf(w, "  context: %d fragment(s), ~%d tokens\n", len(info.Fragments), info.Tokens)
	case info.Tokens > 0:
		fmt.Fprintf(w, "  context: ~%d tokens\n", info.Tokens)
	}
	if info.Previous != nil && info.Previous.Harp != "" {
		fmt.Fprintf(w, "  previous session: %s — bring it back in-session with the \"resume\" skill\n", info.Previous.Harp)
	}
}
