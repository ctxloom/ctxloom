package vectordb

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	distilledDir = "distilled"
)

// DistilledSession holds a compacted session summary.
type DistilledSession struct {
	SessionID  string    `yaml:"session_id"`
	CreatedAt  time.Time `yaml:"created_at"`
	Content    string    `yaml:"content"`
	TokenCount int       `yaml:"token_count"`
}

// ListDistilledSessions returns all distilled session IDs sorted by name.
func ListDistilledSessions(memoryDir string) ([]string, error) {
	dir := filepath.Join(memoryDir, distilledDir)
	entries, err := os.ReadDir(dir)
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
		// Match session-*.yaml
		if len(name) > 13 && name[:8] == "session-" && name[len(name)-5:] == ".yaml" {
			sessionID := name[8 : len(name)-5]
			sessions = append(sessions, sessionID)
		}
	}

	sort.Strings(sessions)
	return sessions, nil
}

// LoadDistilledSession loads a distilled session summary.
func LoadDistilledSession(memoryDir, sessionID string) (*DistilledSession, error) {
	path := filepath.Join(memoryDir, distilledDir, "session-"+sessionID+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var distilled DistilledSession
	if err := yaml.Unmarshal(data, &distilled); err != nil {
		return nil, err
	}

	return &distilled, nil
}
