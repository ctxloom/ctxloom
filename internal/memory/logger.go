package memory

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// SessionsDir is the subdirectory for session logs.
	SessionsDir = "sessions"
	// DistilledDir is the subdirectory for compacted summaries.
	DistilledDir = "distilled"
)

// LoadSessionLog loads all entries from a session log file.
func LoadSessionLog(memoryDir, sessionID string) ([]Entry, error) {
	logPath := filepath.Join(memoryDir, SessionsDir, fmt.Sprintf("session-%s.jsonl", sessionID))
	file, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer func() { _ = file.Close() }()

	var entries []Entry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		entry, err := UnmarshalJSONL(scanner.Bytes())
		if err != nil {
			// Skip malformed entries
			continue
		}
		entries = append(entries, *entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan log file: %w", err)
	}

	return entries, nil
}

// LoadSessionMeta loads metadata for a session.
func LoadSessionMeta(memoryDir, sessionID string) (*SessionMeta, error) {
	metaPath := filepath.Join(memoryDir, SessionsDir, fmt.Sprintf("session-%s.meta.yaml", sessionID))
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, err
	}

	var meta SessionMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

// ListSessions returns all session IDs in the memory directory.
func ListSessions(memoryDir string) ([]string, error) {
	sessionsPath := filepath.Join(memoryDir, SessionsDir)
	entries, err := os.ReadDir(sessionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Match session-*.jsonl
		if len(name) > 14 && name[:8] == "session-" && name[len(name)-6:] == ".jsonl" {
			sessionID := name[8 : len(name)-6]
			sessions = append(sessions, sessionID)
		}
	}

	return sessions, nil
}
