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
		// Fallback to "ctxloom" if we can't get the path (shouldn't happen)
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

// GetCtxloomHudCommand returns the command string for the ctxloom statusline HUD.
// Uses absolute path to the current ctxloom binary.
func GetCtxloomHudCommand() string {
	execPath, err := GetExecutablePath()
	if err != nil {
		execPath = "ctxloom"
	}
	return fmt.Sprintf(`"%s" meta hud`, execPath)
}

// GetCtxloomMCPCommand returns the command (executable path) for the ctxloom MCP server.
// Uses absolute path to the current ctxloom binary.
func GetCtxloomMCPCommand() string {
	execPath, err := GetExecutablePath()
	if err != nil {
		// Fallback to "ctxloom" if we can't get the path (shouldn't happen)
		return "ctxloom"
	}
	return execPath
}

// GetCtxloomMCPArgs returns the arguments for the ctxloom MCP server.
func GetCtxloomMCPArgs() []string {
	return []string{"mcp"}
}
