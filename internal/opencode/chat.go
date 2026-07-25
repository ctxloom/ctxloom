package opencode

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that Opencode offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Opencode)(nil)

// opencodeConfigFile is the project-local config opencode reads (and strictly
// validates) from its cwd. ctxloom merges its managed keys into it (settings.go).
const opencodeConfigFile = "opencode.json"

// Chat implements structured chat by delegating to the generic ACP driver over
// `opencode acp`. opencode speaks ACP natively (first-party subcommand), so no
// third-party adapter is needed.
//
// Before delegating, ctxloom's managed keys are merged into a project-local
// opencode.json in the run's cwd — the ONLY delivery vehicle opencode resolves for
// this run:
//   - model: `opencode acp` has no --model flag, so the model rides opencode.json.
//   - mcp: the managed MCP servers, so `opencode debug config` resolves them (the
//     ACP wire's session/new mcpServers has no bearing on opencode's own config).
//     Editor-supplied http/sse servers (B3, gap G11) ride here too — opencode's
//     own `remote` config shape, NOT the ACP wire (opencode is never asked to
//     advertise mcpCapabilities.http/sse the way claude-code-acp/codex-acp are,
//     since it never receives mcpServers over session/new to begin with).
//   - permission: the read-only posture in plan mode — enforced by opencode's own
//     permission layer, which the ACP protocol has no field to carry.
//
// The write is a TRANSIENT overlay: opencode.json is snapshotted first and restored
// after the run, so the user's project file is left exactly as it was (a plan run
// never leaves its read-only `permission` behind).
func (b *Opencode) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	b.assertSetupRan("structured chat")
	fs := afero.NewOsFs()
	model := req.Model
	if model == "" {
		model = b.model
	}

	restore, err := snapshotOpencodeConfig(fs, req.WorkDir)
	if err != nil {
		close(out) // honor the StructuredChat contract: producer closes out exactly once
		return err
	}
	// Deliver the assembled context the SAME way the interactive path does:
	// the instructions[] key pointing at a ctxloom-owned
	// .opencode/ctxloom-context.md. Chat used to carry model/mcp/permission on
	// this overlay but NOT context, so structured-chat and oneshot
	// `opencode acp` runs saw no project context whatsoever — the run
	// succeeded, the agent simply knew nothing (sick-dairy).
	mc := chatManaged(req, model, b.pendingContext, req.MCPServers)
	removeContext, err := materializeContextSurface(fs, req.WorkDir, b.pendingContext)
	if err != nil {
		_ = restore()
		close(out)
		return err
	}
	if err := writeOpencodeConfig(fs, req.WorkDir, mc); err != nil {
		_ = removeContext()
		_ = restore()
		close(out)
		return err
	}

	// Materialize the host-assembled commands (bundle skill/prompt exports captured
	// during Setup) TRANSIENTLY under .opencode/command/, mirroring the opencode.json
	// overlay: written before the run so opencode discovers them, reverted after so a
	// live run leaves no debris. Only ctxloom-manifest-tracked files are touched, so a
	// user's own commands in the same dir survive; with no exports the dir is left
	// entirely untouched (no manifest write, so a materialized set here is not clobbered).
	revertCmds := func() error { return nil }
	if len(b.pendingCommands) > 0 {
		if err := WriteCommandFiles(req.WorkDir, b.pendingCommands, agent.WithCommandFS(fs)); err != nil {
			_ = removeContext()
			_ = restore()
			close(out)
			return err
		}
		revertCmds = func() error { return WriteCommandFiles(req.WorkDir, nil, agent.WithCommandFS(fs)) }
	}

	// Materialize the host-assembled Agent Skill packages TRANSIENTLY under
	// .opencode/skill/ (+ the opencode.json skills.paths registration), mirroring
	// the commands overlay above: written before the run so opencode discovers
	// them, reverted after so a live run leaves no debris. reconcileSkillsSurface
	// is the SAME function the persistent surfaces.go path binds, so the two
	// paths cannot diverge on what "materializing skills" means.
	revertSkills := func() error { return nil }
	if len(b.pendingSkills) > 0 {
		if err := reconcileSkillsSurface(fs, req.WorkDir, b.pendingSkills); err != nil {
			_ = revertCmds()
			_ = removeContext()
			_ = restore()
			close(out)
			return err
		}
		revertSkills = func() error { return reconcileSkillsSurface(fs, req.WorkDir, nil) }
	}

	// opencode acp takes NO --model flag: it treats the unknown flag as a parse
	// error, prints usage, and exits WITHOUT starting the ACP server — which would
	// break the spawn entirely. Model, MCP, and permission all ride opencode.json
	// (written above), so clear them from the request the driver builds its
	// argv/session-frame from: DO NOT reintroduce a --model flag, and do not ALSO
	// inject the servers over the wire (session/new mcpServers) or opencode would
	// see them twice.
	req.Model = ""
	req.MCPServers = nil

	drv := acp.NewChatDriver(b.chatACPConfig())
	chatErr := drv.Chat(ctx, req, in, out)
	if rerr := revertSkills(); rerr != nil && chatErr == nil {
		chatErr = rerr
	}
	if rerr := revertCmds(); rerr != nil && chatErr == nil {
		chatErr = rerr
	}
	if rerr := removeContext(); rerr != nil && chatErr == nil {
		chatErr = rerr
	}
	if rerr := restore(); rerr != nil && chatErr == nil {
		chatErr = rerr
	}
	return chatErr
}

