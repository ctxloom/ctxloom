package opencode

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file is the SINGLE opencode.json writer (slice 2) — the read-modify-write
// merge engine plus the SettingsWriter (agent.SettingsWriter + ContextWriter) that
// drives the static `profile materialize` path. The live chat/oneshot path
// (chat.go) writes the SAME file through the SAME merge core (writeOpencodeConfig)
// — there is exactly ONE opencode.json writer.
//
// opencode's config schema is STRICTLY VALIDATED and CLOSED: it rejects any
// unrecognized key with a loud error ("Configuration is invalid ... Unrecognized
// key"), and its per-server `mcp` entries are additionalProperties:false, so an
// in-file ownership marker is impossible. Every key written here is therefore a
// schema-known key (verified against https://opencode.ai/config.json and
// `opencode debug config` on opencode 1.18.1), and managed ownership is tracked in
// an out-of-file sidecar ledger, as kiro does for its mcp.json.

// opencodeContextFile is the ctxloom-owned context file opencode reads via the
// `instructions` key (opencode's documented "additional instruction files"
// mechanism). It lives under .opencode/ so it sits with opencode's own config
// dir and is obviously not hand-authored project source.
const opencodeContextFile = ".opencode/ctxloom-context.md"

// opencodeMCPPluginKey selects opencode's passthrough MCP servers from
// wire.MCPConfig.Plugins.
const opencodeMCPPluginKey = "opencode"

// readOnlyPermission is the plan-mode read-only posture written into opencode.json's
// `permission` key. It denies the only two workspace-mutation / command-execution
// vectors: `edit` — which gates BOTH the edit and write tools, since opencode has
// no separate `write` permission key (verified: with edit=deny the model reports no
// edit tool and a requested edit leaves the file byte-for-byte unchanged) — and
// `bash`. Denying bash is what makes this a GENUINE read-only mode: opencode's
// built-in `plan` agent denies edit but LEAVES BASH ALLOWED, so a plan agent can
// still `bash` its way to a file write. That gap is exactly why ctxloom writes its
// own permission rather than launching `--agent plan`.
var readOnlyPermission = map[string]string{
	"edit": "deny",
	"bash": "deny",
}

// opencodeMCPLocal is opencode's local (stdio) MCP server shape. Only
// schema-known fields appear — opencode rejects unknown keys. `command` is the
// full argv as a single array (binary followed by its args), and env is spelled
// `environment`.
type opencodeMCPLocal struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment,omitempty"`
	Enabled     bool              `json:"enabled"`
}

// opencodeMCPRemote is opencode's remote (http/sse) MCP server shape (B3, gap
// G11) — VERIFIED against opencode 1.18.1 via `opencode debug config`: a
// `{"type":"remote","url":...,"headers":{...},"enabled":true}` entry round-trips
// with no validation error. opencode has ONE remote shape for both ACP
// transports (it does not distinguish http from sse the way the ACP wire
// does), so both agent.MCPTransportHTTP and agent.MCPTransportSSE map here.
type opencodeMCPRemote struct {
	Type    string            `json:"type"`
	Url     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Enabled bool              `json:"enabled"`
}

// managedConfig is the ctxloom-managed slice of opencode.json for one merge. Every
// field is optional; a zero field leaves that key untouched.
type managedConfig struct {
	model        string
	mcpServers   []agent.ChatMCPServer
	readOnly     bool
	instructions []string
	// skillPaths registers opencode's Agent Skills directory (or directories)
	// in the nested `skills.paths` key. VERIFIED against opencode 1.18.1: the
	// default project skills dir (opencodeSkillDir, ".opencode/skill") is
	// ALREADY auto-discovered with no config entry at all (`opencode debug
	// skill` lists a skill placed there against a config file with no
	// `skills` key whatsoever) — this registration is therefore
	// belt-and-suspenders explicitness, not a load-bearing discovery
	// requirement, kept because it costs nothing and survives a future change
	// to opencode's default scan set.
	skillPaths []string
}

