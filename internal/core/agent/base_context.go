package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/spf13/afero"
)

// BaseContextProvider writes the assembled context to its content-addressed
// cache file for a launch, whose path reaches the engine as
// CTXLOOM_CONTEXT_FILE, and removes it afterwards. The file is written and
// removed through fs.
type BaseContextProvider struct {
	fs          afero.Fs
	contextHash string
}

// NewBaseContextProvider creates a new context provider on the real filesystem.
func NewBaseContextProvider() *BaseContextProvider {
	return &BaseContextProvider{fs: afero.NewOsFs()}
}

// Provide writes the context file and records its hash.
func (c *BaseContextProvider) Provide(workDir string, fragments []*Fragment) error {
	hash, err := WriteContextFile(workDir, fragments, WithContextFS(c.fs))
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
	if err := c.fs.Remove(contextPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to remove context file for %s: %w", c.contextHash, err)
	}
	c.contextHash = ""
	return nil
}

// GetContextFilePath returns the path to the context file (for env var).
func (c *BaseContextProvider) GetContextFilePath() string {
	if c.contextHash == "" {
		return ""
	}
	return filepath.Join(SCMContextSubdir, c.contextHash+".md")
}
