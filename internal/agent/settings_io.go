package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
)

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

// Warn prints a "ctxloom: warning:" line to stderr.
func Warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ctxloom: warning: "+format+"\n", args...)
}

// ComputeHookHash returns a short, stable hash of a hook's defining fields.
func ComputeHookHash(h config.Hook) string {
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

// AtomicWriteFile writes data to path atomically: it backs up any existing file
// to path.ctxloom.bak, writes to a temp file, then renames (falling back to a
// direct write if rename fails cross-device).
func AtomicWriteFile(fs afero.Fs, path string, data []byte, desc string) error {
	if exists, _ := afero.Exists(fs, path); exists {
		backupPath := path + ".ctxloom.bak"
		if origData, err := afero.ReadFile(fs, path); err == nil {
			_ = afero.WriteFile(fs, backupPath, origData, 0644)
		}
	}

	tmpPath := path + ".ctxloom.tmp"
	if err := afero.WriteFile(fs, tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", desc, err)
	}

	if err := fs.Rename(tmpPath, path); err != nil {
		if writeErr := afero.WriteFile(fs, path, data, 0644); writeErr != nil {
			return fmt.Errorf("failed to write %s: %w", desc, writeErr)
		}
		_ = fs.Remove(tmpPath)
	}
	return nil
}
