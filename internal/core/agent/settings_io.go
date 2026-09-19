package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/spf13/afero"
)

// CtxloomBinary is the bare executable name "ctxloom" — the PATH lookup
// target WarnOnCtxloomPathSkew compares against, and the command every
// materialized surface names (see CtxloomCommand).
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
// INVARIANT: a surface names the BARE executable name and nothing else, so
// it resolves against PATH at fire time, wherever it fires. This is
// load-bearing in two directions. A materialized surface is often a TRACKED
// file (.claude/settings.json, .mcp.json), and an absolute path in one is a
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

// ResolveManagedMCPServers returns servers with ctxloom's OWN entry — the one
// the builtin ctxloom bundle contributes under MCPServerName — carrying the
// command and args a materialized surface must name: the bare ctxloom
// executable (CtxloomCommand) and the `mcp serve` leaf (CtxloomMCPArgs).
//
// INVARIANT: a bundle declares WHETHER ctxloom's own server is registered;
// this function fixes WHAT is written, because the invocation is not knowable
// when a bundle is authored — it is ctxloom's own business, not the declaring
// bundle's. A server set carrying no ctxloom entry is returned unchanged,
// which is how withholding the builtin bundle's item (a profile's
// exclude_mcp, or rejecting it) turns ctxloom's own server off.
//
// The ctxloom entry is CONSTRUCTED, not copied: see ctxloomOwnMCPServer for
// which fields the source may contribute and why Env is not among them. Every
// OTHER name in servers passes through untouched — a third-party MCP server's
// env is that server's own business and reaches its process verbatim.
//
// servers is never mutated: one resolved bundle set is shared across engines
// and cells.
//
// The returned Findings say what a declared ctxloom entry carried that was
// ignored; the caller renders them.
func ResolveManagedMCPServers(servers map[string]wire.MCPServer) (map[string]wire.MCPServer, report.Findings) {
	src, ok := servers[MCPServerName]
	if !ok {
		return servers, nil
	}
	out := make(map[string]wire.MCPServer, len(servers))
	maps.Copy(out, servers)
	var found report.Findings
	out[MCPServerName] = ctxloomOwnMCPServer(report.To(&found), src)
	return out, found
}

// ctxloomOwnMCPServer builds the entry for ctxloom's OWN MCP server from
// ctxloom's own definition, taking from src only the fields that cannot reach
// the spawned process.
//
// Every field that INFLUENCES THE INVOCATION — Command, Args, Env, and the
// remote pair URL/Headers — is constructed here and the source's value for
// it is discarded. Controlling which binary runs is not control if whoever
// declared the entry still chooses its environment, or can point the entry
// at a different endpoint altogether: an env var reaching `ctxloom mcp
// serve` selects its config root, its project and session identity, and
// (CTXLOOM_MCP_SOCKET) the runner it forwards every tool call to, so a
// foreign Env or URL here is a redirection of ctxloom's own control plane,
// not a tweak to a third-party server.
//
// The DESCRIPTIVE fields — Notes, Installation, and the SCM provenance marker
// — are carried through. No engine writer hands them to a process (each
// builds its entry from the invocation fields via ChatMCPServerFromWire);
// they reach only the read-only `ctxloom mcp` listing, where the builtin
// bundle's own notes and its bundle:ctxloom+builtin: source ref are the
// intended content. Dropping them would blank that listing to fix an
// exposure they do not have.
//
// Discarding rather than REFUSING is deliberate, and is the same shape
// config.mcpNameClaims.claim uses: the withholding is unconditional. A
// strictness finding would be disabled by --degraded/CTXLOOM_DEGRADED=1, and
// this property must hold in every mode. The finding is also the wrong
// instrument here — since a contested MCP name is refused at bundle
// resolution, the only source that can still reach this function under the
// ctxloom name is ctxloom's OWN builtin bundle, so a fatal would only ever
// abort a launch (or a read-only `ctxloom mcp` listing) over our own shipped
// content. It is still never silent: a discarded Env is warned about, because
// an operator who wrote one is entitled to know it did nothing.
func ctxloomOwnMCPServer(rep report.Reporter, src wire.MCPServer) wire.MCPServer {
	if len(src.Env) > 0 {
		rep.WarnOncef(
			"ignoring the env declared for the %q MCP server (%s): ctxloom's own MCP server runs with the environment ctxloom gives it, never one supplied by whatever declared the entry",
			MCPServerName, strings.Join(slices.Sorted(maps.Keys(src.Env)), ", "))
	}
	if src.IsRemote() {
		rep.WarnOncef(
			"ignoring the url declared for the %q MCP server (%s): ctxloom's own MCP server is reached the way ctxloom decides, never at an endpoint supplied by whatever declared the entry",
			MCPServerName, src.URL)
	}
	return wire.MCPServer{
		Command:      CtxloomCommand(),
		Args:         slices.Clone(CtxloomMCPArgs),
		Notes:        src.Notes,
		Installation: src.Installation,
		SCM:          src.SCM,
	}
}

// SettingsOptions configures a settings-writing operation. It carries the
// filesystem seam and nothing else: per-engine POLICY (which surfaces are
// managed, whether the HUD statusline is one of them) rides the surfaces ×
// cells seam — each engine's settings approach — not this struct,
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
// Warn is the engine base's remaining route to the process's diagnostic
// channel. It stays until BaseLifecycle, LaunchBackend and the managed
// package writers carry a report.Reporter the engines hand in; the sites
// that already do (ResolveManagedMCPServers, RouteUnifiedHooks,
// ResolveDefault) report findings instead.
func Warn(format string, args ...any) {
	clidiag.Warn("ctxloom", format, args...)
}

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