// applyManaged merges ctxloom's managed keys into the parsed opencode.json map,
// preserving every foreign key and merging at the OBJECT level for `mcp`,
// `permission`, and `instructions` (it adds/replaces only OUR entries). It returns
// the managed mcp server names it wrote, for the ledger. A `mcp`/`instructions`
// value of an unexpected JSON type is a malformed config for opencode and fails
// loudly; a scalar `permission` (a valid but rare top-level form) is replaced
// wholesale in read-only mode, since the read-only posture is authoritative.
func applyManaged(cfg map[string]json.RawMessage, m managedConfig) (mcpNames []string, err error) {
	if m.model != "" {
		raw, e := json.Marshal(m.model)
		if e != nil {
			return nil, fmt.Errorf("encode opencode model: %w", e)
		}
		cfg["model"] = raw
	}

	if len(m.mcpServers) > 0 {
		servers := map[string]json.RawMessage{}
		if existing, ok := cfg["mcp"]; ok {
			if e := json.Unmarshal(existing, &servers); e != nil {
				return nil, fmt.Errorf("existing opencode.json mcp is malformed (refusing to overwrite): %w", e)
			}
		}
		for _, s := range m.mcpServers {
			var (
				raw []byte
				e   error
			)
			switch s.Transport {
			case agent.MCPTransportHTTP, agent.MCPTransportSSE:
				raw, e = json.Marshal(opencodeMCPRemote{Type: "remote", Url: s.URL, Headers: s.Headers, Enabled: true})
			default:
				raw, e = json.Marshal(opencodeMCPLocal{
					Type:        "local",
					Command:     append([]string{s.Command}, s.Args...),
					Environment: s.Env,
					Enabled:     true,
				})
			}
			if e != nil {
				return nil, fmt.Errorf("encode opencode mcp %q: %w", s.Name, e)
			}
			servers[s.Name] = raw
			mcpNames = append(mcpNames, s.Name)
		}
		raw, e := json.Marshal(servers)
		if e != nil {
			return nil, fmt.Errorf("encode opencode mcp: %w", e)
		}
		cfg["mcp"] = raw
	}

	if m.readOnly {
		perm := map[string]json.RawMessage{}
		if existing, ok := cfg["permission"]; ok {
			// A scalar/object permission that doesn't decode as an object is
			// replaced: read-only is authoritative, not merged, and we must not
			// leave a user's edit/bash "allow" standing.
			_ = json.Unmarshal(existing, &perm)
		}
		for k, v := range readOnlyPermission {
			// v is always one of readOnlyPermission's own string literals
			// ("deny"), so json.Marshal of a plain string cannot fail; the
			// error is discarded because there is genuinely nothing to check,
			// not because this branch is being sloppy like its neighbors are not.
			raw, _ := json.Marshal(v)
			perm[k] = raw
		}
		raw, e := json.Marshal(perm)
		if e != nil {
			return nil, fmt.Errorf("encode opencode permission: %w", e)
		}
		cfg["permission"] = raw
	}

	if len(m.instructions) > 0 {
		var list []string
		if existing, ok := cfg["instructions"]; ok {
			if e := json.Unmarshal(existing, &list); e != nil {
				return nil, fmt.Errorf("existing opencode.json instructions is malformed (refusing to overwrite): %w", e)
			}
		}
		for _, p := range m.instructions {
			if !slices.Contains(list, p) {
				list = append(list, p)
			}
		}
		raw, e := json.Marshal(list)
		if e != nil {
			return nil, fmt.Errorf("encode opencode instructions: %w", e)
		}
		cfg["instructions"] = raw
	}

	if len(m.skillPaths) > 0 {
		skills := map[string]json.RawMessage{}
		if existing, ok := cfg["skills"]; ok {
			if e := json.Unmarshal(existing, &skills); e != nil {
				return nil, fmt.Errorf("existing opencode.json skills is malformed (refusing to overwrite): %w", e)
			}
		}
		var paths []string
		if existing, ok := skills["paths"]; ok {
			if e := json.Unmarshal(existing, &paths); e != nil {
				return nil, fmt.Errorf("existing opencode.json skills.paths is malformed (refusing to overwrite): %w", e)
			}
		}
		for _, p := range m.skillPaths {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
		rawPaths, e := json.Marshal(paths)
		if e != nil {
			return nil, fmt.Errorf("encode opencode skills.paths: %w", e)
		}
		skills["paths"] = rawPaths
		rawSkills, e := json.Marshal(skills)
		if e != nil {
			return nil, fmt.Errorf("encode opencode skills: %w", e)
		}
		cfg["skills"] = rawSkills
	}

	return mcpNames, nil
}

// loadOpencodeConfig reads and parses opencode.json into a key-preserving map. A
// missing file yields an empty map; a MALFORMED existing file FAILS LOUDLY rather
// than being clobbered — opencode validates this file strictly, and silently
// replacing a user's hand-edited config would lose their settings.
func loadOpencodeConfig(fs afero.Fs, path string) (map[string]json.RawMessage, error) {
	cfg := map[string]json.RawMessage{}
	if exists, _ := afero.Exists(fs, path); exists {
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("existing %s is malformed (refusing to overwrite): %w", path, err)
		}
	}
	return cfg, nil
}