// chatACPConfig is the adapter config for one opencode structured-chat spawn.
// Command honors the configured binary_path (this host's opencode is not on
// PATH); the driver adopts its first field as the binary and prepends "acp".
//
// It sets NO Model/ModelConfigKey/ModelEnvVar: opencode acp has no --model flag
// and no known model env var — model selection is the opencode.json written in
// Chat. Nor Agent/AgentEngine: opencode acp rejects those flags too.
//
// It likewise sets no ReasoningConfigKey/ReasoningEffort: the cross-engine
// normalized thinking level (off|low|medium|high — see
// internal/claude/chat.go, internal/codex/chat.go) is a DOCUMENTED no-op on
// this backend — no ACP config-option or env-var equivalent was found
// (OpencodeConfig.Thinking's Configure warns if a user sets it, backend.go).
func (b *Opencode) chatACPConfig() acp.ACPConfig {
	return acp.ACPConfig{
		Command: b.BinaryPath + " acp",
		Args:    b.Args,
		Env:     b.Env,
	}
}

// writeModelConfig sets ONLY the `model` key in the project-local opencode.json,
// preserving every other key verbatim. It is the model-only projection of the
// single opencode.json merge engine (writeOpencodeConfig, settings.go): a missing
// file is created with just the model key, an existing file keeps its other keys,
// and a MALFORMED existing file FAILS LOUDLY rather than being clobbered.
func writeModelConfig(fs afero.Fs, workDir, model string) error {
	return writeOpencodeConfig(fs, workDir, managedConfig{model: model})
}

// chatManaged is the pure ChatRequest→opencode.json mapping the structured-chat
// overlay writes, mirroring interactiveManaged so the two paths cannot diverge
// on what a run's managed config contains. Chat has no SkipSetup posture (that
// is an ExecuteRequest concept), so MCP and context always ride.
func chatManaged(req agent.ChatRequest, model, context string, mcp []agent.ChatMCPServer) managedConfig {
	mc := managedConfig{
		model:      model,
		mcpServers: mcp,
		readOnly:   req.Permissions == agent.PermissionPlan,
	}
	if context != "" {
		mc.instructions = []string{opencodeContextFile}
	}
	return mc
}

// materializeContextSurface writes the assembled context to the ctxloom-owned
// .opencode/ctxloom-context.md that the opencode.json instructions[] key points
// at — opencode's documented "additional instruction files" mechanism — and
// returns the revert that removes it again. An empty context writes nothing and
// reverts to a no-op, so a run with no context never points opencode at an
// empty instruction file.
//
// Shared by the interactive launch and the structured-chat overlay: context
// delivery living in only one of them is exactly the gap this closes.
func materializeContextSurface(fs afero.Fs, workDir, context string) (func() error, error) {
	noop := func() error { return nil }
	// TrimSpace, not == "": a context of "\n" or "  " is not a context. Testing
	// for the empty string let 1-4 bytes of whitespace through, writing a
	// ctxloom-context.md that says nothing and pointing opencode's
	// instructions[] at it — a delivery indistinguishable from a real one
	// (U080-F16).
	if strings.TrimSpace(context) == "" {
		return noop, nil
	}
	ctxPath := filepath.Join(workDir, opencodeContextFile)
	if err := fs.MkdirAll(filepath.Dir(ctxPath), 0o755); err != nil {
		return noop, fmt.Errorf("create .opencode directory: %w", err)
	}
	if err := agent.AtomicWriteFile(fs, ctxPath, []byte(context+"\n"), "ctxloom-context.md"); err != nil {
		return noop, err
	}
	return func() error {
		if e, _ := afero.Exists(fs, ctxPath); e {
			return fs.Remove(ctxPath)
		}
		return nil
	}, nil
}
