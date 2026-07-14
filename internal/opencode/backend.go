// Package opencode implements the ctxloom Backend for opencode (the `opencode`
// CLI), driven over its first-party `opencode acp` mode — no third-party ACP
// adapter. This is the HOST-only chat spine (slice 1): structured chat + the
// headless oneshot projection of it, both riding the generic ACP driver in
// internal/acp. opencode has no `--model` flag on its acp subcommand; the model
// is delivered through a project-local opencode.json in the run's cwd (see
// chat.go), which opencode reads and validates strictly.
//
// LIVE-VERIFIED against opencode 1.18.1 authenticated to OpenRouter: a real
// oneshot chat over `opencode acp` round-tripped a requested nonce back through
// meta-llama/llama-3.3-70b-instruct:free, proving model delivery via
// opencode.json reaches real model resolution.
//
// Slice 1 deliberately materializes NO native config beyond the model key:
// opencode reads .claude/skills/ and CLAUDE.md natively, so context/skills need
// no writer here. MCP/permission/instructions writers, a session-history reader,
// interactive PTY launch, and read-only plan mode are later slices.
package opencode

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// OpencodeConfig is opencode's typed LLM config. The backend owns this struct;
// the config package only carries the raw body that decodes into it.
type OpencodeConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (OpencodeConfig) BackendType() string { return "opencode" }

// Opencode implements the Backend interface for the opencode CLI over ACP.
type Opencode struct {
	agent.LaunchBackend
	model string
}

// NewOpencode creates a new opencode backend with default settings.
func NewOpencode() *Opencode {
	b := &Opencode{}
	b.BaseBackend = agent.NewBaseBackend("opencode", "1.0.0")
	// Default binary name; a configured binary_path (this host's opencode is not
	// on PATH) overrides it via Configure/ApplyLocalCLIConfig.
	b.BinaryPath = "opencode"
	// Slice 1 materializes no files: opencode reads .claude/skills/ and CLAUDE.md
	// natively, and the model rides opencode.json written on the chat path. The
	// launch capabilities are the minimal stubs the embedded LaunchBackend
	// requires; the empty CellDelivery still runs the lifecycle merge but writes
	// nothing.
	b.InitLaunch(
		agent.NewBaseLifecycle("opencode"),
		&opencodeSkills{},
		agent.NewBaseContextProvider(),
		&opencodeSessionHistory{},
		&agent.CellDelivery{Build: func(agent.SurfaceInputs, string) agent.SurfaceSet {
			return agent.EmptySurfaceSet{}
		}},
	)
	return b
}

// Configure applies a decoded opencode config (binary path, args, env, model).
func (b *Opencode) Configure(cfg agent.BackendConfig) {
	c, ok := cfg.(*OpencodeConfig)
	if !ok {
		return
	}
	agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	if c.Model != "" {
		b.model = c.Model
	}
}

// SupportedModes narrows the BaseBackend default: like the generic ACP backend,
// opencode's slice-1 path has no TUI, so only oneshot is supported. Interactive
// opencode is a later slice.
func (b *Opencode) SupportedModes() []agent.ExecutionMode {
	return []agent.ExecutionMode{agent.ModeOneshot}
}

// Execute runs a ONESHOT prompt as a single structured ACP turn: one Chat
// session, one message, the assistant's streamed text rendered to stdout — a
// one-message projection of the StructuredChat path (chat.go), so the two paths
// cannot diverge. Model delivery (opencode.json) happens inside Chat.
func (b *Opencode) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// The provider is decided by opencode's own resolution of the openrouter/...
	// model string; "opencode" is honest, not a placeholder.
	modelInfo := &agent.ModelInfo{ModelName: req.Model, Provider: "opencode"}

	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}
	if req.Mode == agent.ModeInteractive {
		return nil, fmt.Errorf("the opencode backend is structured/headless only in this build; interactive opencode is a later slice")
	}

	workDir := req.WorkDir
	if workDir == "" {
		workDir = b.WorkDir()
	}

	in := make(chan agent.ChatMessage, 1)
	in <- agent.ChatMessage{Text: agent.GetPromptContent(req.Prompt)}
	close(in)
	// Buffered so the drain loop below and the driver never deadlock on the final
	// events emitted between the last read and the channel close.
	out := make(chan agent.ChatEvent, 16)

	done := make(chan error, 1)
	go func() {
		done <- b.Chat(ctx, agent.ChatRequest{
			WorkDir:     workDir,
			Model:       req.Model,
			Env:         req.Env,
			Permissions: req.Permissions,
			MCPServers:  b.ManagedChatMCPServers(),
		}, in, out)
	}()

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
