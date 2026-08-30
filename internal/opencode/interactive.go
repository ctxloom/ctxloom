package opencode

import (
	"context"
	"io"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// interactive.go is opencode's PTY / TUI launch path (slice 5). Where the chat
// path (chat.go) drives `opencode acp` for a structured turn, this launches
// opencode's interactive TUI — the DEFAULT subcommand (`opencode` with no
// subcommand token) — through the injected pty launcher (RunInteractive), exactly
// as codex/kiro launch their TUIs.
//
// THE POSTURE MATRIX IS CONFIG-DRIVEN, NOT ARGV-DRIVEN. opencode's TUI resolves the
// SAME project-local opencode.json the acp/run paths do — verified against opencode
// 1.18.1: `opencode debug config` echoes the `permission` block regardless of
// subcommand, so model, MCP, assembled context (`instructions`), and the read-only
// `permission` all ride opencode.json, never argv. The TUI DOES accept -m/--model,
// but model is delivered through opencode.json for parity with chat.go (one model
// delivery vehicle, no divergence). The ONE posture with no config expression is
// bypass: opencode's config `permission` can DENY a tool but the honest
// "auto-approve everything" lever is the --auto flag (the peer of claude's
// --dangerously-skip-permissions), so bypass maps to --auto argv.
//
// The opencode.json overlay is TRANSIENT and its lifetime SPANS the whole
// interactive session: RunInteractive blocks until the user exits the TUI, so the
// managed config (+ context + commands) stays in place for the session and is
// reverted only after — mirroring Chat's transient overlay for a structured turn.
// A plan run never leaves its read-only `permission` behind in the user's project.

// launchInteractive writes the per-posture opencode.json overlay, materializes the
// bundle commands and assembled context transiently, launches opencode's TUI, and
// restores everything after the session ends.
func (b *Opencode) launchInteractive(ctx context.Context, req *agent.ExecuteRequest, modelInfo *agent.ModelInfo, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	workDir := req.WorkDir
	if workDir == "" {
		workDir = b.WorkDir()
	}
	fs := afero.NewOsFs()

	model := req.Model
	if model == "" {
		model = b.model
	}

	b.assertSetupRan("interactive launch")
	restore, err := snapshotOpencodeConfig(fs, workDir)
	if err != nil {
		return nil, err
	}

	// The posture→opencode.json mapping (model, MCP, instructions, read-only
	// permission) is the shared interactiveManaged, so the launch overlay and the
	// posture-matrix test can't diverge. Read-only for plan/SkipSetup is the genuine
	// deny-edit/deny-bash block slice 2 proved (readOnlyPermission); the TUI honors it
	// via config-level permission resolution, so read-only is real in the TUI too.
	mc := interactiveManaged(req, model, b.pendingContext, b.ManagedChatMCPServers(req.Env[agent.MCPCommandOverrideEnv]))

	// When instructions are in play, materialize the assembled context as the
	// ctxloom-owned .opencode/ctxloom-context.md the instructions key points at —
	// opencode's documented "additional instruction files" mechanism.
	removeContext := func() error { return nil }
	if len(mc.instructions) > 0 {
		var cerr error
		if removeContext, cerr = materializeContextSurface(fs, workDir, b.pendingContext); cerr != nil {
			warnRevertFailure("opencode.json (transient overlay)", restore())
			return nil, cerr
		}
	}

	if err := writeOpencodeConfig(fs, workDir, mc); err != nil {
		warnRevertFailure(".opencode/ctxloom-context.md", removeContext())
		warnRevertFailure("opencode.json (transient overlay)", restore())
		return nil, err
	}

	// Materialize bundle commands (skill/prompt exports) transiently under
	// .opencode/command/ so the TUI discovers them, mirroring Chat. SkipSetup delivers
	// none. WriteCommandFiles(nil) on revert removes only ctxloom-manifest files.
	revertCmds := func() error { return nil }
	if !req.SkipSetup && len(b.pendingCommands) > 0 {
		if err := WriteCommandFiles(workDir, b.pendingCommands, agent.WithCommandFS(fs)); err != nil {
			warnRevertFailure(".opencode/ctxloom-context.md", removeContext())
			warnRevertFailure("opencode.json (transient overlay)", restore())
			return nil, err
		}
		revertCmds = func() error { return WriteCommandFiles(workDir, nil, agent.WithCommandFS(fs)) }
	}

	// Materialize bundle Agent Skill packages transiently under .opencode/skill/
	// (+ the opencode.json skills.paths registration), mirroring Chat via the SAME
	// reconcileSkillsSurface function. SkipSetup delivers none.
	revertSkills := func() error { return nil }
	if !req.SkipSetup && len(b.pendingSkills) > 0 {
		if err := reconcileSkillsSurface(fs, workDir, b.pendingSkills); err != nil {
			warnRevertFailure(".opencode/command/", revertCmds())
			warnRevertFailure(".opencode/ctxloom-context.md", removeContext())
			warnRevertFailure("opencode.json (transient overlay)", restore())
			return nil, err
		}
		revertSkills = func() error { return reconcileSkillsSurface(fs, workDir, nil) }
	}

	// The launcher builds its LaunchSpec.WorkDir from b.WorkDir(); Setup already set
	// it, but pin it to the resolved dir so a direct Execute (test/agent) launches in
	// the right cwd.
	b.SetWorkDir(workDir)
	exit, runErr := b.RunInteractive(ctx, b.buildInteractiveArgs(req), req.Env, req.Stdin, req.StdinCleanup, stdout, stderr, req.Resize)

	// LIFO teardown: skills, then commands, then context, then the opencode.json
	// snapshot — the user's project is left exactly as it was.
	if e := revertSkills(); e != nil && runErr == nil {
		runErr = e
	}
	if e := revertCmds(); e != nil && runErr == nil {
		runErr = e
	}
	if e := removeContext(); e != nil && runErr == nil {
		runErr = e
	}
	if e := restore(); e != nil && runErr == nil {
		runErr = e
	}

	return &agent.ExecuteResult{ExitCode: exit, ModelInfo: modelInfo}, runErr
}

// buildInteractiveArgs builds the argv for opencode's interactive TUI. The TUI is
// the DEFAULT subcommand, so NO subcommand token is emitted (unlike codex `exec` /
// kiro `chat`). Model/MCP/context/read-only ride opencode.json (see file header);
// the only argv posture lever is --auto for bypass. A prompt, when present, seeds
// the TUI's first message via --prompt (the run still stays interactive).
func (b *Opencode) buildInteractiveArgs(req *agent.ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	// bypass → --auto (auto-approve every permission not explicitly denied). plan
	// writes a deny block into opencode.json instead and must NOT also get --auto,
	// which would relax the read-only posture. SkipSetup (read-only) never bypasses.
	if req.Permissions == agent.PermissionBypass && !req.SkipSetup {
		args = append(args, "--auto")
	}

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	return args
}

// interactiveManaged is the pure posture→opencode.json mapping the launch path
// writes, split out so the posture matrix is unit-testable without a pty launcher.
// It mirrors launchInteractive's config decisions exactly (model always; MCP +
// instructions unless SkipSetup; read-only permission for plan/SkipSetup).
func interactiveManaged(req *agent.ExecuteRequest, model, context string, mcp []agent.ChatMCPServer) managedConfig {
	mc := managedConfig{model: model}
	if !req.SkipSetup {
		mc.mcpServers = mcp
		if context != "" {
			mc.instructions = []string{opencodeContextFile}
		}
	}
	if req.Permissions == agent.PermissionPlan || req.SkipSetup {
		mc.readOnly = true
	}
	return mc
}
