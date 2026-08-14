package acp

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that ACP is a complete backend (Execute below is the
// last piece the Backend contract needs on top of the LaunchBackend embedding).
var _ agent.Backend = (*ACP)(nil)

// SupportedModes narrows the BaseBackend default: ACP has no TUI, so only
// oneshot is supported. An interactive session belongs to the target agent's
// own backend (kiro/claude-code/codex direct CLI).
func (b *ACP) SupportedModes() []agent.ExecutionMode {
	return []agent.ExecutionMode{agent.ModeOneshot}
}

// Execute runs a ONESHOT prompt as a single structured ACP turn: one Chat
// session, one message, the assistant's streamed text rendered to stdout. This
// is the same driver the StructuredChat path uses (session.go) — Execute is a
// one-message projection of it, so the two paths cannot diverge.
func (b *ACP) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// The provider is genuinely unknown here — the spawned command decides which
	// engine (and which provider) answers; "acp" is honest, not a placeholder.
	modelInfo := &agent.ModelInfo{ModelName: req.Model, Provider: "acp"}

	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}
	if req.Mode == agent.ModeInteractive {
		return nil, fmt.Errorf("the acp backend is structured/headless only (ACP has no TUI); use the target agent's own backend for an interactive session")
	}

	workDir := req.WorkDir
	if workDir == "" {
		workDir = b.WorkDir()
	}

	// This Execute's twin defect, reprise-flagged byte-for-byte
	// identical to internal/opencode/backend.go's Execute before this fix:
	// this used to inline its own send/drain loop with no empty-prompt check
	// and no diagnostic for a textless turn (exit 0, zero bytes, silent).
	// Both now share this one plumbing.
	//
	// req.SkipSetup is an INTERNAL invocation (distillation/compaction is the
	// only caller today, via memory.Compactor.runDistill's SkipSetup RunStart)
	// — unlike claude-code's native buildArgs, this Chat-routed Execute had no
	// scrub at all for that case: the spawned adapter kept the caller's own
	// CTXLOOM_SESSION_HARP (ambient, never explicit in req.Env) and its
	// SessionStart hook rebound the caller's REAL session to the throwaway
	// distiller conversation. ScrubInternalIdentityEnv forces the harp empty
	// so the hook still fires but binds nothing, matching the --print path's
	// posture. A real delegated child or an interactive session never sets
	// SkipSetup, so its own harp still reaches the engine unchanged.
	env := req.Env
	if req.SkipSetup {
		env = agent.ScrubInternalIdentityEnv(env)
	}
	return agent.RunOneshotTurn(req.Prompt, modelInfo, req.Verbosity, stdout, stderr,
		func(in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
			return b.Chat(ctx, agent.ChatRequest{
				WorkDir:     workDir,
				Model:       req.Model,
				Env:         env,
				Permissions: req.Permissions,
				// The managed MCP set (ctxloom context server, builtin taskloom,
				// config/profile servers) merged by Setup rides session/new — the
				// ACP child reads no engine settings file, so this injection is
				// the structured path's counterpart of Setup's settings write.
				MCPServers: b.ManagedChatMCPServers(req.Env[agent.MCPCommandOverrideEnv]),
			}, in, out)
		})
}
