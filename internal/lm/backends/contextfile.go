package backends

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

const (
	// SCMContextDir is the directory for SCM-managed files
	SCMContextDir = ".scm"
	// SCMContextSubdir is the subdirectory for context files
	SCMContextSubdir = ".scm/context"
	// SCMContextFileEnv is the environment variable containing the context file path
	SCMContextFileEnv = "SCM_CONTEXT_FILE"
)

// contextFileOptions holds configuration for context file operations.
type contextFileOptions struct {
	fs afero.Fs
}

// ContextFileOption is a functional option for context file operations.
type ContextFileOption func(*contextFileOptions)

// WithContextFS sets the filesystem to use for context file operations.
// If not provided, the real OS filesystem is used.
func WithContextFS(fs afero.Fs) ContextFileOption {
	return func(o *contextFileOptions) {
		o.fs = fs
	}
}

// applyContextOptions applies the given options and returns the configured options.
func applyContextOptions(opts []ContextFileOption) *contextFileOptions {
	options := &contextFileOptions{
		fs: afero.NewOsFs(), // default to real filesystem
	}
	for _, opt := range opts {
		opt(options)
	}
	return options
}

// WriteContextFile writes the assembled context to a hashed filename in .scm/context/.
// Returns the hash (used as filename without .md extension).
// workDir is the directory where the .scm/ directory exists.
// Use WithContextFS to provide a custom filesystem for testing.
func WriteContextFile(workDir string, fragments []*Fragment, opts ...ContextFileOption) (string, error) {
	options := applyContextOptions(opts)
	fs := options.fs

	// Assemble the context content
	var parts []string
	for _, f := range fragments {
		if f.Content == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(f.Content))
	}

	if len(parts) == 0 {
		// No content - nothing to write
		return "", nil
	}

	content := strings.Join(parts, "\n\n---\n\n")

	// Generate hash-based filename from content
	hash := sha256.Sum256([]byte(content))
	hashStr := hex.EncodeToString(hash[:8]) // First 8 bytes = 16 hex chars

	// Ensure .scm/context directory exists
	contextDir := filepath.Join(workDir, SCMContextSubdir)
	if err := fs.MkdirAll(contextDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s directory: %w", SCMContextSubdir, err)
	}

	// Write context file
	contextPath := filepath.Join(contextDir, hashStr+".md")
	if err := afero.WriteFile(fs, contextPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write context file: %w", err)
	}

	return hashStr, nil
}

// ReadContextFile reads the context file for the given hash from .scm/context/[hash].md.
// Returns empty string if file doesn't exist.
// Use WithContextFS to provide a custom filesystem for testing.
func ReadContextFile(workDir, hash string, opts ...ContextFileOption) (string, error) {
	options := applyContextOptions(opts)
	fs := options.fs

	contextPath := filepath.Join(workDir, SCMContextSubdir, hash+".md")
	content, err := afero.ReadFile(fs, contextPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read context file: %w", err)
	}
	return string(content), nil
}

// ReadContextFileAndDelete reads the context file specified by SCM_CONTEXT_FILE env var,
// then deletes the file. Returns empty string if env var not set or file doesn't exist.
// Use WithContextFS to provide a custom filesystem for testing.
func ReadContextFileAndDelete(workDir string, opts ...ContextFileOption) (string, error) {
	options := applyContextOptions(opts)
	fs := options.fs

	contextPath := os.Getenv(SCMContextFileEnv)
	if contextPath == "" {
		return "", nil
	}

	// If relative path, resolve against workDir
	if !filepath.IsAbs(contextPath) {
		contextPath = filepath.Join(workDir, contextPath)
	}

	content, err := afero.ReadFile(fs, contextPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read context file: %w", err)
	}

	// Clean up after reading
	if err := fs.Remove(contextPath); err != nil {
		// Log but don't fail - content was read successfully
		fmt.Fprintf(os.Stderr, "warning: failed to delete context file %s: %v\n", contextPath, err)
	}

	return string(content), nil
}

