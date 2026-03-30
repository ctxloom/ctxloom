package backends

import (
	"os"
	"path/filepath"
)

// BaseContextProvider provides shared context management logic for backends
// that use file-based context injection via hooks.
type BaseContextProvider struct {
	contextHash string
}

// NewBaseContextProvider creates a new context provider.
func NewBaseContextProvider() *BaseContextProvider {
	return &BaseContextProvider{}
}

// Provide writes context to a file that the session start hook will read.
func (c *BaseContextProvider) Provide(workDir string, fragments []*Fragment) error {
	hash, err := WriteContextFile(workDir, fragments)
	if err != nil {
		return err
	}
	c.contextHash = hash
	return nil
}

// Clear removes the context file.
func (c *BaseContextProvider) Clear(workDir string) error {
	if c.contextHash != "" {
		contextPath := filepath.Join(workDir, SCMContextSubdir, c.contextHash+".md")
		_ = os.Remove(contextPath)
		c.contextHash = ""
	}
	return nil
}

// GetContextHash returns the hash of the current context file.
func (c *BaseContextProvider) GetContextHash() string {
	return c.contextHash
}

// GetContextFilePath returns the path to the context file (for env var).
func (c *BaseContextProvider) GetContextFilePath() string {
	if c.contextHash == "" {
		return ""
	}
	return filepath.Join(SCMContextSubdir, c.contextHash+".md")
}
