package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// cachedExecPath stores the resolved executable path (set once at startup).
var cachedExecPath string

// GetExecutablePath returns the absolute path to the current ctxloom binary.
// The path is resolved once and cached for the lifetime of the process.
//
// This is the only place that touches os.Executable. We resolve the path
// live, in-process, rather than persisting it: hook commands and the MCP
// server entry are written as the bare name `ctxloom` (see CtxloomBinary),
// which re-resolves against PATH every time and so never goes stale when
// the binary moves. The one thing a bare name can't catch — a *different*
// ctxloom earlier on PATH than the one that's running — is surfaced by
// WarnOnCtxloomPathSkew, which compares this path against the PATH lookup.
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

// WarnOnCtxloomPathSkew emits a stderr warning when the `ctxloom` that
// PATH resolves to is not the binary currently running. Hooks and the
// MCP server entry are written as the bare name `ctxloom`, so at fire
// time they run whatever PATH points at; if that differs from the
// running binary (e.g. an older system package shadows the freshly
// installed one) a hook can fail with "unknown command" for a
// subcommand the older build lacks. This is the live replacement for
// the absolute path we used to bake into settings.json.
//
// Fault-tolerant by contract: any resolution failure is silent (we
// simply can't make a useful comparison), and a match is silent too.
// It never returns an error and must never block startup.
func WarnOnCtxloomPathSkew() {
	running, err := GetExecutablePath()
	if err != nil {
		return
	}
	onPath, err := exec.LookPath(CtxloomBinary)
	if err != nil {
		// ctxloom isn't on PATH at all. Bare hooks would fail, but the
		// MCP server we're running inside was clearly launchable, so a
		// stripped hook PATH is the likelier cause — and not something
		// this process can fix. Stay quiet rather than cry wolf.
		return
	}
	if resolved, err := filepath.EvalSymlinks(onPath); err == nil {
		onPath = resolved
	}
	if ctxloomPathSkewed(running, onPath) {
		Warn("PATH ctxloom (%s) differs from the running binary (%s) — "+
			"bundle hooks and the statusline run via PATH and may use a "+
			"different version", onPath, running)
	}
}

// ctxloomPathSkewed reports whether the PATH-resolved ctxloom (onPath)
// differs from the running binary (running); both are expected to be
// symlink-resolved by the caller. An empty onPath ("not on PATH") is
// not treated as skew — see WarnOnCtxloomPathSkew for why that case
// stays quiet.
func ctxloomPathSkewed(running, onPath string) bool {
	return onPath != "" && onPath != running
}
