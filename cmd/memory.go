package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/memory"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Manage session memory (external compaction)",
	Long: `Manage session memory for external compaction.

This feature logs conversations and allows compacting them for use in new sessions.
It's a workaround for when native LLM compaction is insufficient.

Commands:
  ctxloom memory list                  List all sessions
  ctxloom memory show <session>        Show session details
  ctxloom memory compact [--session]   Compact a session log

Build with -tags memory to enable this feature.`,
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
  ctxloom memory compact --model gemini     # Use specific model`,
	RunE: runMemoryCompact,
}

var memoryCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check session size and auto-compact if needed",
	Long: `Check the current session log size and trigger compaction if it exceeds
the context window threshold.

This is designed to be called from a PostToolUse hook to proactively manage
context before it becomes too large.

The threshold is based on Claude's ~200K token context window (~800KB).
Compaction is triggered when the session approaches this size.

Examples:
  ctxloom memory check                    # Check and compact if needed
  ctxloom memory check --threshold 500000 # Custom threshold in bytes`,
	RunE: runMemoryCheck,
}

var (
	compactSession string
	compactModel   string
	compactBackend string
	listBackend    string
	showBackend    string
	checkThreshold int64
	checkBackend   string
)

func init() {
	rootCmd.AddCommand(memoryCmd)
	memoryCmd.AddCommand(memoryListCmd)
	memoryCmd.AddCommand(memoryShowCmd)
	memoryCmd.AddCommand(memoryCompactCmd)
	memoryCmd.AddCommand(memoryCheckCmd)

	memoryListCmd.Flags().StringVar(&listBackend, "backend", "", "Backend to list sessions from (default: claude-code)")
	memoryShowCmd.Flags().StringVar(&showBackend, "backend", "", "Backend to read session from (default: claude-code)")

	memoryCompactCmd.Flags().StringVar(&compactSession, "session", "", "Session ID to compact (default: most recent)")
	memoryCompactCmd.Flags().StringVar(&compactModel, "model", "", "LLM model to use for distillation (default: from config or claude-3-haiku)")
	memoryCompactCmd.Flags().StringVar(&compactBackend, "backend", "", "Backend to read session from (default: claude-code)")

	// ~800KB default threshold (200K tokens * 4 chars/token)
	memoryCheckCmd.Flags().Int64Var(&checkThreshold, "threshold", 800000, "Size threshold in bytes to trigger compaction")
	memoryCheckCmd.Flags().StringVar(&checkBackend, "backend", "", "Backend to check session from (default: claude-code)")
}

func getMemoryDir(cfg *config.Config) string {
	return filepath.Join(cfg.AppDir, "memory")
}

func runMemoryList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Determine backend
	backendName := listBackend
	if backendName == "" {
		backendName = cfg.LM.GetDefaultPlugin()
	}

	backend := backends.Get(backendName)
	if backend == nil {
		return fmt.Errorf("unknown backend: %s", backendName)
	}

	history := backend.History()
	if history == nil {
		return fmt.Errorf("backend %q does not support session history", backendName)
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	sessions, err := history.ListSessions(workDir)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	// Check which sessions have been compacted
	memoryDir := getMemoryDir(cfg)
	distilled, err := memory.ListDistilledSessions(memoryDir)
	if err != nil {
		distilled = nil // Non-fatal
	}
	distilledSet := make(map[string]bool)
	for _, s := range distilled {
		distilledSet[s] = true
	}

	if len(sessions) == 0 {
		fmt.Printf("No sessions found in %s.\n", backendName)
		return nil
	}

	fmt.Printf("Sessions from %s:\n\n", backendName)

	// Print table
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

	return nil
}

