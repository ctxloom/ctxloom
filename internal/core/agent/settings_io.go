package agent

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/spf13/afero"
)

// CtxloomBinary is the bare executable name "ctxloom" — the PATH lookup
// target WarnOnCtxloomPathSkew compares against, and the command every
// materialized surface names (see CtxloomCommand).
// MCPServerName is the key under which ctxloom's own companion loadout
// declares ctxloom's MCP server (cmd/ctxloom/loadout.yaml) — the
// session-endpoint declaration, never a command.
const (
	CtxloomBinary = "ctxloom"
	MCPServerName = wire.LayerServerName
)

// CtxloomCommand returns the command to write into a materialized surface
// (a statusline command, a context-injection or hook command) that invokes
// ctxloom.
//
// INVARIANT: a surface names the BARE executable name and nothing else, so
// it resolves against PATH at fire time, wherever it fires. This is
// load-bearing in two directions. A materialized surface is often a TRACKED
// file (.claude/settings.json), and an absolute path in one is a
// fact about one developer's machine that every other clone inherits and
// cannot satisfy — their hooks then fail silently. And a surface written on
// the host is read inside a container, where a host path does not exist at
// all.
//
// ACCEPTED COST: the binary that fires a surface need not be the binary that
// materialized it. A different ctxloom earlier on PATH, or none, resolves
// instead. Do not reintroduce an absolute path, a guard or an override to
// close that gap.
func CtxloomCommand() string {
	return CtxloomBinary
}

// SettingsOptions configures a settings-writing operation. It carries the
// filesystem seam and the Reporter, nothing else: per-engine POLICY (which
// surfaces are managed, whether the HUD statusline is one of them) rides the
// surfaces × cells seam — each engine's settings approach — not this struct,
// which is shared by every backend.
type SettingsOptions struct {
	FS afero.Fs // filesystem to use; nil means the real OS filesystem
	// ProjectClaims lists the places in a file that the ownership record says
	// the project writer claims AND the file holds now — what a status read
	// reports as installed. nil knows of none.
	ProjectClaims func(target string) ([]string, error)
}

// SettingsOption is a functional option for settings operations.
type SettingsOption func(*SettingsOptions)

// WithSettingsFS sets the filesystem used for settings operations. If not
// provided, the real OS filesystem is used.
func WithSettingsFS(fs afero.Fs) SettingsOption {
	return func(o *SettingsOptions) { o.FS = fs }
}

// WithSettingsProjectClaims hands the status read the ownership record's
// account of what the project writer has installed (SettingsOptions).
func WithSettingsProjectClaims(claims func(target string) ([]string, error)) SettingsOption {
	return func(o *SettingsOptions) { o.ProjectClaims = claims }
}

// GetFS returns fs, or the OS filesystem when fs is nil.
func GetFS(fs afero.Fs) afero.Fs {
	if fs == nil {
		return afero.NewOsFs()
	}
	return fs
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
// gets persisted over the file it failed to read. A warning is not a guard.
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
