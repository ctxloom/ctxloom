package backends

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

const (
	// SCMBinDir is the directory for SCM-managed binaries/symlinks
	SCMBinDir = ".scm/bin"
	// SCMBinaryName is the name of the scm binary symlink
	SCMBinaryName = "scm"
)

// symlinkOptions holds configuration for symlink operations.
type symlinkOptions struct {
	fs       afero.Fs
	execPath string // Override for executable path (for testing)
}

// SymlinkOption is a functional option for symlink operations.
type SymlinkOption func(*symlinkOptions)

// WithSymlinkFS sets the filesystem to use for symlink operations.
// Note: For actual symlink creation, use afero.NewOsFs() or a filesystem
// that supports the Linker interface. MemMapFs does not support symlinks.
func WithSymlinkFS(fs afero.Fs) SymlinkOption {
	return func(o *symlinkOptions) {
		o.fs = fs
	}
}

// WithExecPath sets the executable path for testing (skips os.Executable()).
func WithExecPath(path string) SymlinkOption {
	return func(o *symlinkOptions) {
		o.execPath = path
	}
}

// applySymlinkOptions applies the given options and returns the configured options.
func applySymlinkOptions(opts []SymlinkOption) *symlinkOptions {
	options := &symlinkOptions{
		fs: afero.NewOsFs(), // default to real filesystem
	}
	for _, opt := range opts {
		opt(options)
	}
	return options
}

// EnsureSCMSymlink creates a symlink to the current scm binary at .scm/bin/scm.
// This allows hooks to call scm without requiring it to be in PATH.
// workDir is the directory where the .scm/ directory exists.
// Use WithSymlinkFS and WithExecPath for testing.
func EnsureSCMSymlink(workDir string, opts ...SymlinkOption) (string, error) {
	options := applySymlinkOptions(opts)
	fs := options.fs

	// Get the path to the currently running scm binary
	execPath := options.execPath
	if execPath == "" {
		var err error
		execPath, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("failed to get executable path: %w", err)
		}

		// Resolve symlinks to get the real path
		execPath, err = filepath.EvalSymlinks(execPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve executable path: %w", err)
		}
	}

	// Ensure .scm/bin directory exists
	binDir := filepath.Join(workDir, SCMBinDir)
	if err := fs.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create %s directory: %w", SCMBinDir, err)
	}

	// Create/update symlink
	symlinkPath := filepath.Join(binDir, SCMBinaryName)

	// Remove existing symlink if it exists
	if _, err := fs.Stat(symlinkPath); err == nil {
		if err := fs.Remove(symlinkPath); err != nil {
			return "", fmt.Errorf("failed to remove existing symlink: %w", err)
		}
	}

	// Create new symlink - note: symlink creation requires real filesystem
	// For testing with MemMapFs, use a Linker-compatible fs or accept this limitation
	linker, ok := fs.(afero.Linker)
	if ok {
		if err := linker.SymlinkIfPossible(execPath, symlinkPath); err != nil {
			return "", fmt.Errorf("failed to create symlink: %w", err)
		}
	} else {
		// Fallback to os.Symlink for filesystems that don't support linking
		if err := os.Symlink(execPath, symlinkPath); err != nil {
			return "", fmt.Errorf("failed to create symlink: %w", err)
		}
	}

	return symlinkPath, nil
}

// GetContextInjectionCommand returns the hook command for context injection.
// Uses relative path from project root - works for all backends (Claude Code, Gemini, etc.)
func GetContextInjectionCommand(hash string) string {
	// Use relative path - hooks run from project directory
	return fmt.Sprintf(`./%s/%s hook inject-context %s`, SCMBinDir, SCMBinaryName, hash)
}

// GetSCMMCPCommand returns the command (executable path) for the SCM MCP server.
// Uses relative path since MCP commands run from the project directory.
// Note: The "mcp" subcommand should be passed via args, not baked into the command.
func GetSCMMCPCommand() string {
	// Use relative path - MCP servers run from project directory
	return fmt.Sprintf(`./%s/%s`, SCMBinDir, SCMBinaryName)
}

// GetSCMMCPArgs returns the arguments for the SCM MCP server.
func GetSCMMCPArgs() []string {
	return []string{"mcp"}
}