func runMemoryShow(cmd *cobra.Command, args []string) error {
	sessionID := args[0]

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Determine backend
	backendName := showBackend
	if backendName == "" {
		backendName = cfg.LM.GetDefaultPlugin()
	}

	backend := backends.Get(backendName)
	if backend == nil {
		return fmt.Errorf("unknown backend: %s", backendName)
	}

	history := backend.History()
	if history == nil {
		return fmt.Errorf("backend %q does not support session history", backendName)
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Load session
	session, err := history.GetSession(workDir, sessionID)
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
	memoryDir := getMemoryDir(cfg)
	distilled, err := memory.LoadDistilledSession(memoryDir, sessionID)
	if err == nil {
		fmt.Println("\n--- Distilled Summary ---")
		fmt.Printf("Created: %s\n", distilled.CreatedAt.Format(time.RFC3339))
		fmt.Printf("Tokens: %d\n", distilled.TokenCount)
		fmt.Println()

		// Truncate if very long
		content := distilled.Content
		if len(content) > 2000 {
			content = content[:2000] + "\n\n... [truncated, use --full to see all]"
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
	plugin := cfg.GetCompactionPlugin()
	model := compactModel
	if model == "" {
		model = cfg.GetCompactionModel()
	}

	// Determine backend to read session from
	backend := compactBackend
	if backend == "" {
		backend = cfg.LM.GetDefaultPlugin()
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Create compactor
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    plugin,
		Model:     model,
		Backend:   backend,
		ChunkSize: cfg.GetCompactionChunkSize(),
		SessionID: compactSession,
		WorkDir:   workDir,
		OutputDir: getMemoryDir(cfg),
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
	fmt.Printf("Tokens: %d -> %d (%.0f%% reduction)\n",
		result.TotalTokensIn,
		result.TotalTokensOut,
		100*(1-float64(result.TotalTokensOut)/float64(result.TotalTokensIn)))
	fmt.Printf("Duration: %s\n", result.Duration.Round(time.Millisecond))
	fmt.Printf("Output: %s\n", result.DistilledPath)

	return nil
}

func runMemoryCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Determine backend
	backendName := checkBackend
	if backendName == "" {
		backendName = cfg.LM.GetDefaultPlugin()
	}

	backend := backends.Get(backendName)
	if backend == nil {
		return fmt.Errorf("unknown backend: %s", backendName)
	}

	history := backend.History()
	if history == nil {
		return fmt.Errorf("backend %q does not support session history", backendName)
	}

	// Get working directory
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Get current session
	session, err := history.GetCurrentSession(workDir)
	if err != nil {
		return fmt.Errorf("get current session: %w", err)
	}

	if session == nil {
		fmt.Println("No current session found")
		return nil
	}

	// Get session file size
	sessionPath := history.TranscriptPathFromHook(workDir, session.ID, "")
	info, err := os.Stat(sessionPath)
	if err != nil {
		return fmt.Errorf("stat session file: %w", err)
	}

	size := info.Size()
	fmt.Printf("Session: %s\n", session.ID)
	fmt.Printf("Size: %s (%d bytes)\n", formatSize(size), size)
	fmt.Printf("Threshold: %s (%d bytes)\n", formatSize(checkThreshold), checkThreshold)

	if size < checkThreshold {
		fmt.Println("Status: OK (below threshold)")
		return nil
	}

	fmt.Println("Status: COMPACTING (above threshold)")

	// Trigger compaction
	plugin := cfg.GetCompactionPlugin()
	model := cfg.GetCompactionModel()

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Plugin:    plugin,
		Model:     model,
		Backend:   backendName,
		ChunkSize: cfg.GetCompactionChunkSize(),
		SessionID: session.ID,
		WorkDir:   workDir,
		OutputDir: getMemoryDir(cfg),
	})
	if err != nil {
		return fmt.Errorf("create compactor: %w", err)
	}

	result, err := compactor.Compact(context.Background())
	if err != nil {
		return fmt.Errorf("compaction failed: %w", err)
	}

	fmt.Printf("Compacted: %d -> %d tokens (%.0f%% reduction)\n",
		result.TotalTokensIn,
		result.TotalTokensOut,
		100*(1-float64(result.TotalTokensOut)/float64(result.TotalTokensIn)))

	return nil
}

// formatSize formats a byte count as a human-readable string.
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

