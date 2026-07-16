package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Manage session memory (out-of-band compaction)",
	Long: `Manage session memory: distil a session's transcript so a later session can pick it up.

Compaction here happens OUT OF BAND, and that is the point. Native in-context
compaction asks an agent to summarise itself at the moment it is running out of
room to think — a context-starved agent writes a starved summary, and whatever it
drops is gone. ctxloom instead reads the complete transcript from disk in a fresh
process with a full budget, and distils it there.

So it never competes with native compaction for live context pressure; use native
compaction for that. This runs where the summary can be good: on shutdown, at
startup for historical sessions, and on recovery after a /clear.

Commands:
  ctxloom memory list                  List all sessions
  ctxloom memory show <session>        Show session details
  ctxloom memory compact [--session]   Distil a session log`,
}

var memoryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	RunE:  runMemoryList,
}

var memoryShowCmd = &cobra.Command{
	Use:   "show <session-id>",
	Short: "Show session details",
	Args:  cobra.ExactArgs(1),
	RunE:  runMemoryShow,
}

var memoryCompactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Compact a session log",
	Long: `Compact a session log by distilling it into a summary.

This uses an external LLM (default: claude-3-haiku) to compress the session
log into a concise summary that can be loaded in future sessions.

The compaction runs in chunks to handle large sessions that exceed
context window limits.

Examples:
  ctxloom memory compact                    # Compact most recent session
  ctxloom memory compact --session abc123   # Compact specific session
  ctxloom memory compact --model antigravity # Use specific model`,
	RunE: runMemoryCompact,
}

var (
	compactSession string
	compactModel   string
	compactBackend string
	listBackend    string
	showBackend    string
	showFull       bool
)

func init() {
	rootCmd.AddCommand(memoryCmd)
	memoryCmd.AddCommand(memoryListCmd)
	memoryCmd.AddCommand(memoryShowCmd)
	memoryCmd.AddCommand(memoryCompactCmd)

	memoryListCmd.Flags().StringVar(&listBackend, "backend", "", "Backend to list sessions from (default: the configured default LLM)")
	memoryShowCmd.Flags().StringVar(&showBackend, "backend", "", "Backend to read session from (default: the configured default LLM)")
	memoryShowCmd.Flags().BoolVar(&showFull, "full", false, "Show the full distilled summary without truncation")

	memoryCompactCmd.Flags().StringVar(&compactSession, "session", "", "Session ID to compact (default: most recent)")
	memoryCompactCmd.Flags().StringVar(&compactModel, "model", "", "LLM model to use for distillation (default: from config or claude-3-haiku)")
	memoryCompactCmd.Flags().StringVar(&compactBackend, "backend", "", "Backend to read session from (default: the configured default LLM)")
}

// memoryListSessionEntry is one `memory list --format json` row: a session's
// metadata plus the distillation status the text table's STATUS column shows
// ("pending" or "compacted") — computed once and carried into both renderers
// so json and text never disagree.
type memoryListSessionEntry struct {
	agent.SessionMeta
	Status string `json:"status"`
}

// memoryListOutput is the --format json shape for `ctxloom memory list`.
type memoryListOutput struct {
	Backend  string                   `json:"backend"`
	Sessions []memoryListSessionEntry `json:"sessions"`
}

// memoryListEntries pairs each session with its distillation status, in the
// order ListSessions returned them. Pulled out of runMemoryList so it's unit
// testable without a live session source.
func memoryListEntries(sessions []agent.SessionMeta, distilledSet map[string]bool) []memoryListSessionEntry {
	entries := make([]memoryListSessionEntry, 0, len(sessions))
	for _, meta := range sessions {
		status := "pending"
		if distilledSet[meta.ID] {
			status = "compacted"
		}
		entries = append(entries, memoryListSessionEntry{SessionMeta: meta, Status: status})
	}
	return entries
}

func runMemoryList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	source, backendName, err := resolveSessionSource(cfg, listBackend, workDir)
	if err != nil {
		return err
	}

	sessions, err := source.ListSessions(context.Background())
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	distilledSet := loadDistilledSet(cfg)
	out := memoryListOutput{Backend: backendName, Sessions: memoryListEntries(sessions, distilledSet)}

	return emit(cmd, out, func() error {
		if len(sessions) == 0 {
			fmt.Printf("No sessions found in %s.\n", backendName)
			return nil
		}
		fmt.Printf("Sessions from %s:\n\n", backendName)
		printSessionTable(sessions, distilledSet)
		return nil
	})
}