// saveOpencodeConfig re-emits opencode.json deterministically (MarshalIndent sorts
// keys) via the shared atomic write (backup + temp + rename).
func saveOpencodeConfig(fs afero.Fs, path string, cfg map[string]json.RawMessage) error {
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	out = append(out, '\n')
	return agent.AtomicWriteFile(fs, path, out, ConfigFileName)
}

// writeOpencodeConfig is the ONE opencode.json read-modify-write entry point. It
// loads the file (fail-loud on malformed), merges the managed keys (preserving
// every foreign key), and writes it back atomically. Both the live chat path
// (model + mcp + read-only permission) and the settings writer / surfaces
// (mcp + instructions) go through it.
//
// Locked on path, same as the four SettingsWriter entry points below
// (WriteSettings/WriteContext/removeMCP/RemoveSettings): this is the SAME
// opencode.json, and the live chat/interactive launch path (chat.go,
// interactive.go) races those methods otherwise — the exact hazard
// agent.WithFileLock's doc describes.
func writeOpencodeConfig(fs afero.Fs, workDir string, m managedConfig) error {
	path := filepath.Join(workDir, ConfigFileName)
	return agent.WithFileLock(fs, path, func() error {
		cfg, err := loadOpencodeConfig(fs, path)
		if err != nil {
			return err
		}
		if _, err := applyManaged(cfg, m); err != nil {
			return err
		}
		return saveOpencodeConfig(fs, path, cfg)
	})
}

// snapshotOpencodeConfig captures opencode.json's current bytes (or its absence)
// so the transient per-run overlay the chat path writes can be reverted EXACTLY,
// leaving the user's project file as it was — in particular NOT leaving a plan
// run's read-only `permission` behind to silently lock the project. The returned
// closure restores the original bytes, or removes the file if we created it.
//
// The restore closure is locked on path too, same reason as writeOpencodeConfig:
// it is the SAME file the four SettingsWriter entry points read-modify-write,
// and restoring the pre-overlay bytes unlocked could race a concurrent settings
// write into a lost update on the file this whole overlay is meant to leave
// untouched.
func snapshotOpencodeConfig(fs afero.Fs, workDir string) (func() error, error) {
	path := filepath.Join(workDir, ConfigFileName)
	exists, _ := afero.Exists(fs, path)
	if !exists {
		return func() error {
			return agent.WithFileLock(fs, path, func() error {
				if e, _ := afero.Exists(fs, path); e {
					return fs.Remove(path)
				}
				return nil
			})
		}, nil
	}
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", path, err)
	}
	return func() error {
		return agent.WithFileLock(fs, path, func() error {
			return agent.AtomicWriteFile(fs, path, data, ConfigFileName)
		})
	}, nil
}

