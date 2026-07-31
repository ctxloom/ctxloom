package agent

import (
	"errors"
	"fmt"
	"io/fs"
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

// Clear removes the context file and releases its hash. A file that is ALREADY
// gone is a successful clear (nothing was left behind); any other removal failure
// is returned AND leaves contextHash intact — the hash is the only handle on the
// file still on disk, so discarding it would make the leak both unreportable and
// unretryable.
func (c *BaseContextProvider) Clear(workDir string) error {
	if c.contextHash == "" {
		return nil
	}
	contextPath := filepath.Join(workDir, SCMContextSubdir, c.contextHash+".md")
	if err := os.Remove(contextPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove context file for %s: %w", c.contextHash, err)
	}
	c.contextHash = ""
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
