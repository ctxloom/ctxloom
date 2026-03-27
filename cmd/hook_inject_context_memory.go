//go:build memory

package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/lm/backends"
	"github.com/SophisticatedContextManager/scm/internal/memory"
)

// loadSessionMemoryForHook loads the most recent distilled session for context injection.
// Returns empty string if no distilled session exists or on any error.
// Skips loading if this appears to be a session resume (current session has existing entries).
func loadSessionMemoryForHook(workDir string) string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}

	// Check if memory loading is enabled
	if !cfg.Memory.ShouldLoadOnStart() {
		return ""
	}

	// Check if this is a resume (session already has content)
	// If so, skip loading - we don't want to re-inject memory on resume
	if isSessionResume(workDir) {
		return ""
	}

	memoryDir := getMemoryDir(cfg)

	// Get distilled sessions
	sessions, err := memory.ListDistilledSessions(memoryDir)
	if err != nil || len(sessions) == 0 {
		return ""
	}

	// Sort and get most recent
	sort.Strings(sessions)
	mostRecent := sessions[len(sessions)-1]

	// Load distilled content
	distilled, err := memory.LoadDistilledSession(memoryDir, mostRecent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scm hook inject-context: warning: failed to load session memory: %v\n", err)
		return ""
	}

	// Format for context injection
	var builder strings.Builder
	builder.WriteString("# Previous Session Memory\n\n")
	builder.WriteString(fmt.Sprintf("*Distilled from session: %s*\n\n", distilled.SessionID))
	builder.WriteString(distilled.Content)

	return builder.String()
}

// isSessionResume checks if this appears to be a session resume.
// Returns true if the current session already has user messages (entries).
// On new session or /clear, the session will be empty at SessionStart time.
func isSessionResume(workDir string) bool {
	history := &backends.ClaudeSessionHistory{}
	session, err := history.GetCurrentSession(workDir)
	if err != nil {
		// No session or error - treat as new session
		return false
	}

	// If session has any user entries, this is a resume
	for _, entry := range session.Entries {
		if entry.Type == backends.EntryTypeUser {
			return true
		}
	}
	return false
}