// stripManagedMCP removes the previously-managed servers (ledger names, plus the
// well-known ctxloom name for pre-ledger files) from cfg's `mcp` object,
// preserving user-authored servers. An emptied `mcp` object is dropped entirely.
// A non-object `mcp` value FAILS LOUDLY rather than silently stripping
// nothing: applyManaged already refuses to overwrite the identical
// condition, and a caller that swallowed this error went on to clear the
// managed-server ledger unconditionally, permanently orphaning those servers
// with no way left to remove them.
func stripManagedMCP(cfg map[string]json.RawMessage, ledger []string) error {
	raw, ok := cfg["mcp"]
	if !ok {
		return nil
	}
	servers := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &servers); err != nil {
		return fmt.Errorf("existing opencode.json mcp is malformed (refusing to modify): %w", err)
	}
	delete(servers, agent.MCPServerName)
	for _, n := range ledger {
		delete(servers, n)
	}
	if len(servers) == 0 {
		delete(cfg, "mcp")
		return nil
	}
	if b, err := json.Marshal(servers); err == nil {
		cfg["mcp"] = b
	}
	return nil
}

// stripManagedInstructions removes ctxloom's context-file entry from cfg's
// `instructions` array, preserving user entries; an emptied array is dropped. It
// reports whether it changed anything.
func stripManagedInstructions(cfg map[string]json.RawMessage) bool {
	raw, ok := cfg["instructions"]
	if !ok {
		return false
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	kept := make([]string, 0, len(list))
	for _, p := range list {
		if p != opencodeContextFile {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(list) {
		return false
	}
	if len(kept) == 0 {
		delete(cfg, "instructions")
	} else if b, err := json.Marshal(kept); err == nil {
		cfg["instructions"] = b
	}
	return true
}

// stripManagedSkillPath removes exactly path from cfg's `skills.paths` array,
// preserving any other registered path and any sibling `skills` key (e.g.
// `urls`); an emptied `paths` drops just that key, and a `skills` object left
// with no keys at all is dropped entirely. It reports whether it changed
// anything, mirroring stripManagedInstructions one level deeper.
func stripManagedSkillPath(cfg map[string]json.RawMessage, path string) bool {
	raw, ok := cfg["skills"]
	if !ok {
		return false
	}
	skills := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &skills) != nil {
		return false
	}
	pathsRaw, ok := skills["paths"]
	if !ok {
		return false
	}
	var paths []string
	if json.Unmarshal(pathsRaw, &paths) != nil {
		return false
	}
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		if p != path {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(paths) {
		return false
	}
	if len(kept) == 0 {
		delete(skills, "paths")
	} else if b, err := json.Marshal(kept); err == nil {
		skills["paths"] = b
	}
	if len(skills) == 0 {
		delete(cfg, "skills")
	} else if b, err := json.Marshal(skills); err == nil {
		cfg["skills"] = b
	}
	return true
}

// registerSkillsPath ensures opencodeSkillDir is registered in opencode.json's
// `skills.paths`, preserving every foreign key. See managedConfig.skillPaths
// for why this is explicitness rather than a load-bearing discovery
// requirement.
//
// Locked on path, same reason as writeOpencodeConfig: it is the SAME
// opencode.json every other managed surface (mcp, instructions) reads-
// modifies-writes, and reconcileSkillsSurface (this function's only caller)
// runs on the same live chat/interactive path writeOpencodeConfig does.
func registerSkillsPath(fs afero.Fs, projectDir string) error {
	path := filepath.Join(projectDir, ConfigFileName)
	return agent.WithFileLock(fs, path, func() error {
		cfg, err := loadOpencodeConfig(fs, path)
		if err != nil {
			return err
		}
		if _, err := applyManaged(cfg, managedConfig{skillPaths: []string{opencodeSkillDir}}); err != nil {
			return err
		}
		return saveOpencodeConfig(fs, path, cfg)
	})
}

// unregisterSkillsPath removes ctxloom's opencodeSkillDir entry from
// opencode.json's `skills.paths`, leaving any other registered path (and any
// absent config file) untouched.
//
// Locked on path: see registerSkillsPath.
func unregisterSkillsPath(fs afero.Fs, projectDir string) error {
	path := filepath.Join(projectDir, ConfigFileName)
	return agent.WithFileLock(fs, path, func() error {
		exists, _ := afero.Exists(fs, path)
		if !exists {
			return nil
		}
		cfg, err := loadOpencodeConfig(fs, path)
		if err != nil {
			return err
		}
		if stripManagedSkillPath(cfg, opencodeSkillDir) {
			return saveOpencodeConfig(fs, path, cfg)
		}
		return nil
	})
}

// composeManagedServers builds the materializable server set the settings writer
// installs: the auto-registered ctxloom server (unless explicitly disabled), bundle
// servers (overridable), config+profile servers, then opencode's passthrough
// servers — the SAME sources and override order codex/MCPFileConfig reconcile, so
// the materialize path matches every other engine. (The chat path receives its set
// pre-composed via ManagedChatMCPServers, so it does not call this.) Unlike the
// chat reconciler, a nil config still auto-registers ctxloom — materialize always
// wants the session's own MCP server.
func composeManagedServers(mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) []agent.ChatMCPServer {
	var out []agent.ChatMCPServer
	add := func(name, cmd string, args []string, env map[string]string) {
		out = append(out, agent.ChatMCPServer{Name: name, Command: cmd, Args: args, Env: env})
	}
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		add(agent.MCPServerName, agent.CtxloomCommand(), agent.CtxloomMCPArgs, nil)
	}
	for name, s := range bundleMCP {
		add(name, s.Command, s.Args, s.Env)
	}
	if mcp != nil {
		for name, s := range mcp.Servers {
			add(name, s.Command, s.Args, s.Env)
		}
		for name, s := range mcp.Plugins[opencodeMCPPluginKey] {
			add(name, s.Command, s.Args, s.Env)
		}
	}
	return out
}

// --- SettingsWriter (agent.SettingsWriter + agent.ContextWriter) ---

// NewWriter constructs the opencode settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &OpencodeWriter{FS: o.FS}
}

// OpencodeWriter writes ctxloom's managed opencode.json keys: the MCP servers
// (`mcp`) and the assembled context (a ctxloom-owned .opencode/ctxloom-context.md
// referenced from `instructions`). opencode.json is SHARED with the user, so every
// write is read-modify-write: foreign keys are preserved and only managed entries
// are added or removed (managed mcp names tracked via the sidecar ledger).
//
// opencode has no ctxloom-style hook mechanism (it has plugins, which ctxloom does
// not author), so WriteSettings ignores the hooks argument. Read-only `permission`
// is a per-RUN posture delivered by the chat path (chat.go), not a materialize-time
// concern, so it is deliberately absent here.
type OpencodeWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
}

