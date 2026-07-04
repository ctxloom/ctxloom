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

	in := make(chan agent.ChatMessage, 1)
	in <- agent.ChatMessage{Text: agent.GetPromptContent(req.Prompt)}
	close(in)
	// Buffered so the drain loop below and the driver never deadlock on the
	// final events emitted between the last read and the channel close.
	out := make(chan agent.ChatEvent, 16)

	done := make(chan error, 1)
	go func() {
		done <- b.Chat(ctx, agent.ChatRequest{
			WorkDir:     workDir,
			Model:       req.Model,
			Env:         req.Env,
			Permissions: req.Permissions,
		}, in, out)
	}()

	// Render the turn: assistant chunks stream to stdout (concatenated — the
	// mapper emits one entry per chunk); thinking and tool traffic go to stderr
	// only at verbosity, mirroring how the direct oneshot backends keep stdout
	// clean for the response text.
	wroteText := false
	for ev := range out {
		if ev.Entry == nil {
			continue
		}
		switch ev.Entry.Type {
		case agent.EntryTypeAssistant:
			fmt.Fprint(stdout, ev.Entry.Content)
			wroteText = true
		case agent.EntryTypeThinking:
			if req.Verbosity >= 16 {
				fmt.Fprintf(stderr, "[thinking] %s\n", ev.Entry.Content)
			}
		case agent.EntryTypeToolUse:
			if req.Verbosity >= 16 {
				fmt.Fprintf(stderr, "[tool] %s\n", ev.Entry.ToolName)
			}
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if wroteText {
		fmt.Fprintln(stdout)
	}
	return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
}
