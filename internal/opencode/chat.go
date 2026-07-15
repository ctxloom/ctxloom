package opencode

import (
	"context"

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
//   - permission: the read-only posture in plan mode — enforced by opencode's own
//     permission layer, which the ACP protocol has no field to carry.
//
// The write is a TRANSIENT overlay: opencode.json is snapshotted first and restored
// after the run, so the user's project file is left exactly as it was (a plan run
// never leaves its read-only `permission` behind).
func (b *Opencode) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
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
	if err := writeOpencodeConfig(fs, req.WorkDir, managedConfig{
		model:      model,
		mcpServers: req.MCPServers,
		readOnly:   req.Permissions == agent.PermissionPlan,
	}); err != nil {
		close(out)
		return err
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
