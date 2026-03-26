//go:build !memory

package memory

const (
	// SessionsDir is the subdirectory for session logs.
	SessionsDir = "sessions"
	// DistilledDir is the subdirectory for compacted summaries.
	DistilledDir = "distilled"
	// VectorsDir is the subdirectory for vector database.
	VectorsDir = "vectors"
)

// LoadSessionLog is a no-op when memory feature is disabled.
func LoadSessionLog(memoryDir, sessionID string) ([]Entry, error) {
	return nil, nil
}

// LoadSessionMeta is a no-op when memory feature is disabled.
func LoadSessionMeta(memoryDir, sessionID string) (*SessionMeta, error) {
	return nil, nil
}

// ListSessions is a no-op when memory feature is disabled.
func ListSessions(memoryDir string) ([]string, error) {
	return nil, nil
}
