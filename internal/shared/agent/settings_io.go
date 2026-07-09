package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
)

// CtxloomBinary is the bare executable name written into managed hook/MCP
// commands so it re-resolves via PATH at fire time. MCPServerName is the key for
// the auto-registered ctxloom MCP server, and CtxloomMCPArgs its args.
const (
	CtxloomBinary = "ctxloom"
	MCPServerName = "ctxloom"
)

// CtxloomMCPArgs is the arg list for the auto-registered MCP server.
var CtxloomMCPArgs = []string{"mcp"}

// SettingsOptions configures a settings-writing operation.
type SettingsOptions struct {
	FS                 afero.Fs // filesystem to use; nil means the real OS filesystem
	StatusLineDisabled bool     // opt out of managing the ctxloom HUD statusline
}

// SettingsOption is a functional option for settings operations.
type SettingsOption func(*SettingsOptions)

// WithSettingsFS sets the filesystem used for settings operations. If not
// provided, the real OS filesystem is used.
func WithSettingsFS(fs afero.Fs) SettingsOption {
	return func(o *SettingsOptions) { o.FS = fs }
}

// WithStatusLineDisabled controls whether the ctxloom HUD statusline is managed.
// When disabled, the writer installs no statusline and clears any it previously
// managed, so the user's own (or no) statusline stands.
func WithStatusLineDisabled(disabled bool) SettingsOption {
	return func(o *SettingsOptions) { o.StatusLineDisabled = disabled }
}

// GetFS returns fs, or the OS filesystem when fs is nil.
func GetFS(fs afero.Fs) afero.Fs {
	if fs == nil {
		return afero.NewOsFs()
	}
	return fs
}

// Warn prints a "ctxloom: warning:" line to stderr. Thin wrapper over
// clidiag.Warn so the family's "<prog>: warning:" format lives in exactly one
// place; the ctxloom-family callers here (the agent-engine libs, settings and
// context internals) all warn under the ctxloom name.
func Warn(format string, args ...any) {
	clidiag.Warn("ctxloom", format, args...)
}

// ComputeHookHash returns a short, stable hash of a hook's defining fields.
func ComputeHookHash(h wire.Hook) string {
	parts := []string{
		h.Command,
		h.Matcher,
		h.Type,
		h.Prompt,
		fmt.Sprintf("%d", h.Timeout),
		fmt.Sprintf("%t", h.Async),
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:8]) // first 8 bytes for brevity
}

// ComputeMCPServerHash returns a short, stable hash of an MCP server's defining
// fields, used as the `_ctxloom` marker on managed servers.
func ComputeMCPServerHash(s wire.MCPServer) string {
	parts := append([]string{s.Command}, s.Args...)
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:8])
}

// AtomicWriteFile writes data to path atomically: it backs up any existing file
// to path.ctxloom.bak, writes to a temp file, then renames (falling back to a
// direct write if rename fails cross-device).
//
// The existing file's mode is preserved across the rewrite, and a brand-new
// file defaults to 0600 (not a world-readable 0644). Settings files written
// here can carry MCPServer.Env secrets (API keys/tokens), so a mode a user
// deliberately tightened must never be silently widened; the backup copy is
// written with the same restrictive mode rather than a hardcoded 0644.
func AtomicWriteFile(fs afero.Fs, path string, data []byte, desc string) error {
	// Default new files to owner-only; reuse the existing mode when present.
	perm := os.FileMode(0600)
	if info, err := fs.Stat(path); err == nil {
		perm = info.Mode().Perm()
		backupPath := path + ".ctxloom.bak"
		if origData, err := afero.ReadFile(fs, path); err == nil {
			_ = afero.WriteFile(fs, backupPath, origData, perm)
		}
	}

	tmpPath := path + ".ctxloom.tmp"
	if err := afero.WriteFile(fs, tmpPath, data, perm); err != nil {
		return fmt.Errorf("failed to write %s: %w", desc, err)
	}

	if err := fs.Rename(tmpPath, path); err != nil {
		if writeErr := afero.WriteFile(fs, path, data, perm); writeErr != nil {
			return fmt.Errorf("failed to write %s: %w", desc, writeErr)
		}
		_ = fs.Remove(tmpPath)
	}
	return nil
}