func (w *OpencodeWriter) getFS() afero.Fs { return agent.GetFS(w.FS) }

// SettingsPath returns the path to opencode's project-local opencode.json.
func (w *OpencodeWriter) SettingsPath(projectDir string) string {
	return filepath.Join(projectDir, ConfigFileName)
}

func (w *OpencodeWriter) contextFilePath(projectDir string) string {
	return filepath.Join(projectDir, opencodeContextFile)
}

// WriteSettings writes the managed MCP servers into opencode.json's `mcp` key,
// preserving foreign keys and reconciling the managed set against the ledger
// (renamed/removed servers drop out instead of orphaning). opencode has no
// ctxloom-managed hook mechanism, so hooks are ignored.
func (w *OpencodeWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	fs := w.getFS()
	path := w.SettingsPath(projectDir)
	// The lock spans load through save+ledger-write: opencode.json is a
	// SINGLE shared file every managed surface (mcp here, instructions in
	// WriteContext) reads-modifies-writes, and the CLI/runner/MCP server/an
	// in-container ctxloom (same file bind-mounted) all reach it unlocked
	// otherwise — see agent.WithFileLock's doc.
	return agent.WithFileLock(fs, path, func() error {
		cfg, err := loadOpencodeConfig(fs, path)
		if err != nil {
			return err
		}
		ledger, err := w.readLedger(projectDir)
		if err != nil {
			return err
		}
		if err := stripManagedMCP(cfg, ledger); err != nil {
			return err
		}
		servers := composeManagedServers(mcp, bundleMCP)
		names, err := applyManaged(cfg, managedConfig{mcpServers: servers})
		if err != nil {
			return err
		}
		if err := saveOpencodeConfig(fs, path, cfg); err != nil {
			return err
		}
		return w.writeLedger(projectDir, names)
	})
}

