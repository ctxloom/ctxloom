package claude

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
)

// claude-code's per-engine EXPORT BLOCK: the opaque bytes a bundle item
// carries under this engine's name, decoded HERE and nowhere else. The block
// is one shape for both item kinds — a command uses every field, a skill
// only `enabled` — and its schema is what Definition.ExportSchema publishes,
// so an author's block is judged by the same document `gen-schemas` writes.

// ExportSchema is the JSON schema of claude-code's export block.
var ExportSchema = []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "claude-code export block",
  "description": "How a bundle command surfaces as a Claude Code slash command, and whether a skill package is offered. Absent means enabled (opt-out).",
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean", "description": "Set to false to withhold the item from claude-code. Absent means enabled."},
    "description": {"type": "string", "description": "Shown in /help for the slash command."},
    "argument_hint": {"type": "string", "description": "Autocomplete hint for the slash command's arguments."},
    "allowed_tools": {"type": "array", "items": {"type": "string"}, "description": "Tool restrictions for the slash command; absent means every tool."},
    "model": {"type": "string", "description": "Model override for the slash command."}
  },
  "additionalProperties": false
}`)

// ExportBlock is claude-code's export block, decoded. The zero value is the
// block of an item that declares none: enabled, nothing else set.
type ExportBlock struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	Description  string   `json:"description,omitempty"`
	ArgumentHint string   `json:"argument_hint,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Model        string   `json:"model,omitempty"`
}

// IsEnabled reports the effective enablement: absent means enabled.
func (b ExportBlock) IsEnabled() bool { return b.Enabled == nil || *b.Enabled }

var (
	exportValidatorOnce sync.Once
	exportValidator     *schema.ConfigValidator
	exportValidatorErr  error
)

// DecodeExportBlock decodes an item's block for this engine, validated
// against ExportSchema. nil is the absent block. A block the schema refuses
// is an error naming the engine: the item is withheld from claude-code, not
// exported with a guess.
func DecodeExportBlock(raw json.RawMessage) (ExportBlock, error) {
	if raw == nil {
		return ExportBlock{}, nil
	}
	exportValidatorOnce.Do(func() {
		exportValidator, exportValidatorErr = schema.NewValidatorFromSchema(ExportSchema)
	})
	if exportValidatorErr != nil {
		return ExportBlock{}, fmt.Errorf("%s: export schema: %w", EngineName, exportValidatorErr)
	}
	if err := exportValidator.ValidateBytes(raw); err != nil {
		return ExportBlock{}, fmt.Errorf("%s: export block: %w", EngineName, err)
	}
	var block ExportBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		return ExportBlock{}, fmt.Errorf("%s: export block: %w", EngineName, err)
	}
	return block, nil
}

// Exports decodes each item's claude-code block and says what this engine
// exports: a command is a slash command unless its block opts out (a
// profile-curated one exports regardless), with the block's help text or,
// absent that, the authored description; a skill package is offered unless
// its block opts out. A block the schema refuses is an error naming the
// engine and the item — nothing is exported on a guess.
func (c Claude) Exports(items engine.Items) (engine.Exports, error) {
	var out engine.Exports
	for _, item := range items.Commands {
		block, err := DecodeExportBlock(item.Exports)
		if err != nil {
			return engine.Exports{}, fmt.Errorf("command %q: %w", item.Ref, err)
		}
		description := block.Description
		if description == "" {
			description = item.Description
		}
		out.Commands = append(out.Commands, engine.CommandExport{
			Name:         item.Name,
			Body:         item.Body,
			Enabled:      item.Curated || block.IsEnabled(),
			Description:  description,
			ArgumentHint: block.ArgumentHint,
			AllowedTools: block.AllowedTools,
			Model:        block.Model,
		})
	}
	for _, item := range items.Skills {
		block, err := DecodeExportBlock(item.Exports)
		if err != nil {
			return engine.Exports{}, fmt.Errorf("skill %q: %w", item.Ref, err)
		}
		out.Skills = append(out.Skills, engine.SkillExport{
			Name:        item.Name,
			Description: item.Description,
			Files:       item.Files,
			Enabled:     item.Curated || block.IsEnabled(),
		})
	}
	return out, nil
}