// resolveSessionSource resolves the backend (defaulting when empty) and a
// transcript source for it, returning the resolved backend name for display.
// Shared by the memory list and load tools. The legacy leg (pb.SessionReader)
// reads over gRPC to the agent server (self-situated, no workspace passed,
// works for a remote agent); it is wrapped in CanonicalFallbackSource
// (tough-cloud S4) so any harp with a captured canonical transcript is read
// from that instead — workDir scopes the canonical side to this project. A
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

// loadDistilledSet returns the set of session IDs already distilled (best
// effort; a listing error is non-fatal and yields an empty set).
func loadDistilledSet(cfg *config.Config) map[string]bool {
	distilled, err := memory.ListDistilledSessions(paths.ProjectSessionsDir(cfg.AppDir))
	if err != nil {
		distilled = nil // Non-fatal
	}
	set := make(map[string]bool, len(distilled))
	for _, s := range distilled {
		set[s] = true
	}
	return set
}

// printSessionTable renders the session list as an aligned table.
func printSessionTable(sessions []agent.SessionMeta, distilledSet map[string]bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "SESSION ID\tSTARTED\tENTRIES\tSTATUS")
	_, _ = fmt.Fprintln(w, "----------\t-------\t-------\t------")

	for _, meta := range sessions {
		started := "?"
		if !meta.StartTime.IsZero() {
			started = meta.StartTime.Format("2006-01-02 15:04")
		}

		status := "pending"
		if distilledSet[meta.ID] {
			status = "compacted"
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", meta.ID, started, meta.EntryCount, status)
	}
	_ = w.Flush()
}

func runMemoryShow(cmd *cobra.Command, args []string) error {
	sessionID := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Determine backend
	source, backendName, err := resolveSessionSource(cfg, showBackend, workDir)
	if err != nil {
		return err
	}

	session, err := source.GetSession(context.Background(), sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}

	fmt.Printf("Session: %s\n", session.ID)
	fmt.Printf("Backend: %s\n", backendName)
	fmt.Printf("Started: %s\n", session.StartTime.Format(time.RFC3339))
	if !session.EndTime.IsZero() {
		fmt.Printf("Ended: %s\n", session.EndTime.Format(time.RFC3339))
		fmt.Printf("Duration: %s\n", session.EndTime.Sub(session.StartTime).Round(time.Second))
	}
	fmt.Printf("Entries: %d\n", len(session.Entries))

	// Check for distilled version
	sessionsDir := paths.ProjectSessionsDir(cfg.AppDir)
	distilled, err := memory.LoadDistilledSession(sessionsDir, sessionID)
	if err == nil {
		fmt.Println("\n--- Distilled Summary ---")
		fmt.Printf("Created: %s\n", distilled.DistilledAt.Format(time.RFC3339))
		fmt.Printf("Tokens: %d\n", distilled.TokensOut)
		fmt.Println()

		// Truncate if very long (rune-safe: a byte slice can split a multibyte rune)
		content := distilled.Body
		if !showFull && len(content) > 2000 {
			content = textutil.TruncateBytes(content, 2000) + "\n\n... [truncated, use --full to see all]"
		}
		fmt.Println(content)
	}

	return nil
}

func runMemoryCompact(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Determine plugin and model for distillation
	plugin := cfg.GetCompactionLLM()
	model := compactModel
	if model == "" {
		model = cfg.GetCompactionModel()
	}

	// Determine backend to read session from
	backend := compactBackend
	if backend == "" {
		backend = cfg.GetDefaultLLM()
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Create compactor
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:       plugin,
		Model:     model,
		Backend:   backend,
		ChunkSize: cfg.GetCompactionChunkSize(),
		SessionID: compactSession,
		WorkDir:   workDir,
		OutputDir: paths.ProjectSessionsDir(cfg.AppDir),
		Progress:  os.Stderr, // a CLI owns its terminal; chunk progress is wanted here
	})
	if err != nil {
		return fmt.Errorf("create compactor: %w", err)
	}

	fmt.Printf("Compacting session from %s using %s (model: %s)...\n", backend, plugin, model)

	// Run compaction
	result, err := compactor.Compact(context.Background())
	if err != nil {
		return fmt.Errorf("compaction failed: %w", err)
	}

	// Print results
	fmt.Println()
	fmt.Printf("Session: %s\n", result.SessionID)
	fmt.Printf("Chunks: %d\n", result.ChunksCreated)
	fmt.Printf("Tokens: %d -> %d (%s reduction)\n",
		result.TotalTokensIn,
		result.TotalTokensOut,
		reductionPct(result.TotalTokensIn, result.TotalTokensOut))
	fmt.Printf("Duration: %s\n", result.Duration.Round(time.Millisecond))
	fmt.Printf("Output: %s\n", result.DistilledPath)

	return nil
}