// WriteContext writes the assembled context to the ctxloom-owned context file and
// references it from opencode.json's `instructions` key (opencode's documented
// mechanism for additional instruction files). Empty content removes both the file
// and the instructions reference.
func (w *OpencodeWriter) WriteContext(req agent.ContextWriteRequest) (agent.ContextReport, error) {
	fs := w.getFS()
	ctxPath := w.contextFilePath(req.ProjectDir)
	cfgPath := w.SettingsPath(req.ProjectDir)

	// Locked on cfgPath, not ctxPath: this cycle's shared-file hazard is
	// opencode.json (the same file WriteSettings/removeMCP/RemoveSettings
	// read-modify-write for the `mcp` key) — ctxloom-context.md is wholly
	// ctxloom-owned and safe to overwrite unlocked, but writing it INSIDE the
	// same critical section keeps its Wrote/Removed report atomic with the
	// opencode.json edit that references it.
	var report agent.ContextReport
	err := agent.WithFileLock(fs, cfgPath, func() error {
		// TrimSpace, not == "": whitespace-only content is no context at all,
		// and must take this removal path rather than writing a blank
		// instruction file opencode is then pointed at.
		if strings.TrimSpace(req.Context) == "" {
			if exists, _ := afero.Exists(fs, ctxPath); exists {
				if err := fs.Remove(ctxPath); err != nil {
					return err
				}
				report.Removed = append(report.Removed, opencodeContextFile)
			}
			if exists, _ := afero.Exists(fs, cfgPath); exists {
				cfg, err := loadOpencodeConfig(fs, cfgPath)
				if err != nil {
					return err
				}
				if stripManagedInstructions(cfg) {
					if err := saveOpencodeConfig(fs, cfgPath, cfg); err != nil {
						return err
					}
					report.Removed = append(report.Removed, ConfigFileName)
				}
			}
			return nil
		}

		if err := fs.MkdirAll(filepath.Dir(ctxPath), 0755); err != nil {
			return fmt.Errorf("create .opencode directory: %w", err)
		}
		if err := agent.AtomicWriteFile(fs, ctxPath, []byte(req.Context+"\n"), "ctxloom-context.md"); err != nil {
			return err
		}
		cfg, err := loadOpencodeConfig(fs, cfgPath)
		if err != nil {
			return err
		}
		if _, err := applyManaged(cfg, managedConfig{instructions: []string{opencodeContextFile}}); err != nil {
			return err
		}
		if err := saveOpencodeConfig(fs, cfgPath, cfg); err != nil {
			return err
		}
		report = agent.ContextReport{Wrote: []string{opencodeContextFile, ConfigFileName}}
		return nil
	})
	return report, err
}

