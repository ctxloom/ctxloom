package backends

import (
	"fmt"
	"os"
	"path/filepath"
)

// cachedExecPath stores the resolved executable path (set once at startup).
var cachedExecPath string

// GetExecutablePath returns the absolute path to the current ctxloom binary.
// The path is resolved once and cached for the lifetime of the process.
func GetExecutablePath() (string, error) {
	if cachedExecPath != "" {
		return cachedExecPath, nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	// Resolve symlinks to get the real path
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve executable path: %w", err)
	}

	cachedExecPath = execPath
	return execPath, nil
}

// SetExecutablePathForTesting allows tests to override the executable path.
func SetExecutablePathForTesting(path string) {
	cachedExecPath = path
}

// GetContextInjectionCommand returns the hook command for context injection.
// Uses absolute path to the current ctxloom binary.
// workDir is the project directory where the context file lives.
func GetContextInjectionCommand(hash, workDir string) string {
	execPath, err := GetExecutablePath()
	if err != nil {
		// Fallback to "scm" if we can't get the path (shouldn't happen)
		execPath = "ctxloom"
	}
	// Include --project flag with absolute path to ensure hook finds the context file
	// even when Claude Code runs from a different working directory
	absWorkDir := workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		absWorkDir = abs
	}
	return fmt.Sprintf(`"%s" hook inject-context --project "%s" %s`, execPath, absWorkDir, hash)
}

// GetMemoryCheckCommand returns the hook command for proactive memory checking.
// Uses absolute path to the current ctxloom binary.
// workDir is the project directory.
func GetMemoryCheckCommand(workDir string) string {
	execPath, err := GetExecutablePath()
	if err != nil {
		// Fallback to "scm" if we can't get the path (shouldn't happen)
		execPath = "ctxloom"
	}
	// Use absolute path for --project to ensure hook works from any directory
	absWorkDir := workDir
	if abs, err := filepath.Abs(workDir); err == nil {
		absWorkDir = abs
	}
	return fmt.Sprintf(`cd "%s" && "%s" memory check`, absWorkDir, execPath)
}

// GetSCMMCPCommand returns the command (executable path) for the SCM MCP server.
// Uses absolute path to the current ctxloom binary.
func GetSCMMCPCommand() string {
	execPath, err := GetExecutablePath()
	if err != nil {
		// Fallback to "scm" if we can't get the path (shouldn't happen)
		return "ctxloom"
	}
	return execPath
}

// GetSCMMCPArgs returns the arguments for the SCM MCP server.
func GetSCMMCPArgs() []string {
	return []string{"mcp"}
}
