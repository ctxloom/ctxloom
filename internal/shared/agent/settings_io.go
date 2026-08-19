package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/selfexec"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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

// CtxloomMCPArgs is the arg list for the auto-registered MCP server: the
// `serve` leaf, which is the one spelling that speaks the protocol. The bare
// `ctxloom mcp` noun answers a human with the configured-server listing, and a
// listing delivered to a client waiting for JSON-RPC reads as a hang — so this
// value is what every materialized surface (.mcp.json, .agents/mcp_config.json,
// .kiro/settings/mcp.json, .codex/config.toml's [mcp_servers], opencode.json)
// must carry. An entry left at the bare noun is reported by
// `ctxloom doctor` (DOCTOR-CHECK-MCP-INVOCATION-g7).
var CtxloomMCPArgs = []string{"mcp", "serve"}

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
// binary INSIDE the container — the override substitutes the
// known in-container path (e.g. /usr/local/bin/ctxloom) so the ctxloom MCP
// stdio command is one the container can actually exec.
func ResolveMCPCommand(override string) string {
	if override != "" {
		return override
	}
	return CtxloomCommand()
}

// ResolveManagedMCPServers returns servers with ctxloom's OWN entry — the one
// the builtin ctxloom bundle contributes under MCPServerName — carrying the
// command and args a materialized surface must name: the self-exec absolute
// path of the binary writing the surface (or override's in-container path, see
// ResolveMCPCommand) and the `mcp serve` leaf (CtxloomMCPArgs).
//
// INVARIANT: a bundle declares WHETHER ctxloom's own server is registered;
// this function fixes WHAT is written, because neither value is knowable when
// a bundle is authored — the absolute path is a fact about the running
// process, and the in-container path a fact about the cell. A server set
// carrying no ctxloom entry is returned unchanged, which is how withholding
// the builtin bundle's item (a profile's exclude_mcp, or rejecting it) turns
// ctxloom's own server off.
//
// servers is never mutated: one resolved bundle set is shared across engines
// and cells, and only some of them carry a container override.
func ResolveManagedMCPServers(servers map[string]wire.MCPServer, override string) map[string]wire.MCPServer {
	own, ok := servers[MCPServerName]
	if !ok {
		return servers
	}
	out := make(map[string]wire.MCPServer, len(servers))
	maps.Copy(out, servers)
	own.Command = ResolveMCPCommand(override)
	own.Args = slices.Clone(CtxloomMCPArgs)
	out[MCPServerName] = own
	return out
}

// SettingsOptions configures a settings-writing operation. It carries the
// filesystem seam and nothing else: per-engine POLICY (which surfaces are
// managed, whether the HUD statusline is one of them) rides the surfaces ×
// cells seam — see agent.SettingsDelivery.DeliverSettings — not this struct,
// which is shared by every backend.
type SettingsOptions struct {
	FS afero.Fs // filesystem to use; nil means the real OS filesystem
}

// SettingsOption is a functional option for settings operations.
type SettingsOption func(*SettingsOptions)

// WithSettingsFS sets the filesystem used for settings operations. If not
// provided, the real OS filesystem is used.
func WithSettingsFS(fs afero.Fs) SettingsOption {
	return func(o *SettingsOptions) { o.FS = fs }
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
// ComputeCommandDigest is the ledger's identity for a hook: a short digest of
// the command string.
//
// The ledger records THIS, never the command itself. A hook command is
// arbitrary user-supplied text — it can carry paths, tokens, or anything else
// the operator put in it — and copying it verbatim into a sidecar would
// duplicate that content into a second file for no gain. A digest is enough to
// recognise "ctxloom wrote this one" on the next reconcile, which is the only
// question the ledger has to answer.
func ComputeCommandDigest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:8])
}

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

// AtomicWriteFile writes data to path atomically: it backs up any existing file
// through iox.WriteFileAtomicFs (unique temp
// in the destination directory, fsync, rename). There is no non-atomic
// fallback: a rename failure is returned, never papered over.
//
// The existing file's mode is preserved across the rewrite, and a brand-new
// file defaults to 0600 (not a world-readable 0644). Settings files written
// here can carry MCPServer.Env secrets (API keys/tokens), so a mode a user
// deliberately tightened must never be silently widened.
func AtomicWriteFile(fs afero.Fs, path string, data []byte, desc string, opts ...WriteFileOption) error {
	var o writeFileOptions
	for _, opt := range opts {
		opt(&o)
	}

	// Default new files to owner-only; reuse the existing mode when present.
	//
	// NO BACKUP IS TAKEN. This used to copy the live bytes to
	// "<path>.ctxloom.bak" first, because a writer that could not tell its own
	// entries from the user's had to rewrite the file wholesale and keep a
	// copy in case it was wrong. Every writer reaching this function now knows
	// what it owns — through the sidecar ledger (internal/shared/ledger) or
	// through in-file managed markers — so it edits its own content and leaves
	// the rest untouched, and there is nothing to recover from. The copies were
	// never free: single-slot and overwritten by the very next write, they had
	// accumulated into the hundreds across a developer's engine config dirs
	// while being least useful in the case that actually recurs, a bad write
	// repeated.
	perm := os.FileMode(0600)
	if info, err := fs.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	// Route through iox: a fixed temp name (path + ".ctxloom.tmp") is exactly
	// the concurrent-clobber hazard iox.WriteFileAtomicFs's unique name exists
	// to prevent, and it fsyncs before the rename. A failed rename here is a
	// real fault and must be reported, never papered over by a DIRECT
	// non-atomic overwrite of the live file that a reader can observe
	// half-finished. Such a fallback would only be justified cross-device,
	// which cannot occur: the temp lives in the destination directory.
	//
	// The zero-length-over-existing refusal is iox's own guard now (promoted
	// from here, fs-consolidation plan C4/Q1): AllowEmptyWrite maps straight
	// onto iox.AllowEmpty rather than this function keeping a second copy of
	// the check. The one legitimate exception (codex's config.toml, whose
	// TOML encoder renders an emptied managed set as literally zero bytes,
	// unlike JSON's "{}") still opts in explicitly; every other caller's
	// removal path goes through fs.Remove instead, never here.
	var iopts []iox.Option
	if o.allowEmpty {
		iopts = append(iopts, iox.AllowEmpty())
	}
	if err := iox.WriteFileAtomicFs(fs, path, data, perm, iopts...); err != nil {
		return fmt.Errorf("failed to write %s: %w", desc, err)
	}
	return nil
}

// WriteFileOption configures AtomicWriteFile's default refusal-of-empty-writes
// behavior.
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