// removeMCP strips only the managed MCP servers (and clears the ledger), leaving
// the context surface's `instructions` in the SAME file untouched. The config
// surface's cleanup calls this rather than RemoveSettings so the two opencode.json
// surfaces do not clobber each other.
func (w *OpencodeWriter) removeMCP(projectDir string) error {
	fs := w.getFS()
	path := w.SettingsPath(projectDir)
	// See WriteSettings: same file, same lock, same race to close.
	return agent.WithFileLock(fs, path, func() error {
		if exists, _ := afero.Exists(fs, path); exists {
			cfg, err := loadOpencodeConfig(fs, path)
			if err != nil {
				return err
			}
			ledger, err := w.readLedger(projectDir)
			if err != nil {
				return err
			}
			if err := stripManagedMCP(cfg, ledger); err != nil {
				return err
			}
			if err := saveOpencodeConfig(fs, path, cfg); err != nil {
				return err
			}
		}
		return w.writeLedger(projectDir, nil)
	})
}

// RemoveSettings strips every managed entry from opencode.json (mcp, the
// instructions reference, and the skills.paths registration), removes the
// ctxloom context file, and clears the ledger, preserving user-defined keys.
// An absent config file is left absent.
func (w *OpencodeWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()
	path := w.SettingsPath(projectDir)
	// See WriteSettings: same file, same lock, same race to close.
	return agent.WithFileLock(fs, path, func() error {
		if exists, _ := afero.Exists(fs, path); exists {
			cfg, err := loadOpencodeConfig(fs, path)
			if err != nil {
				return err
			}
			ledger, err := w.readLedger(projectDir)
			if err != nil {
				return err
			}
			if err := stripManagedMCP(cfg, ledger); err != nil {
				return err
			}
			stripManagedInstructions(cfg)
			stripManagedSkillPath(cfg, opencodeSkillDir)
			if err := saveOpencodeConfig(fs, path, cfg); err != nil {
				return err
			}
		}
		if exists, _ := afero.Exists(fs, w.contextFilePath(projectDir)); exists {
			if err := fs.Remove(w.contextFilePath(projectDir)); err != nil {
				return err
			}
		}
		return w.writeLedger(projectDir, nil)
	})
}

// Status reports which managed artifacts are wired into opencode.json.
func (w *OpencodeWriter) Status(projectDir string) (agent.SettingsStatus, error) {
	fs := w.getFS()
	var status agent.SettingsStatus
	path := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, path); !exists {
		return status, nil
	}
	status.SettingsExists = true
	cfg, err := loadOpencodeConfig(fs, path)
	if err != nil {
		return status, err
	}
	if raw, ok := cfg["mcp"]; ok {
		servers := map[string]json.RawMessage{}
		if json.Unmarshal(raw, &servers) == nil {
			if _, ok := servers[agent.MCPServerName]; ok {
				status.MCPPresent = true
			}
			ledger, err := w.readLedger(projectDir)
			if err != nil {
				return status, err
			}
			for _, n := range ledger {
				if _, ok := servers[n]; ok {
					status.MCPPresent = true
				}
			}
		}
	}
	if raw, ok := cfg["instructions"]; ok {
		var list []string
		if json.Unmarshal(raw, &list) == nil && slices.Contains(list, opencodeContextFile) {
			status.HooksPresent = true // the instructions reference is opencode's context stand-in
		}
	}
	return status, nil
}

// ledger is this project's managed-content record, scoped to the MCP surface.
// The read/write pair used to live here, independently owned and drifting from
// three near-identical siblings; internal/shared/ledger owns the mechanism now.
func (w *OpencodeWriter) ledger(projectDir string) ledger.Ledger {
	return ledger.Ledger{FS: w.getFS(), Dir: projectDir, Warn: agent.Warn}
}

func (w *OpencodeWriter) readLedger(projectDir string) ([]string, error) {
	return w.ledger(projectDir).Read(ledger.SurfaceMCP)
}

func (w *OpencodeWriter) writeLedger(projectDir string, names []string) error {
	return w.ledger(projectDir).Write(ledger.SurfaceMCP, names)
}

// Compile-time contracts: OpencodeWriter is both a SettingsWriter and a
// ContextWriter (opencode's context rides opencode.json's instructions key).
var (
	_ agent.SettingsWriter = (*OpencodeWriter)(nil)
	_ agent.ContextWriter  = (*OpencodeWriter)(nil)
)
