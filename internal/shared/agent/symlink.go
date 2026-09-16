package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// cachedExecPath stores the resolved executable path (set once at startup).
// Guarded by execPathMu: this package is a dependency of every engine backend,
// so nothing about the seam confines the memoizing write to one goroutine.
var (
	execPathMu     sync.RWMutex
	cachedExecPath string
)

// GetExecutablePath returns the absolute path to the current ctxloom binary.
// The path is resolved once and cached for the lifetime of the process.
//
// This is the CACHED variant, and WarnOnCtxloomPathSkew is its only caller.
// A materialized surface must NOT use it: a surface names the bare executable
// name (CtxloomCommand) so it resolves against PATH at fire time, on whatever
// machine and in whatever cell it fires. This function answers the different
// question WarnOnCtxloomPathSkew asks — which binary is running right now —
// so that the two can be compared.
func GetExecutablePath() (string, error) {
	execPathMu.RLock()
	cached := cachedExecPath
	execPathMu.RUnlock()
	if cached != "" {
		return cached, nil
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

	execPathMu.Lock()
	cachedExecPath = execPath
	execPathMu.Unlock()
	return execPath, nil
}

// SetExecutablePathForTesting allows tests to override the executable path.
// It has no production caller and must not acquire one: the memoized answer is
// a property of the running binary, not a knob.
func SetExecutablePathForTesting(path string) {
	execPathMu.Lock()
	defer execPathMu.Unlock()
	cachedExecPath = path
}

// WarnOnCtxloomPathSkew emits a stderr warning when the `ctxloom` that
// PATH resolves to is not the binary currently running. Every materialized
// surface names the bare `ctxloom` (CtxloomCommand), so a hook, a statusline
// or the MCP stdio entry runs whatever PATH points at when it fires; if that
// differs from the running binary (e.g. an older system package shadows the
// freshly installed one) a hook can fail with "unknown command" for a
// subcommand the older build lacks. This warning is the only thing standing
// between that and silence — bare-name resolution is deliberate, and its
// accepted cost is exactly this divergence.
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
