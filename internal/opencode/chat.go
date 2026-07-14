package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that Opencode offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Opencode)(nil)

// opencodeConfigFile is the project-local config opencode reads (and strictly
// validates) from its cwd. ctxloom writes only the `model` key into it.
const opencodeConfigFile = "opencode.json"

// Chat implements structured chat by delegating to the generic ACP driver over
// `opencode acp`. opencode speaks ACP natively (first-party subcommand), so no
// third-party adapter is needed. Before delegating, the requested model is
// written into a project-local opencode.json in the run's cwd — the ONLY way to
// select the model, since `opencode acp` has no --model flag (see chatACPConfig).
func (b *Opencode) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	model := req.Model
	if model == "" {
		model = b.model
	}
	if model != "" {
		if err := writeModelConfig(afero.NewOsFs(), req.WorkDir, model); err != nil {
			close(out) // honor the StructuredChat contract: producer closes out exactly once
			return err
		}
	}
	// opencode acp takes NO --model flag: it treats the unknown flag as a parse
	// error, prints usage, and exits WITHOUT starting the ACP server — which would
	// break the spawn entirely. The model rides opencode.json (written above), so
	// clear it from the request the driver builds argv from. DO NOT reintroduce a
	// --model flag here.
	req.Model = ""

	drv := acp.NewChatDriver(b.chatACPConfig())
	return drv.Chat(ctx, req, in, out)
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
// preserving every other key verbatim. It is strictly read-modify-write:
//   - a missing file is created with just the model key;
//   - an existing file is parsed and re-emitted with only model changed, so
//     unrelated keys survive;
//   - a MALFORMED existing file FAILS LOUDLY rather than being clobbered —
//     opencode validates this file strictly, and silently replacing a user's
//     hand-edited config would lose their settings.
//
// The write is atomic (backup + temp + rename) via the shared AtomicWriteFile.
func writeModelConfig(fs afero.Fs, workDir, model string) error {
	path := filepath.Join(workDir, opencodeConfigFile)

	// json.RawMessage preserves each non-model value's content verbatim; a
	// string-keyed map re-emits deterministically (MarshalIndent sorts keys).
	cfg := map[string]json.RawMessage{}
	if exists, _ := afero.Exists(fs, path); exists {
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("existing %s is malformed (refusing to overwrite): %w", path, err)
		}
	}

	modelJSON, err := json.Marshal(model)
	if err != nil {
		return fmt.Errorf("encode opencode model: %w", err)
	}
	cfg["model"] = modelJSON

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	out = append(out, '\n')
	return agent.AtomicWriteFile(fs, path, out, opencodeConfigFile)
}
