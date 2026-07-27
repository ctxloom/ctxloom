package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
)

// CtxloomBinary is the bare executable name "ctxloom" — the PATH lookup
// target WarnOnCtxloomPathSkew compares against, and the last-resort value
// selfexec.Path (and so CtxloomCommand) falls back to when self-lookup
// fails. Do NOT use it to materialize a command into a surface (.mcp.json,
// a hook, a statusline) — use CtxloomCommand for that; see its doc for why.
// MCPServerName is the key for the auto-registered ctxloom MCP server, and
// CtxloomMCPArgs its args.
const (
	CtxloomBinary = "ctxloom"
	MCPServerName = "ctxloom"
)

// CtxloomMCPArgs is the arg list for the auto-registered MCP server.
var CtxloomMCPArgs = []string{"mcp"}

// CtxloomCommand returns the command to write into a materialized surface
// (an .mcp.json/config.toml MCP entry, a statusline command, a
// context-injection or hook command) that invokes ctxloom.
//
// INVARIANT: a surface names the absolute path of the binary that
// materialized it, so a staged and an installed binary can never diverge
// within one session. This is load-bearing: a bare "ctxloom" re-resolves
// against PATH at fire time, which is a DIFFERENT resolution than the one
// the process materializing the surface used — an engine harness could
// silently run the installed binary while the user was running a staged
// one (the staged-binary divergence bug this fixes). Falls back to the
// bare name "ctxloom" only if self-lookup fails; see selfexec.Path.
func CtxloomCommand() string {
	return selfexec.Path()
}

// ResolveMCPCommand returns override when non-empty, else CtxloomCommand()'s
// self-exec-absolute default. Every MCP-surface writer's ctxloom stdio entry
// resolves through this one function, so a caller that never sets an override
// (every cell but an isolated CONTAINER) gets byte-for-byte the old
// CtxloomCommand() behavior — the host self-exec-absolute invariant stays
// untouched. The container axis is the ONLY populated caller (see
// isolation.Container.MCPCommandOverride / MCPCommandOverrideEnv): a
// container cell's engine reads its own bind-mounted, identical-path
// .mcp.json, but the binary that materialized the surface may not be the
// binary INSIDE the container (dire-five) — the override substitutes the
// known in-container path (e.g. /usr/local/bin/ctxloom) so the ctxloom MCP
// stdio command is one the container can actually exec.
func ResolveMCPCommand(override string) string {
	if override != "" {
		return override
	}
	return CtxloomCommand()
}

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
func AtomicWriteFile(fs afero.Fs, path string, data []byte, desc string, opts ...WriteFileOption) error {
	var o writeFileOptions
	for _, opt := range opts {
		opt(&o)
	}
	// U102-F08: zero-length data is almost never an intentional settings
	// write — writing it over a live file would silently truncate it to zero
	// bytes on a success path. Refuse by default, matching the "refuse to
	// overwrite, never self-heal" posture corrupt-config handling already
	// uses. The one legitimate exception (codex's config.toml, whose TOML
	// encoder renders an emptied managed set as literally zero bytes, unlike
	// JSON's "{}") opts in explicitly via AllowEmptyWrite — every other
	// caller's removal path goes through fs.Remove instead, never here.
	if len(data) == 0 && !o.allowEmpty {
		if exists, _ := afero.Exists(fs, path); exists {
			return fmt.Errorf("refusing to write %s: assembled zero bytes over an existing file", desc)
		}
	}

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

// WriteFileOption configures AtomicWriteFile's default refusal-of-empty-writes
// behavior (U102-F08).
type WriteFileOption func(*writeFileOptions)

type writeFileOptions struct {
	allowEmpty bool
}

// AllowEmptyWrite opts an AtomicWriteFile call OUT of the zero-byte refusal
// guard, for the rare caller that has already decided — with its own,
// narrower reasoning — that an empty result is legitimate (codex's
// RemoveSettings/save: stripping ctxloom's own keys from a config that held
// nothing else legitimately renders as zero TOML bytes).
func AllowEmptyWrite() WriteFileOption {
	return func(o *writeFileOptions) { o.allowEmpty = true }
}

// RefuseCorrupt is the one refusal shape for "part of this user-owned file
// will not parse, and writing it back would therefore lose whatever could not
// be read".
//
// It backs the original bytes up to <path>.corrupt-<unix-timestamp> and
// returns an error, so the caller aborts before touching the file — the
// backup is the recovery path the error points at.
//
// Every backend that reads a user-editable settings/hooks/MCP file,
// round-trips it, and writes it back must route its partial-parse failures
// here. The alternative — warn and continue with an empty structure — reads
// as "fault tolerance" and IS silent data destruction: the empty structure
// gets persisted over the file it failed to read (taskloom lone-taste,
// U032-F03/F05, U029-F06). A warning is not a guard.
//
// what names the thing that failed to parse; consequence completes
// "refusing to write <file> … <consequence>".
func RefuseCorrupt(fs afero.Fs, path string, data []byte, what string, cause error, consequence string) error {
	name := filepath.Base(path)
	backupPath := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	if err := afero.WriteFile(fs, backupPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to parse %s: %w; additionally failed to back up the corrupt file: %v - refusing to write %s %s", what, cause, err, name, consequence)
	}
	return fmt.Errorf("failed to parse %s: %w - original backed up to %s; fix the JSON and re-run (refusing to write %s %s)", what, cause, backupPath, name, consequence)
}
