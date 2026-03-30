package backends

import "fmt"

// CLIContextProvider implements ContextProvider for backends that inject context via CLI arguments.
// Context is stored in memory and retrieved during command building.
type CLIContextProvider struct {
	assembledContext string
}

// NilSessionHistory is a no-op SessionHistory for backends that don't support history access.
type NilSessionHistory struct{}

// GetCurrentSession returns an error indicating history is not supported.
func (h *NilSessionHistory) GetCurrentSession(workDir string) (*Session, error) {
	return nil, fmt.Errorf("session history not supported by this backend")
}

// ListSessions returns an empty list.
func (h *NilSessionHistory) ListSessions(workDir string) ([]SessionMeta, error) {
	return nil, nil
}

// GetSession returns an error indicating history is not supported.
func (h *NilSessionHistory) GetSession(workDir string, sessionID string) (*Session, error) {
	return nil, fmt.Errorf("session history not supported by this backend")
}

// GetSessionByPath returns an error indicating history is not supported.
func (h *NilSessionHistory) GetSessionByPath(path string) (*Session, error) {
	return nil, fmt.Errorf("session history not supported by this backend")
}

// RegisterSession is a no-op for backends without history support.
// TranscriptPathFromHook returns empty string - no history support.
func (h *NilSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

func (h *NilSessionHistory) RegisterSession(workDir string, pid int, transcriptPath string) error {
	return nil // Silent no-op
}

// GetPreviousSession returns nil for backends without history support.
func (h *NilSessionHistory) GetPreviousSession(workDir string, pid int) (*Session, error) {
	return nil, nil // No previous session
}

// Provide assembles context fragments and stores them for later retrieval.
func (c *CLIContextProvider) Provide(workDir string, fragments []*Fragment) error {
	c.assembledContext = assembleFragments(fragments)
	return nil
}

// Clear removes the stored context.
func (c *CLIContextProvider) Clear(workDir string) error {
	c.assembledContext = ""
	return nil
}

// GetAssembled returns the assembled context string for CLI injection.
func (c *CLIContextProvider) GetAssembled() string {
	return c.assembledContext
}

// assembleFragments joins fragments with separators.
func assembleFragments(fragments []*Fragment) string {
	if len(fragments) == 0 {
		return ""
	}

	var parts []string
	for _, f := range fragments {
		if f.Content == "" {
			continue
		}
		parts = append(parts, f.Content)
	}

	if len(parts) == 0 {
		return ""
	}

	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += "\n\n---\n\n" + parts[i]
	}
	return result
}
